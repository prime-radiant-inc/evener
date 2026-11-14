// PendingRowStack: every user message that has not fully delivered to the
// session rendered as the provisional user message it will become - one
// transcript-shaped row per message, same UserMessageView construction a
// delivered message gets, actions on the row. This is the ONE surface for
// undelivered user input (Jesse's ruling, 2026-11-13): no dock below the
// transcript (the retired QueueStrip), no dimmed chip beside the composer
// (the retired PendingChips). The families:
//
// - pending entries (usePendingTurnEntries): every method - steer/drain/
//   promote hold through the running step exactly as the steering-ghost
//   spec §2 defined them; send/queue render as the message awaiting its
//   turn, with the same provisional caption the held steers carry. This
//   stack never re-sorts: reconcilePendingEntries' shared known-first
//   order (§4) is the order the whole transcript reads.
// - the daemon's authoritative queue (model.queue): real rows the hub
//   already accepted, rendered in queue position order after the pending
//   entries, each with its Steer now / Edit / Remove actions.
// - durable blocked/canceled rows: delivery-uncertain and Stop-canceled
//   submissions with their Retry affordance.
// - recovery rows: a refused submission's durable record with its Edit (or
//   Dismiss for a failed interrupt) affordance.
//
// The stack renders as the transcript's trailing virtual-list row
// (ConversationBody's trailingRow), so it always sits below anything the
// server has sent and scrolls with the content instead of floating over it.
// Order arrives from reconcilePendingEntries' shared sort for pending
// entries; the daemon queue's rows follow in their own array order (the
// hub's queue position), then the durable/recovery families' intent
// sequence (QueueStrip's own ordering, carried over unchanged).

import {
  type ComposerMention,
  canonicalSkillNames,
  errorText,
  formatElapsed,
  type InputItem,
  isTurnActive,
  sessionActionError,
} from "@evener/appwire-client";
import { canEditQueuedMessage, canRemoveQueuedMessage } from "@evener/appwire-client/state/mutation";
import type { JSX } from "react";
import { useContext, useEffect, useRef, useState } from "react";
import { copyToClipboard } from "../../../../shell/palette/commands";
import { controlsFor, pressLocalRecoveryFenced, pressRefusal } from "../../../../stores/liveControls";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import {
  retryBlockedForTarget,
  threadsStore,
  useEditableQueuedMutationEntries,
  useThreadsStore,
} from "../../../../stores/threads";
import { Button, IconButton, useToasts } from "../../../../widgets";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import {
  discardRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  removeQueuedPendingTurn,
  retryBlockedPendingTurn,
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useQueueStripMutationIds,
  useRecoveryEntries,
} from "../../composer/queue/pendingTurnsStore";
import {
  commandMarkers,
  pendingEntryPreview,
  skillMarkers,
  truncateForDisplay,
} from "../../composer/queue/queueDisplay";
import { requestQueueRowRestore, useActiveRecoveryId, useComposerBusy } from "../../composer/queueRowInsert";
import { SessionNowContext } from "../../liveness";
import styles from "./pendingrowstack.module.css";
import { UserMessageView } from "./UserMessageItem";

// The steer-family filter, kept for Session's epoch and the announcements
// region (the held-steer family's live-gate consumers). The stack itself
// renders every family now.
export function heldSteerEntries(
  entries: readonly PendingTurnEntry[],
  queueStripIds?: ReadonlySet<string>,
): PendingTurnEntry[] {
  return entries.filter(
    (entry) =>
      (entry.method === "steer" || entry.method === "drain" || entry.method === "promote") &&
      !queueStripIds?.has(entry.id) &&
      entry.state !== "blockedUnknown" &&
      entry.state !== "canceled",
  );
}

// The caption state table (steering-ghost spec §4, extended to send/queue):
// both arms read the thread STATUS TYPE (isTurnActive), never
// model.activeTurnId - the projector closes one turn row before opening the
// next, so the id goes false between inline turns while the run continues
// (#1330). The count is elapsed time from createdAt through the package's
// formatElapsed, and is omitted entirely when createdAt is unknown - never
// NaN, never a false 0s.
export interface HeldCaptionParts {
  stage: "next-turn" | "step-finishes" | "joining" | "waiting-for-hub" | "sending" | "queued";
  held: boolean;
  elapsedMs?: number;
}

const HELD_STAGE_LABELS: Record<HeldCaptionParts["stage"], string> = {
  "next-turn": "Delivers with the next turn",
  "step-finishes": "Delivers when this step finishes",
  joining: "Joining this turn",
  "waiting-for-hub": "Waiting for the hub",
  sending: "Sending",
  queued: "Queued for the next turn",
};

export function heldCaptionParts(
  entry: PendingTurnEntry,
  turnActive: boolean,
  now: number,
  hubRefused = false,
): HeldCaptionParts {
  if (entry.method === "send")
    return {
      stage: "sending",
      held: false,
      ...(entry.createdAt !== undefined ? { elapsedMs: now - entry.createdAt } : {}),
    };
  if (entry.method === "queue")
    return {
      stage: "queued",
      held: false,
      ...(entry.createdAt !== undefined ? { elapsedMs: now - entry.createdAt } : {}),
    };
  let stage: HeldCaptionParts["stage"];
  let held: boolean;
  if (hubRefused) {
    // A hub-refused steer waits on the store's paced retry, not on the turn:
    // the caption must not read as an ordinary in-flight join while the hub
    // has explicitly not accepted the row yet.
    stage = "waiting-for-hub";
    held = false;
  } else {
    held = entry.state === "accepted";
    // An accepted steer waits out the running step; a submitting one joins
    // the turn already in flight.
    stage = !turnActive ? "next-turn" : held ? "step-finishes" : "joining";
  }
  return entry.createdAt === undefined ? { stage, held } : { stage, held, elapsedMs: now - entry.createdAt };
}

export function heldCaption(entry: PendingTurnEntry, turnActive: boolean, now: number, hubRefused = false): string {
  const { stage, held, elapsedMs } = heldCaptionParts(entry, turnActive, now, hubRefused);
  const label = HELD_STAGE_LABELS[stage];
  if (elapsedMs === undefined) return label;
  const elapsed = formatElapsed(elapsedMs);
  return held ? `${label} · held ${elapsed}` : `${label} · ${elapsed}`;
}

// The pending set's arrival counter, mirroring askDockStore's activationEpoch
// semantics: bumps when an id joins the current pending set, never on removal
// (a departure is the announcements region's job, not new content). The
// comparison is against the PREVIOUS observation, not a lifetime set: a
// retried blocked/canceled mutation re-enters holding the id it already had,
// and its reappeared row is new content again for a scrolled-away reader.
// `visible` carries Session's §1 live gate: a suppressed surface counts no
// arrivals.
export function useHeldSteerEpoch(ref: string, entries: readonly PendingTurnEntry[], visible: boolean): number {
  const [epoch, setEpoch] = useState(0);
  const prevRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);
  const visibleEntries = visible ? entries : [];
  const ids = new Set(visibleEntries.map((entry) => entry.id));
  useEffect(() => {
    const prev = prevRef.current;
    prevRef.current = { ref, ids };
    if (prev?.ref !== ref) return;
    const added = visible && entries.some((entry) => !prev.ids.has(entry.id));
    if (!added) return;
    setEpoch((count) => count + 1);
  });
  return epoch;
}

// recordContent: a durable record's display composition (QueueStrip's own
// helper, carried over): the optimisticDisplay.input carries a promoted row's
// composed content (its wire params carry only the queue position), so it is
// preferred over the payload input every other method populates.
function recordContent(record: MutationOutboxRecord): {
  text: string;
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
} {
  const display = record.optimisticDisplay;
  const displayInput =
    display && typeof display === "object" && "input" in display && Array.isArray(display.input)
      ? (display.input as InputItem[])
      : undefined;
  const input = displayInput ?? (Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : []);
  const text = input
    .filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n");
  return {
    text,
    imageCount: input.filter((item) => item.type === "image").length,
    skillNames: canonicalSkillNames(
      input
        .filter((item): item is InputItem & { name: string } => item.type === "skill" && typeof item.name === "string")
        .map((item) => item.name),
    ),
    commandNames: canonicalSkillNames(input.filter((item) => item.type === "command").map((item) => item.name ?? "")),
  };
}

function recordPreview(record: MutationOutboxRecord): string {
  return truncateForDisplay(pendingEntryPreview(recordContent(record)));
}

export function PendingRowStack({ ref: sessionRef }: { ref: string }): JSX.Element | null {
  const entries = usePendingTurnEntries(sessionRef);
  const queueStripIds = useQueueStripMutationIds(sessionRef);
  const blockedEntries = useBlockedMutationEntries(sessionRef).filter((record) => record.method !== "notes/human/set");
  const canceledEntries = useCanceledMutationEntries(sessionRef).filter(
    (record) => record.method !== "notes/human/set",
  );
  const recoveryEntries = useRecoveryEntries(sessionRef).filter((record) => record.method !== "notes/human/set");
  // The composer's live recovery composition never renders as a row: the
  // message being edited is already visible as the composition under edit,
  // and the retired strip's activeRecoveryId exclusion moves with it.
  const activeRecoveryId = useActiveRecoveryId(sessionRef);
  const composerBusy = useComposerBusy(sessionRef);
  const visibleRecoveryEntries = activeRecoveryId
    ? recoveryEntries.filter((record) => record.clientMutationId !== activeRecoveryId)
    : recoveryEntries;
  // This client's unattempted editable rows (the queueStripIds set's own
  // selector): an empty drain or a remove-only promotion parks here as a
  // durable local row - the waiting family, rendered with its own
  // Edit/Remove affordances below.
  const editableEntries = useEditableQueuedMutationEntries(sessionRef).filter(
    (record) => record.state === "submitting",
  );
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const refusedId = useThreadsStore((s) => s.transientRefusalPaces.get(sessionRef)?.clientMutationId);
  // The caption reads the same clock the liveness line does: Session's 3s
  // SessionNowContext tick, so no per-second machinery re-renders the
  // virtualized row.
  const now = useContext(SessionNowContext);
  const toasts = useToasts();
  const [busyEntryIds, setBusyEntryIds] = useState<ReadonlySet<string>>(new Set());
  const [retryErrors, setRetryErrors] = useState<ReadonlyMap<string, string>>(new Map());
  const localActions = useRef(new Set<string>());

  const queue = model?.queue ?? null;
  const ids = queue?.ids;
  const texts = queue?.texts;
  const preview = queue?.preview;
  const rowCount = preview?.length ?? texts?.length ?? ids?.length ?? 0;
  const hasIds = ids !== undefined;
  const hasTexts = texts !== undefined;
  const depth = queue?.depth ?? 0;
  const pendingQueueEntries = entries.filter(
    (entry) =>
      entry.method === "queue" &&
      entry.state !== "blockedUnknown" &&
      entry.state !== "canceled" &&
      !queueStripIds.has(entry.id),
  );
  // The daemon's queue rows this client also holds a durable optimistic
  // record for: the optimistic entry renders instead (it carries createdAt),
  // so the daemon row and its optimistic twin never both render.
  const pendingTexts = new Set(pendingQueueEntries.map((entry) => pendingEntryPreview(entry)));
  // Edits remove the daemon row first and leave the optimistic pending row
  // for the composition; the pending side owns everything it can be
  // reconciled against.

  // Retry eligibility is ROW-scoped: each durable row's Retry reads its own
  // target's flags, so subscribe to the target flags as one primitive key.
  const retryTargets = [...new Set([...blockedEntries, ...canceledEntries].map((record) => record.targetRef))];
  useThreadsStore((s) =>
    retryTargets
      .map((target) =>
        [
          s.threads.get(target)?.status.type ?? "",
          s.mutationAuthorityRefs.has(target),
          s.restartBlockingObligations.has(target),
          s.mutationReconciliationStorageBlocked.has(target),
        ].join("\u0000"),
      )
      .join("\u0001"),
  );

  const turnActive = isTurnActive(model?.status.type ?? "");

  // Steer-now on a daemon queue row: promote that row into the running turn.
  async function handlePromote(index: number, entryId: string, displayText: string): Promise<void> {
    // The recovery fence, re-read live at the press (the same render-vs-press
    // rule every queue press follows): the hub refuses
    // turn/promoteQueuedAsSteer for the obligation's whole window.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", "Queue actions aren't available until this session is resumed");
      return;
    }
    const refusal = pressRefusal(sessionRef, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      return;
    }
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().promoteQueuedAsSteer(sessionRef, index, entryId, { text: displayText });
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't steer with this message now", err));
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleCancel(index: number, entryId: string): Promise<void> {
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", "Queue actions aren't available until this session is resumed");
      return;
    }
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't remove this message from the queue", err));
    } finally {
      setRowBusy(entryId, false);
    }
  }

  // Edit a daemon queue row: loser-safe order - the row's full text reaches
  // the composer FIRST (through the restore seam; Composer owns the merge
  // and the caret), then the queued copy is removed. A failure on the remove
  // half leaves the user's text in the composer and an honest toast, never a
  // silently duplicated message.
  function handleEdit(
    index: number,
    entryId: string,
    fullText: string,
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): void {
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", "Queue actions aren't available until this session is resumed");
      return;
    }
    setRowBusy(entryId, true);
    try {
      requestQueueRowRestore(
        sessionRef,
        mentions
          ? { text: fullText, skillNames, commandNames, mentions, target: null }
          : { text: fullText, skillNames, commandNames, target: null },
      );
      void threadsStore
        .getState()
        .cancelQueued(sessionRef, index, entryId)
        .catch((err: unknown) => {
          toasts.push("error", `Moved to the composer, but couldn't remove it from the queue: ${errorText(err)}`);
        })
        .finally(() => setRowBusy(entryId, false));
    } catch (err) {
      toasts.push("error", `Couldn't move this message to the composer: ${errorText(err)}`);
      setRowBusy(entryId, false);
    }
  }

  function setRowBusy(entryId: string, busy: boolean): void {
    setBusyEntryIds((prev) => {
      const next = new Set(prev);
      if (busy) next.add(entryId);
      else next.delete(entryId);
      return next;
    });
  }

  // Edit on this client's own durable rows (blocked, canceled, waiting): the
  // row's own destination identity governs its controls - a retargeted row
  // surfaced under the displayed ref carries the resumed session as its
  // targetRef, and the identity checks inside edit and remove reject a pane
  // ref that differs from the row's destination. An edit routes through the
  // restore seam so the composer's own gates (an attachment still encoding,
  // the recovery-operation lease) run BEFORE the durable claim, exactly
  // where the retired strip's handler ran them.
  async function handleLocalAction(record: MutationOutboxRecord, action: "edit" | "remove"): Promise<void> {
    const id = record.clientMutationId;
    if (localActions.current.has(id)) return;
    if (action === "edit") {
      requestQueueRowRestore(sessionRef, { text: "", outboxRecord: record, target: null });
      return;
    }
    localActions.current.add(id);
    setRowBusy(id, true);
    try {
      const ref = record.targetRef;
      if (!(await removeQueuedPendingTurn(id, ref))) {
        toasts.push("error", "This message changed elsewhere; reopen it to edit");
      }
      if (ref !== sessionRef) await refreshPendingTurnsProjection(sessionRef);
    } catch (error) {
      toasts.push("error", sessionActionError("Remove failed", error));
    } finally {
      localActions.current.delete(id);
      setRowBusy(id, false);
    }
  }

  async function handleRetry(record: MutationOutboxRecord): Promise<void> {
    setRowBusy(record.clientMutationId, true);
    setRetryErrors((errors) => {
      const next = new Map(errors);
      next.delete(record.clientMutationId);
      return next;
    });
    const reportFailure = (cause: string) =>
      setRetryErrors((errors) => new Map(errors).set(record.clientMutationId, `Retry failed: ${cause}`));
    try {
      const outcome = await retryBlockedPendingTurn(record.clientMutationId, sessionRef);
      if (!outcome.released) {
        const { current } = outcome;
        if (current?.state === "blockedUnknown")
          reportFailure("Delivery still cannot be checked. The original message is kept; you can send a new message.");
        else if (current?.state === "canceled")
          reportFailure("Still canceled by Stop. The original message is kept; press Retry again.");
      }
    } catch (error) {
      reportFailure(errorText(error));
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  async function handleDismiss(record: MutationRecoveryRecord): Promise<void> {
    setRowBusy(record.clientMutationId, true);
    try {
      const discarded = await discardRecoveryPendingTurn(
        record.clientMutationId,
        sessionRef,
        undefined,
        record.compositionRevision ?? 0,
      );
      if (!discarded) toasts.push("error", "This message changed elsewhere; reopen it to edit");
    } catch (error) {
      toasts.push("error", `Couldn't dismiss this row: ${errorText(error)}`);
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  async function handleCopy(record: MutationRecoveryRecord): Promise<void> {
    setRowBusy(record.clientMutationId, true);
    try {
      const { text, skillNames, commandNames } = recordContent(record);
      const content = [text, skillMarkers(skillNames), commandMarkers(commandNames)]
        .filter((part) => part !== "")
        .join("\n");
      await copyToClipboard(content);
      toasts.push("success", "Copied message");
    } catch (error) {
      toasts.push("error", `Couldn't copy message: ${errorText(error)}`);
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  const queuedCount =
    depth + pendingQueueEntries.length + blockedEntries.length + canceledEntries.length + editableEntries.length;
  // One rendering per message: the durable families below own blockedUnknown
  // and canceled rows (their Retry affordance lives there), and this client's
  // editable queue rows own their unattempted entries, so the pending-entry
  // map must exclude exactly those ids or a row appears twice - once as an
  // in-flight entry and once as its durable replacement.
  const durableRowIds = new Set([
    ...blockedEntries.map((record) => record.clientMutationId),
    ...canceledEntries.map((record) => record.clientMutationId),
    ...queueStripIds,
  ]);
  const stackEntries = entries.filter((entry) => !durableRowIds.has(entry.id));
  if (queuedCount === 0 && stackEntries.length === 0 && visibleRecoveryEntries.length === 0) return null;

  // The controls read the model at the press, not at the render: the turn can
  // have ended in between (stores/liveControls.ts).
  const controls = model ? controlsFor(model) : { drain: false, reason: { drain: undefined } };

  return (
    <ul className={styles.stack} data-testid="pending-row-stack" data-queued-count={queuedCount}>
      {stackEntries.map((entry) => {
        const hubRefused = entry.id === refusedId && entry.state === "submitting";
        return (
          <li key={entry.id} data-busy={hubRefused ? "true" : undefined}>
            <UserMessageView
              item={{
                id: entry.id,
                turnId: "",
                type: "userMessage",
                text: pendingEntryPreview(entry) || "[queued messages]",
              }}
              opensExchange={false}
              provisional={heldCaption(entry, turnActive, now, hubRefused)}
            />
          </li>
        );
      })}
      {Array.from({ length: rowCount }, (_, index) => {
        const entryId = ids?.[index];
        const fullText = texts?.[index];
        const entrySkillNames = queue?.skillNames?.[index];
        const entryCommandNames = queue?.commandNames?.[index];
        const entryMentions = queue?.mentions?.[index]?.flatMap((item): ComposerMention[] =>
          item.kind === "skill" || item.kind === "command" ? [{ ...item, kind: item.kind }] : [],
        );
        const entryPreview = truncateForDisplay(preview?.[index] ?? fullText ?? "");
        // The optimistic twin owns the row while this client's durable
        // record reconciles against the daemon's queue.
        if (pendingTexts.has(entryPreview) && entryId === undefined) return null;
        const busy = entryId !== undefined && busyEntryIds.has(entryId);
        const actionsAvailable = hasIds && entryId !== undefined;
        const imageOnly =
          hasTexts &&
          (fullText ?? "").trim() === "" &&
          (entrySkillNames?.length ?? 0) === 0 &&
          (entryCommandNames?.length ?? 0) === 0;
        const actions = actionsAvailable ? (
          <>
            <IconButton
              label="Steer now"
              icon={<span aria-hidden="true">⇧</span>}
              size="sm"
              disabled={!controls.drain || busy}
              onClick={() => {
                if (entryId !== undefined) {
                  const rowText = fullText ?? "";
                  const displayText = rowText.trim() !== "" ? rowText : entryPreview;
                  void handlePromote(index, entryId, displayText);
                }
              }}
            />
            <IconButton
              label="Edit message"
              icon={<span aria-hidden="true">✎</span>}
              size="sm"
              disabled={!hasTexts || imageOnly || busy}
              onClick={() => {
                if (entryId !== undefined && fullText !== undefined) {
                  handleEdit(index, entryId, fullText, entrySkillNames, entryCommandNames, entryMentions);
                }
              }}
            />
            <IconButton
              label="Remove from queue"
              icon={<span aria-hidden="true">✕</span>}
              variant="danger"
              size="sm"
              disabled={busy}
              onClick={() => {
                if (entryId !== undefined) void handleCancel(index, entryId);
              }}
            />
          </>
        ) : undefined;
        return (
          <li key={entryId ?? index} data-kind="queued">
            <UserMessageView
              item={{
                id: entryId ?? `queue_${index}`,
                turnId: "",
                type: "userMessage",
                text: [entryPreview, skillMarkers(entrySkillNames ?? []), commandMarkers(entryCommandNames ?? [])]
                  .filter((part) => part !== "")
                  .join(" "),
              }}
              opensExchange={false}
              provisional="Queued for the next turn"
              actions={actions}
            />
          </li>
        );
      })}
      {editableEntries.map((record) => {
        // The waiting family: this client's unattempted editable rows - the
        // same durable-local slot the retired strip's waiting kind held.
        const rowBusy = busyEntryIds.has(record.clientMutationId);
        const hubRefused = record.clientMutationId === refusedId;
        const actions = (
          <>
            {canEditQueuedMessage(record) && (
              <IconButton
                label="Edit message"
                icon={<span aria-hidden="true">✎</span>}
                size="sm"
                disabled={rowBusy}
                onClick={() => void handleLocalAction(record, "edit")}
              />
            )}
            {canRemoveQueuedMessage(record) && (
              <IconButton
                label="Remove from queue"
                icon={<span aria-hidden="true">✕</span>}
                size="sm"
                variant="danger"
                disabled={rowBusy}
                onClick={() => void handleLocalAction(record, "remove")}
              />
            )}
          </>
        );
        return (
          <li key={record.clientMutationId} data-kind="waiting" data-busy={hubRefused ? "true" : undefined}>
            <UserMessageView
              item={{
                id: record.clientMutationId,
                turnId: "",
                type: "userMessage",
                text: recordPreview(record) || "[queued messages]",
              }}
              opensExchange={false}
              provisional={hubRefused ? "Waiting for the hub" : "Waiting to send"}
              actions={actions}
            />
          </li>
        );
      })}
      {[
        ...blockedEntries.map((record) => ({ kind: "blocked" as const, record })),
        ...canceledEntries.map((record) => ({ kind: "canceled" as const, record })),
      ].map(({ kind, record }) => {
        const rowBusy = busyEntryIds.has(record.clientMutationId);
        const retryError = retryErrors.get(record.clientMutationId);
        const actions = (
          <>
            {canEditQueuedMessage(record) && (
              <IconButton
                label="Edit message"
                icon={<span aria-hidden="true">✎</span>}
                size="sm"
                disabled={rowBusy}
                onClick={() => void handleLocalAction(record, "edit")}
              />
            )}
            {canRemoveQueuedMessage(record) && (
              <IconButton
                label="Remove from queue"
                icon={<span aria-hidden="true">✕</span>}
                size="sm"
                variant="danger"
                disabled={rowBusy}
                onClick={() => void handleLocalAction(record, "remove")}
              />
            )}
            <Button
              size="sm"
              variant="quiet"
              disabled={
                rowBusy ||
                // Retry is offered only when retryBlockedMutation can actually
                // act, and it acts on the ROW's own target - which for a
                // retargeted row is not this pane's ref.
                !retryBlockedForTarget(record.targetRef)
              }
              onClick={() => void handleRetry(record)}
            >
              {rowBusy ? "Retrying…" : "Retry"}
            </Button>
          </>
        );
        const label = kind === "canceled" ? "Canceled by Stop" : "Delivery uncertain";
        return (
          <li key={record.clientMutationId} data-kind={kind}>
            <UserMessageView
              item={{
                id: record.clientMutationId,
                turnId: "",
                type: "userMessage",
                text: recordPreview(record),
              }}
              opensExchange={false}
              provisional={retryError !== undefined ? `${label} · ${retryError}` : label}
              actions={actions}
            />
          </li>
        );
      })}
      {visibleRecoveryEntries.map((record) => {
        const rowBusy = busyEntryIds.has(record.clientMutationId);
        const isInterrupt = record.method === "turn/interrupt";
        const reason = record.recoveryReason;
        if (record.recoveryKind === "orphaned") {
          return (
            <li key={record.clientMutationId} data-kind="orphaned">
              <UserMessageView
                item={{ id: record.clientMutationId, turnId: "", type: "userMessage", text: recordPreview(record) }}
                opensExchange={false}
                provisional="Destination deleted"
                actions={
                  <Button size="sm" variant="quiet" disabled={rowBusy} onClick={() => void handleCopy(record)}>
                    Copy
                  </Button>
                }
              />
            </li>
          );
        }
        return (
          <li key={record.clientMutationId} data-kind={isInterrupt ? "refused-stop" : "refused"}>
            <UserMessageView
              item={{
                id: record.clientMutationId,
                turnId: "",
                type: "userMessage",
                // An interrupt is not a message: it carries no input, so say
                // what failed instead of offering to edit a Stop.
                text: isInterrupt ? "Stop didn't reach the session" : recordPreview(record),
              }}
              opensExchange={false}
              provisional={reason ? `Not delivered · ${reason}` : "Not delivered"}
              actions={
                isInterrupt ? (
                  <IconButton
                    label="Dismiss"
                    icon={<span aria-hidden="true">✕</span>}
                    size="sm"
                    disabled={rowBusy}
                    onClick={() => void handleDismiss(record)}
                  />
                ) : (
                  <IconButton
                    label="Edit message"
                    icon={<span aria-hidden="true">✎</span>}
                    size="sm"
                    // A refused control has to say so: the composer's live
                    // busy gate (an in-flight submit/steer/drain) keeps the
                    // press from racing, read through the seam store because
                    // only the composer knows its own busyAction. The
                    // refusal is aria-disabled rather than native, so the
                    // reason stays reachable from the keyboard.
                    disabled={rowBusy || composerBusy}
                    disabledReason={composerBusy ? "Wait for the current message to finish sending" : ""}
                    onClick={() =>
                      requestQueueRowRestore(sessionRef, { text: "", recoveryRecord: record, target: null })
                    }
                  />
                )
              }
            />
          </li>
        );
      })}
    </ul>
  );
}
