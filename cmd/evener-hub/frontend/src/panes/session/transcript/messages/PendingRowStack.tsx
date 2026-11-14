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
  errorText,
  formatElapsed,
  isTurnActive,
  sessionActionError,
} from "@evener/appwire-client";
import {
  canEditQueuedMessage,
  canRemoveQueuedMessage,
  queueEntryAt,
  reflectedMutationIds,
} from "@evener/appwire-client/state/mutation";
import type { JSX } from "react";
import { useContext, useEffect, useMemo, useRef, useState } from "react";
import { copyToClipboard } from "../../../../shell/palette/commands";
import { controlsFor, pressLocalRecoveryFenced, pressRefusal } from "../../../../stores/liveControls";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import {
  threadsStore,
  useEditableQueuedMutationEntries,
  useRetryBlockedForTarget,
  useThreadsStore,
} from "../../../../stores/threads";
import { Button, IconButton, useToasts } from "../../../../widgets";
import { QUEUE_ACTIONS_RECOVERY_FENCED_REASON } from "../../composer/controlTooltips";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import {
  discardRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  removeQueuedPendingTurn,
  retryBlockedPendingTurn,
  useBlockedMessageEntries,
  useCanceledMessageEntries,
  useEditablePendingMutationIds,
  usePendingTurnEntries,
  useRecoveryMessageEntries,
} from "../../composer/queue/pendingTurnsStore";
import {
  commandMarkers,
  outboxRecordPreview,
  pendingEntryPreview,
  skillMarkers,
  truncateForDisplay,
} from "../../composer/queue/queueDisplay";
import {
  requestQueueRowRestore,
  useActiveRecoveryId,
  useComposerBusy,
  waitForQueueRowRestoreConsumed,
} from "../../composer/queueRowInsert";
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
const NO_VISIBLE_ENTRIES: readonly PendingTurnEntry[] = [];

export function useHeldSteerEpoch(ref: string, entries: readonly PendingTurnEntry[], visible: boolean): number {
  const [epoch, setEpoch] = useState(0);
  const prevRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);
  const visibleEntries = visible ? entries : NO_VISIBLE_ENTRIES;
  const ids = useMemo(() => new Set(visibleEntries.map((entry) => entry.id)), [visibleEntries]);
  useEffect(() => {
    const prev = prevRef.current;
    prevRef.current = { ref, ids };
    if (prev?.ref !== ref) return;
    const added = visibleEntries.some((entry) => !prev.ids.has(entry.id));
    if (!added) return;
    setEpoch((count) => count + 1);
  }, [ref, ids, visibleEntries]);
  return epoch;
}

function recordPreview(record: MutationOutboxRecord): string {
  // A durable row is a transcript bubble: the whole message body reads in
  // place, never a 140-character cut that forces Edit to inspect the rest.
  // Previews stay for preview-only records (the daemon's queued preview).
  return pendingEntryPreview(outboxRecordPreview(record));
}

export function PendingRowStack({
  ref: sessionRef,
  paneId,
}: {
  ref: string;
  // The seat the stack mounts under: restore requests address this pane the
  // way quote inserts do, so one ref's two mounted composers (an embedded
  // cascade column) never both consume one request.
  paneId?: string;
}): JSX.Element | null {
  const entries = usePendingTurnEntries(sessionRef);
  const queueStripIds = useEditablePendingMutationIds(sessionRef);
  const blockedEntries = useBlockedMessageEntries(sessionRef);
  const canceledEntries = useCanceledMessageEntries(sessionRef);
  const recoveryEntries = useRecoveryMessageEntries(sessionRef);
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
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  // The ONE reflected-identity rule (the appwire core's own): an id the
  // live model already carries - a delivered transcript item or a daemon
  // queue row - is not a waiting row, or a send that landed would render
  // both as its delivered row and as its durable waiting row.
  const reflectedIds = reflectedMutationIds(model);
  const editableEntries = useEditableQueuedMutationEntries(sessionRef).filter(
    (record) => record.state === "submitting" && !reflectedIds.has(record.clientMutationId),
  );
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
  const pendingQueueEntries = useMemo(
    () =>
      entries.filter(
        (entry) =>
          entry.method === "queue" &&
          entry.state !== "blockedUnknown" &&
          entry.state !== "canceled" &&
          !queueStripIds.has(entry.id),
      ),
    [entries, queueStripIds],
  );
  // One preview per entry, computed once per projection change: the
  // pending-entry rows below render it as their text, and the daemon-row
  // twin check reads its queue-method subset as a matching key - the
  // optimistic twin renders instead of the daemon row (it carries
  // createdAt), so the daemon row and its twin never both render. Edits
  // remove the daemon row first and leave the optimistic pending row for
  // the composition; the pending side owns everything it can be reconciled
  // against.
  const entryPreviews = useMemo(() => {
    const previews = new Map<string, string>();
    for (const entry of entries) previews.set(entry.id, pendingEntryPreview(entry));
    return previews;
  }, [entries]);
  const pendingTexts = useMemo(
    () => new Set(pendingQueueEntries.map((entry) => entryPreviews.get(entry.id) ?? "")),
    [pendingQueueEntries, entryPreviews],
  );

  const turnActive = isTurnActive(model?.status.type ?? "");

  // The recovery fence, re-read live at the press (the same render-vs-press
  // rule every queue press follows): the hub refuses the queue's verbs for
  // the obligation's whole window. Shared by the three daemon-queue action
  // handlers below; guardSteerPress adds the live-controls refusal check
  // the Steer-now press carries.
  const guardQueuePress = (): boolean => {
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", QUEUE_ACTIONS_RECOVERY_FENCED_REASON);
      return true;
    }
    return false;
  };
  const guardSteerPress = (): boolean => {
    if (guardQueuePress()) return true;
    const refusal = pressRefusal(sessionRef, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      return true;
    }
    return false;
  };

  // Steer-now on a daemon queue row: promote that row into the running turn.
  async function handlePromote(
    index: number,
    entryId: string,
    displayText: string,
    skillNames?: readonly string[],
    commandNames?: readonly string[],
  ): Promise<void> {
    if (guardSteerPress()) return;
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().promoteQueuedAsSteer(sessionRef, index, entryId, {
        text: displayText,
        skillNames,
        commandNames,
      });
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't steer with this message now", err));
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleCancel(index: number, entryId: string): Promise<void> {
    if (guardQueuePress()) return;
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't remove this message from the queue", err));
    } finally {
      setRowBusy(entryId, false);
    }
  }

  // Edit a daemon queue row: loser-safe order (the retired strip's own) -
  // the row's full text reaches the composer FIRST (through the restore
  // seam; Composer owns the merge and the caret), then the queued copy is
  // removed. A failure on the remove half leaves the user's text in the
  // composer and an honest toast, never a silently duplicated message. A
  // seat with no mounted composer never consumes the request, so the
  // remove half only runs after the text provably landed somewhere.
  async function handleEdit(
    index: number,
    entryId: string,
    fullText: string,
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void> {
    if (guardQueuePress()) return;
    setRowBusy(entryId, true);
    try {
      requestQueueRowRestore(
        sessionRef,
        mentions
          ? { text: fullText, skillNames, commandNames, mentions, target: paneId ?? null }
          : { text: fullText, skillNames, commandNames, target: paneId ?? null },
      );
      const composerTookIt = await waitForQueueRowRestoreConsumed(sessionRef);
      if (!composerTookIt) {
        // No composer of this ref consumed the request: leave the queued
        // entry where it is rather than removing a message whose text
        // landed nowhere.
        toasts.push("error", "Open the session composer to edit this message");
      } else {
        try {
          await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
        } catch (err) {
          toasts.push("error", `Moved to the composer, but couldn't remove it from the queue: ${errorText(err)}`);
        }
      }
    } finally {
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
      requestQueueRowRestore(sessionRef, { text: "", outboxRecord: record, target: paneId ?? null });
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
      const { text, skillNames, commandNames } = outboxRecordPreview(record);
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

  // The count signal carries what the stack renders: recovery rows the
  // composer's live composition already owns are excluded here exactly as
  // the render excludes them (the retired strip applied the same
  // activeRecoveryId cut at selection time).
  const queuedCount =
    depth +
    pendingQueueEntries.length +
    blockedEntries.length +
    canceledEntries.length +
    editableEntries.length +
    visibleRecoveryEntries.length;
  // One rendering per message: the durable families below own blockedUnknown
  // and canceled rows (their Retry affordance lives there), and this client's
  // editable queue rows own their unattempted entries, so the pending-entry
  // map must exclude exactly those ids or a row appears twice - once as an
  // in-flight entry and once as its durable replacement.
  const durableRowIds = useMemo(
    () =>
      new Set([
        ...blockedEntries.map((record) => record.clientMutationId),
        ...canceledEntries.map((record) => record.clientMutationId),
        ...queueStripIds,
      ]),
    [blockedEntries, canceledEntries, queueStripIds],
  );
  const stackEntries = entries.filter((entry) => !durableRowIds.has(entry.id));
  if (queuedCount === 0 && stackEntries.length === 0 && visibleRecoveryEntries.length === 0) return null;

  // The controls read the model at the press, not at the render: the turn can
  // have ended in between (stores/liveControls.ts).
  const controls = model ? controlsFor(model) : { drain: false, reason: { drain: undefined } };

  return (
    <ul className={styles.stack} data-testid="pending-row-stack" data-queued-count={queuedCount}>
      {stackEntries.map((entry) => {
        const hubRefused = entry.id === refusedId && entry.state === "submitting";
        const entryPreview = entryPreviews.get(entry.id) ?? "";
        return (
          <li key={entry.id} data-busy={hubRefused ? "true" : undefined}>
            <UserMessageView
              item={{
                id: entry.id,
                turnId: "",
                type: "userMessage",
                text: entryPreview || "[queued messages]",
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
        const namedMarkers = [skillMarkers(entrySkillNames ?? []), commandMarkers(entryCommandNames ?? [])]
          .filter(Boolean)
          .join(" ");
        // The daemon's generic "[N skills]" preview doubles the named
        // markers a selection-only row already renders - suppress it so the
        // synthetic placeholder never reads as the message (the retired
        // strip's own rule).
        const genericSelectionPlaceholder =
          (fullText ?? "").trim() === "" &&
          namedMarkers !== "" &&
          /^\[\d*\s*(skills?|commands?)\]$/i.test(entryPreview.trim());
        const previewText = genericSelectionPlaceholder ? "" : entryPreview;
        // The optimistic twin owns the row while this client's durable
        // record reconciles against the daemon's queue.
        if (pendingTexts.has(entryPreview) && entryId === undefined) return null;
        const busy = entryId !== undefined && busyEntryIds.has(entryId);
        const actionsAvailable = hasIds && entryId !== undefined;
        // The restore seam is text-and-selections only: the queue projection
        // carries no image bytes, so an edit's cancel would destroy the row's
        // images permanently. Any image-bearing row refuses Edit for that
        // reason - not just a textless image-only row, which a selection
        // would otherwise make look editable. The core's own queue-row
        // reader carries the plain-text caveat: a literal "[image]" message
        // is not an image row.
        const carriesImages = queue !== null && queueEntryAt(queue, index).imageCount > 0;
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
                  // The ghost's display text: the row's full text, or -
                  // for a blank row - the same stripped preview the row
                  // renders (nothing for a selection-only row, where the
                  // named markers carry the whole content).
                  const rowText = fullText ?? "";
                  const displayText = rowText.trim() !== "" ? rowText : previewText;
                  void handlePromote(index, entryId, displayText, entrySkillNames, entryCommandNames);
                }
              }}
            />
            <IconButton
              label="Edit message"
              icon={<span aria-hidden="true">✎</span>}
              size="sm"
              disabled={!hasTexts || imageOnly || carriesImages || busy}
              disabledReason={
                carriesImages ? "Editing can't take the image back - remove the message and re-attach it instead" : ""
              }
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
                text: [fullText ?? previewText, namedMarkers].filter(Boolean).join(" "),
              }}
              opensExchange={false}
              provisional="Queued for the next turn"
              footer={actions}
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
                // The composer's live busy gate keeps the press from
                // racing an in-flight submit (the retired strip's own
                // gate), read through the seam store.
                disabled={rowBusy || composerBusy}
                disabledReason={composerBusy ? "Wait for the current message to finish sending" : ""}
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
              footer={actions}
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
                disabled={rowBusy || composerBusy}
                disabledReason={composerBusy ? "Wait for the current message to finish sending" : ""}
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
            <RetryButton record={record} busy={rowBusy} onRetry={() => void handleRetry(record)} />
          </>
        );
        const label = kind === "canceled" ? "Canceled by Stop" : "Delivery uncertain";
        // A failed Retry has to announce itself: the provisional caption is
        // an ordinary span, so the error gets its own live node - the
        // retired strip rendered it with role="alert".
        const retryAlert = retryError !== undefined ? <span role="alert">{retryError}</span> : null;
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
              provisional={label}
              footer={
                <>
                  {actions}
                  {retryAlert}
                </>
              }
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
                footer={
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
              footer={
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
                      requestQueueRowRestore(sessionRef, { text: "", recoveryRecord: record, target: paneId ?? null })
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

// A durable failure row's Retry affordance, its own component so the
// retry-verdict hook runs once per ROW target (hooks cannot sit inside the
// map callback above): it subscribes to exactly the inputs the press's own
// retryBlockedForTarget reads, so the row re-renders precisely when its own
// verdict flips.
function RetryButton({
  record,
  busy,
  onRetry,
}: {
  record: MutationOutboxRecord;
  busy: boolean;
  onRetry(): void;
}): JSX.Element {
  // Retry is offered only when retryBlockedMutation can actually act, and
  // it acts on the ROW's own target - which for a retargeted row is not
  // this pane's ref. A blocked target keeps Retry disabled exactly as its
  // press would refuse.
  const retryBlocked = useRetryBlockedForTarget(record.targetRef);
  return (
    <Button size="sm" variant="quiet" disabled={busy || retryBlocked} onClick={onRetry}>
      {busy ? "Retrying…" : "Retry"}
    </Button>
  );
}
