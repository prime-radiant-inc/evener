// QueueStrip: the queue strip UI (parity-m5-composer.md §B) - real queue
// rows from model.queue with promote/edit/cancel actions (expectedEntryId-
// guarded, Conflict-safe per stores/threads.ts), the drain-as-steer
// affordance, and optimistic pending queue rows from this stream's own
// pendingTurnsStore. Self-contained per the wave's integration seam: reads
// only props (documented on QueueStripProps below) plus the threads store
// and pendingTurnsStore - Composer.tsx/Session.tsx are outside this manifest,
// so mounting this inside Composer's own tree happens at the wave
// integration merge (T6), not here.

import type { ComposerMention, InputItem } from "@evener/appwire-client";
import { canonicalSkillNames, errorText, STEER_UNAVAILABLE, sessionActionError } from "@evener/appwire-client";
import { canEditQueuedMessage, canRemoveQueuedMessage } from "@evener/appwire-client/state/mutation";
import { type ReactNode, useRef, useState } from "react";
import { copyToClipboard } from "../../../../shell/palette/commands";
import { controlsFor, pressLocalRecoveryFenced, pressRefusal } from "../../../../stores/liveControls";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { InputAttachment } from "../../../../stores/threads";
import {
  captureRecoveryStopBarrier,
  retryBlockedBySnapshot,
  threadsStore,
  useEditableQueuedMutationEntries,
  useThreadsStore,
} from "../../../../stores/threads";
import { Button, IconButton, Tooltip, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  pushRecoveryRefusal,
  pushSelectionRefusal,
  type RecoveryRefusal,
  selectionRefusal,
  submitDraftToStore,
} from "../submitProtocol";
import {
  discardRecoveryPendingTurn,
  editQueuedPendingTurn,
  type PendingTurnEntry,
  removeQueuedPendingTurn,
  resendRecoveryPendingTurn,
  retryBlockedPendingTurn,
  submitWithPendingTracking,
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "./pendingTurnsStore";
import { commandMarkers, pendingEntryPreview, skillMarkers, truncateForDisplay } from "./queueDisplay";
import styles from "./queuestrip.module.css";

const CLASS = {
  strip: requireClass(styles.strip, "queuestrip.module.css", "strip"),
  header: requireClass(styles.header, "queuestrip.module.css", "header"),
  title: requireClass(styles.title, "queuestrip.module.css", "title"),
  list: requireClass(styles.list, "queuestrip.module.css", "list"),
  row: requireClass(styles.row, "queuestrip.module.css", "row"),
  rowPending: requireClass(styles.rowPending, "queuestrip.module.css", "rowPending"),
  rowText: requireClass(styles.rowText, "queuestrip.module.css", "rowText"),
  rowReason: requireClass(styles.rowReason, "queuestrip.module.css", "rowReason"),
  rowActions: requireClass(styles.rowActions, "queuestrip.module.css", "rowActions"),
};

// Session.tsx's own loadOlder catch is this wave's reference implementation
// for the failure-feedback convention (T1) - this local helper matches it
// verbatim, per that file's own established per-file-duplication style
// (sandboxEscalation.tsx carries an identical copy rather than a shared
// util).
export interface QueueStripProps {
  ref: string;
  // The composer pane's durable seat. The strip's drain submits as this
  // pane, so its sends - and any row a refusal bounces from them - carry the
  // seat the composer's own sends carry.
  compositionOwnerId?: string;
  // Returns the composer's CURRENT text/attachments/hasPending at the moment
  // the drain affordance is used - drainAsSteer atomically appends this to
  // the queue before draining the whole thing into the active turn as one
  // steering message (see stores/threads.ts's own drainAsSteer doc comment
  // for why this is a getter, not a cached value). `hasPending` mirrors the
  // composer's own attachments.hasPending (true while an attachment is still
  // mid-encode) - handleDrain below blocks on it with the SAME "still
  // processing" toast the composer's own submit paths already use, so a
  // drain can no longer silently omit a not-yet-encoded image from the
  // drained payload (w5-integration-wiring-report.md Concern #3).
  getComposerText(): {
    text: string;
    attachments?: InputAttachment[];
    hasPending: boolean;
    skillNames?: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly ComposerMention[];
  };
  // Restores a queued entry's full text into the composer - called BEFORE
  // cancelQueued on an edit (loser-safe order: a contract row - the text
  // must land even if the cancel that follows fails). `attachments` is
  // never supplied by this module's own call sites: cancelQueued's wire
  // response (TurnCancelQueuedResponse) returns only a removedImages COUNT,
  // never attachment bytes, so an edited entry's images can never be
  // reconstructed here (matches parity: edit is a text-only recompose, and
  // dropped images are surfaced as their own warning toast below). The
  // parameter is kept for signature symmetry with a general "restore to
  // composer" seam the integration may reuse for other callers.
  // `skillNames` carries the entry's canonical skill selections from the
  // queue projection (QueueState.skillNames) so an edit restores its chips
  // too - a queued {type:"skill"} item is otherwise unrecoverable.
  onRestoreToComposer(
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): void;
  activeRecoveryId?: string;
  activeRecoveryRevision?: number;
  // Drain waits for the composer's pending recovery autosaves and reads the
  // committed revision here, so a delayed autosave cannot turn the drain's
  // own resend into a stale-owner refusal.
  getRecoveryRevision?(): Promise<number | undefined>;
  onEditRecovery?(record: MutationRecoveryRecord): void | Promise<void>;
  tryAcquireRecoveryOperation?(): (() => void) | null;
  // Called once a drain-as-steer intent commits to IndexedDB, so the
  // integration can clear the unchanged composer text/attachment snapshot.
  onDrainSuccess(): void;
  // True while EITHER surface has an in-flight submit/steer/queue/drain -
  // the shared busy gate (Composer.tsx's own busyAction, extended with a
  // "drain" value this stream's own onDrainBusyChange sets). Disables the
  // Steer-now button below the same way Composer's own busyAction disables
  // its classic Send/Steer/Stop controls, closing the race where both
  // surfaces' drain-capable buttons could fire concurrently - both
  // ultimately enqueue the SAME drainAsSteer mutation (w5-integration-wiring-
  // report.md's "two Steer buttons" concern).
  busy: boolean;
  // Reports this component's OWN drain busy transitions upward so Composer
  // can fold them into its shared busyAction gate - called with true right
  // before the durable enqueue starts and false once it commits or fails,
  // replacing what used to be a local, Composer-invisible `draining` state.
  onDrainBusyChange(busy: boolean): void;
}

const ACTIONS_UNAVAILABLE_REASON = "Queue actions aren't available for this session";

// The recovery-fenced reading of the same refusal: the hub rejects every queue
// action (turn/promoteQueuedAsSteer, turn/drainAsSteer, turn/cancelQueued) for
// a fenced session until the explicit Resume clears it, so the press names
// that path rather than a generic unavailability.
const RECOVERY_ACTIONS_UNAVAILABLE_REASON = "Queue actions aren't available until this session is resumed";

function recordContent(record: MutationOutboxRecord): {
  text: string;
  imageCount: number;
  skillNames: string[];
  commandNames: string[];
} {
  // A promoted row's composed content lives in its optimisticDisplay.input -
  // its wire params carry only the queue position - so this reading prefers
  // the display input and falls back to the payload input every other
  // method populates (the same precedence pendingEntries' outboxInput gives
  // pending rows).
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
  const skillNames = canonicalSkillNames(
    input
      .filter((item): item is InputItem & { name: string } => item.type === "skill" && typeof item.name === "string")
      .map((item) => item.name),
  );
  return {
    text,
    imageCount: input.filter((item) => item.type === "image").length,
    skillNames,
    commandNames: canonicalSkillNames(input.filter((item) => item.type === "command").map((item) => item.name ?? "")),
  };
}

function recordPreview(record: MutationOutboxRecord): string {
  return truncateForDisplay(pendingEntryPreview(recordContent(record)));
}

// A pending entry already carries its canonical skill names, so it renders the
// same text-plus-markers preview a durable record does. Without the markers an
// entry with no text and no images (a skill-only submission) would be blank
// until the authoritative queue row replaces it.
function pendingPreview(entry: PendingTurnEntry): string {
  return pendingEntryPreview(entry);
}

function editDisabledReason(opts: {
  actionsAvailable: boolean;
  hasTexts: boolean;
  imageOnly: boolean;
}): string | undefined {
  if (!opts.actionsAvailable) return ACTIONS_UNAVAILABLE_REASON;
  if (!opts.hasTexts) return "Editing isn't available for this session";
  if (opts.imageOnly) return "Can't edit an image-only message - remove it and re-attach the image instead";
  return undefined;
}

export function QueueStrip({
  ref: sessionRef,
  compositionOwnerId,
  getComposerText,
  onRestoreToComposer,
  activeRecoveryId,
  activeRecoveryRevision,
  getRecoveryRevision,
  onEditRecovery,
  tryAcquireRecoveryOperation,
  onDrainSuccess,
  busy,
  onDrainBusyChange,
}: QueueStripProps): ReactNode {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const mutationAuthority = useThreadsStore((s) => s.mutationAuthorityRefs.has(sessionRef));
  const recoveryObligated = useThreadsStore((s) => s.restartBlockingObligations.has(sessionRef));
  const reconciliationStorageBlocked = useThreadsStore((s) => s.mutationReconciliationStorageBlocked.has(sessionRef));
  const refusedId = useThreadsStore((s) => s.transientRefusalPaces.get(sessionRef)?.clientMutationId);
  const editableEntries = useEditableQueuedMutationEntries(sessionRef);
  const editableIds = new Set(editableEntries.map((record) => record.clientMutationId));
  const pendingQueueEntries = usePendingTurnEntries(sessionRef, "queue").filter(
    // A queue-method row Stop canceled (or one whose delivery turned unknown)
    // is a durable row below, not a bare pending one - the same slot rule the
    // blocked state already follows.
    (entry) => entry.state !== "blockedUnknown" && entry.state !== "canceled" && !editableIds.has(entry.id),
  );
  const recoveryEntries = useRecoveryEntries(sessionRef).filter(
    (record) => record.method !== "notes/human/set" && record.clientMutationId !== activeRecoveryId,
  );
  // Canceled note saves are owned by the note editor (humanNoteDrafts reports
  // "Note save was canceled by Stop"), the same way blocked ones are.
  const blockedEntries = useBlockedMutationEntries(sessionRef).filter((record) => record.method !== "notes/human/set");
  const canceledEntries = useCanceledMutationEntries(sessionRef).filter(
    (record) => record.method !== "notes/human/set",
  );
  const durableEntries = [
    ...recoveryEntries.map((record) => ({ kind: "recovery" as const, record })),
    ...blockedEntries.map((record) => ({ kind: "blocked" as const, record })),
    ...canceledEntries.map((record) => ({ kind: "canceled" as const, record })),
    ...editableEntries
      .filter((record) => record.state !== "canceled")
      .map((record) => ({ kind: "waiting" as const, record })),
  ].sort((left, right) => left.record.intentSequence - right.record.intentSequence);
  const toasts = useToasts();
  // Keyed by daemon-minted entryId (stable across a re-render even as
  // indices shift), not row index - mirrors the legacy renderer's own
  // setQueuedRowActionsDisabled keying.
  const [busyEntryIds, setBusyEntryIds] = useState<ReadonlySet<string>>(new Set());
  const localActions = useRef(new Set<string>());
  const [retryErrors, setRetryErrors] = useState<ReadonlyMap<string, string>>(new Map());

  const queue = model?.queue ?? null;
  const depth = queue?.depth ?? 0;
  const hasQueuedWork = depth > 0 || pendingQueueEntries.length > 0;
  const visible = hasQueuedWork || durableEntries.length > 0;

  if (!model || !visible) return null;

  // Drain and promote read the session's controls (submitRouting.ts
  // sessionControls: harness steer, and a running turn or a queue a Stop parked).
  const controls = controlsFor(model);

  const ids = queue?.ids;
  const texts = queue?.texts;
  const preview = queue?.preview;
  const rowCount = preview?.length ?? texts?.length ?? ids?.length ?? 0;
  const hasIds = ids !== undefined;
  const hasTexts = texts !== undefined;

  function setRowBusy(entryId: string, busy: boolean): void {
    setBusyEntryIds((prev) => {
      const next = new Set(prev);
      if (busy) next.add(entryId);
      else next.delete(entryId);
      return next;
    });
  }

  async function handleLocalAction(record: MutationOutboxRecord, action: "edit" | "remove"): Promise<void> {
    const id = record.clientMutationId;
    if (localActions.current.has(id)) return;
    const releaseRecoveryOperation = action === "edit" ? tryAcquireRecoveryOperation?.() : null;
    if (action === "edit" && tryAcquireRecoveryOperation && !releaseRecoveryOperation) return;
    if (action === "edit" && getComposerText().hasPending) {
      toasts.push("error", "Image attachment is still processing");
      releaseRecoveryOperation?.();
      return;
    }
    localActions.current.add(id);
    setRowBusy(id, true);
    try {
      if (action === "edit") {
        const recovery = await editQueuedPendingTurn(id, sessionRef);
        if (recovery) await onEditRecovery?.(recovery);
        else pushRecoveryRefusal("changed");
      } else if (!(await removeQueuedPendingTurn(id, sessionRef))) {
        pushRecoveryRefusal("changed");
      }
    } catch (error) {
      toasts.push("error", sessionActionError(action === "edit" ? "Edit failed" : "Remove failed", error));
    } finally {
      localActions.current.delete(id);
      setRowBusy(id, false);
      releaseRecoveryOperation?.();
    }
  }

  async function handlePromote(
    index: number,
    entryId: string,
    displayText: string,
    skillNames?: readonly string[],
    commandNames?: readonly string[],
  ): Promise<void> {
    // The recovery fence, re-read live at the press (the same render-vs-press
    // rule as pressRefusal below): the hub refuses turn/promoteQueuedAsSteer
    // for the obligation's whole window, so an offered press could only mint
    // durable intent that parks until the explicit Resume action clears it.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    // Judged on the store's live controls at the press, not the render's
    // (stores/liveControls.ts): the turn can have ended in between.
    const refusal = pressRefusal(sessionRef, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      return;
    }
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().promoteQueuedAsSteer(sessionRef, index, entryId, {
        text: displayText,
        skillNames,
        commandNames,
      });
      // Success is entirely rendered by the daemon's own thread/queueChanged
      // (row removed) + evener/steering/injected (transcript shows it) - no
      // local mirror, per parity §B.
    } catch (err) {
      // Names the act the button names. The row's control reads "Steer now"
      // (kata mw5w), so a failure that reports "couldn't send" would describe
      // an action the reader never took.
      toasts.push("error", sessionActionError("Couldn't steer with this message now", err));
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleCancel(index: number, entryId: string): Promise<void> {
    // The recovery fence, re-read live at the press: the hub refuses
    // turn/cancelQueued for the obligation's whole window, so an offered press
    // could only mint durable intent that parks until the explicit Resume
    // action clears it.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
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

  async function handleEdit(
    index: number,
    entryId: string,
    fullText: string,
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void> {
    // The recovery fence, re-read live at the press. An edit refuses as a
    // whole: restoring the text without the cancelQueued half would leave the
    // row and the composer carrying the same message, and the cancel alone
    // could only park.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    setRowBusy(entryId, true);
    try {
      // FIRST - loser-safe: the user's text is safely in the composer
      // regardless of whether the cancel below succeeds (contract row).
      // A failure HERE is the other order entirely - nothing moved - so it
      // must not borrow the cancel's message below, and must leave the queued
      // entry alone rather than removing a message with nowhere to go.
      try {
        if (mentions) onRestoreToComposer(fullText, undefined, skillNames, commandNames, mentions);
        else onRestoreToComposer(fullText, undefined, skillNames, ...(commandNames ? [commandNames] : []));
      } catch (err) {
        toasts.push("error", `Couldn't move this message to the composer: ${errorText(err)}`);
        return;
      }
      await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
    } catch (err) {
      toasts.push("error", `Moved to the composer, but couldn't remove it from the queue: ${errorText(err)}`);
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleDrain(): Promise<void> {
    const releaseRecoveryOperation = tryAcquireRecoveryOperation?.();
    if (tryAcquireRecoveryOperation && !releaseRecoveryOperation) return;
    // The recovery fence, re-read live at the press: the hub refuses
    // turn/drainAsSteer for the obligation's whole window, so an offered press
    // could only mint durable intent that parks until the explicit Resume
    // action clears it. Ahead of the busy report so a refusal never churns
    // the shared busy state.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      releaseRecoveryOperation?.();
      return;
    }
    const refusal = pressRefusal(sessionRef, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      releaseRecoveryOperation?.();
      return;
    }
    const { text, attachments, hasPending, skillNames, commandNames, mentions } = getComposerText();
    if (hasPending) {
      toasts.push("error", "Image attachment is still processing");
      releaseRecoveryOperation?.();
      return;
    }
    const selectionGate = selectionRefusal({ skillNames, commandNames }, model?.capabilities);
    if (selectionGate) {
      pushSelectionRefusal(selectionGate);
      releaseRecoveryOperation?.();
      return;
    }
    let recoveryRefusal: RecoveryRefusal | undefined;
    const recoveryResendRefusal = new Error("Recovery resend was refused");
    // Capture the Stop fence before any await: a Stop during the autosave
    // wait must still land between this submission and its mutation.
    const recoveryBarrier = activeRecoveryId ? captureRecoveryStopBarrier(sessionRef) : undefined;
    onDrainBusyChange(true);
    try {
      let expectedRecoveryRevision = activeRecoveryRevision;
      if (activeRecoveryId && getRecoveryRevision) {
        expectedRecoveryRevision = await getRecoveryRevision();
        if (expectedRecoveryRevision === undefined) {
          pushRecoveryRefusal("changed");
          return;
        }
      }
      await submitWithPendingTracking(
        {
          ref: sessionRef,
          recoveryId: activeRecoveryId,
          text,
          attachments,
          skillNames,
          commandNames,
          onFailure: (err) => {
            if (err === recoveryResendRefusal) return;
            toasts.push("error", sessionActionError("Drain failed", err));
          },
        },
        async () => {
          if (activeRecoveryId) {
            const outcome = await resendRecoveryPendingTurn(
              activeRecoveryId,
              sessionRef,
              "drain",
              text,
              attachments ?? [],
              skillNames,
              commandNames,
              mentions,
              recoveryBarrier,
              activeRecoveryId && expectedRecoveryRevision !== undefined
                ? { [activeRecoveryId]: expectedRecoveryRevision }
                : undefined,
              compositionOwnerId,
            );
            if (outcome.status !== "sent") {
              recoveryRefusal = outcome.status === "refused" ? "changed" : "consumed";
              throw recoveryResendRefusal;
            }
            return;
          }
          return submitDraftToStore(
            "drain",
            sessionRef,
            { text, attachments, skillNames, commandNames, mentions },
            compositionOwnerId,
          );
        },
      );
      onDrainSuccess();
    } catch {
      // Already reported via onFailure above; swallow so the rejection
      // doesn't reach React as an unhandled promise rejection from this
      // fire-and-forget click handler.
      if (recoveryRefusal) pushRecoveryRefusal(recoveryRefusal);
    } finally {
      onDrainBusyChange(false);
      releaseRecoveryOperation?.();
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
        // retryBlockedPendingTurn read the record after its own refresh, so
        // this decides on the state that refresh observed (issue #1722 - a
        // second refresh here read the same durable rows and fed nothing).
        const { current } = outcome;
        if (current?.state === "blockedUnknown")
          reportFailure("Delivery still cannot be checked. The original message is kept; you can send a new message.");
        else if (current?.state === "canceled")
          // The refused-press counterpart for a canceled row: its release is
          // the FIRST durable step of the retry, so a refusal leaves the row
          // exactly as it was. The row itself still says what it is; this says
          // the press did nothing (kata 2f41's rule that a refused control
          // has to say so).
          reportFailure("Still canceled by Stop. The original message is kept; press Retry again.");
      }
    } catch (error) {
      reportFailure(errorText(error));
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  // discardRecoveryPendingTurn, not the bare store mutation: it refreshes the
  // projection unconditionally, so a record another surface already discarded
  // still leaves the strip instead of sitting there being counted as queued.
  async function handleDismiss(record: MutationRecoveryRecord): Promise<void> {
    setRowBusy(record.clientMutationId, true);
    try {
      const discarded = await discardRecoveryPendingTurn(
        record.clientMutationId,
        sessionRef,
        undefined,
        record.compositionRevision ?? 0,
      );
      if (!discarded) pushRecoveryRefusal("changed");
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

  const queuedCount = depth + pendingQueueEntries.length + durableEntries.length;
  return (
    <section className={CLASS.strip} data-testid="queue-strip" data-queued-count={queuedCount}>
      <div className={CLASS.header}>
        <h3 className={CLASS.title}>Queued messages ({queuedCount})</h3>
        {hasQueuedWork && controls.drain && (
          <Tooltip label="Send your message and everything queued into the current turn">
            <Button variant="quiet" size="sm" onClick={() => void handleDrain()} disabled={busy}>
              Steer queue now
            </Button>
          </Tooltip>
        )}
      </div>
      <ul className={CLASS.list}>
        {Array.from({ length: rowCount }, (_, index) => {
          const entryId = ids?.[index];
          const fullText = texts?.[index];
          const entrySkillNames = queue?.skillNames?.[index];
          // Named markers replace synthetic selection-only previews. Full user
          // text, including literal "[skill]" or "[command]", stays visible.
          // Truncate the preview first so it cannot push the markers past the cap.
          const entryCommandNames = queue?.commandNames?.[index];
          const entryMentions = queue?.mentions?.[index]?.flatMap((item): ComposerMention[] =>
            item.kind === "skill" || item.kind === "command" ? [{ ...item, kind: item.kind }] : [],
          );
          const namedMarkers = [skillMarkers(entrySkillNames ?? []), commandMarkers(entryCommandNames ?? [])]
            .filter(Boolean)
            .join(" ");
          const entryPreview = truncateForDisplay(preview?.[index] ?? fullText ?? "");
          const genericSelectionPlaceholder =
            (fullText ?? "").trim() === "" &&
            namedMarkers !== "" &&
            /^\[\d*\s*(skills?|commands?)\]$/i.test(entryPreview.trim());
          const previewText = genericSelectionPlaceholder ? "" : entryPreview;
          const displayText = [previewText, namedMarkers].filter((part) => part !== "").join(" ");
          const busy = entryId !== undefined && busyEntryIds.has(entryId);
          const actionsAvailable = hasIds && entryId !== undefined;
          // A blank-text entry is uneditable only when it carries nothing
          // else restorable - a selection-only entry's chips ARE the content.
          const imageOnly =
            hasTexts &&
            (fullText ?? "").trim() === "" &&
            (entrySkillNames?.length ?? 0) === 0 &&
            (entryCommandNames?.length ?? 0) === 0;
          const editAvailable = actionsAvailable && hasTexts && !imageOnly;

          return (
            // key: entryId is the real, stable identity whenever the daemon
            // reports one; index is only a last-resort fallback for a
            // degraded/old daemon that reports no ids array at all, where no
            // better identity exists.
            <li key={entryId ?? index} className={CLASS.row} data-kind="queued">
              <span className={CLASS.rowText}>{displayText}</span>
              <div className={CLASS.rowActions}>
                <IconButton
                  label="Steer now"
                  icon={<span aria-hidden="true">⇧</span>}
                  size="sm"
                  disabled={!actionsAvailable || !controls.drain || busy}
                  disabledReason={
                    !actionsAvailable
                      ? ACTIONS_UNAVAILABLE_REASON
                      : controls.drain
                        ? ""
                        : (controls.reason.drain ?? STEER_UNAVAILABLE)
                  }
                  onClick={() => {
                    if (entryId !== undefined) {
                      // The ghost's display text: the row's full text, or - for a
                      // blank row - the same stripped preview the row renders
                      // above: the image placeholder for an image-only row (its
                      // whole content), and nothing for a selection-only row, where
                      // the named markers carry the whole content and the raw
                      // generic placeholder would only double it.
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
                  disabled={!editAvailable || busy}
                  disabledReason={editDisabledReason({ actionsAvailable, hasTexts, imageOnly }) ?? ""}
                  onClick={() => {
                    if (entryId !== undefined && fullText !== undefined) {
                      void handleEdit(index, entryId, fullText, entrySkillNames, entryCommandNames, entryMentions);
                    }
                  }}
                />
                <IconButton
                  label="Remove from queue"
                  icon={<span aria-hidden="true">✕</span>}
                  variant="danger"
                  size="sm"
                  disabled={!actionsAvailable || busy}
                  disabledReason={actionsAvailable ? "" : ACTIONS_UNAVAILABLE_REASON}
                  onClick={() => {
                    if (entryId !== undefined) void handleCancel(index, entryId);
                  }}
                />
              </div>
            </li>
          );
        })}
        {pendingQueueEntries.map((entry) => {
          const hubRefused = entry.id === refusedId && entry.state === "submitting";
          return (
            <li
              key={entry.id}
              className={`${CLASS.row} ${CLASS.rowPending}`}
              data-kind="pending"
              data-busy={hubRefused ? "true" : undefined}
            >
              <span className={CLASS.rowText}>
                {hubRefused ? (
                  <span className={CLASS.rowReason} data-busy-reason="">
                    Retrying —{" "}
                  </span>
                ) : null}
                {pendingPreview(entry)}
              </span>
            </li>
          );
        })}
        {durableEntries.map(({ kind, record }) => {
          const rowBusy = busyEntryIds.has(record.clientMutationId);
          const retryError = retryErrors.get(record.clientMutationId);
          const hubRefused = kind === "waiting" && record.clientMutationId === refusedId;
          if (kind === "blocked" || kind === "canceled" || kind === "waiting") {
            // Delivery-uncertain rows and Stop-canceled rows share one slot
            // (stop-cancellation-outbox §6 Display): both are this client's
            // own undelivered submissions sitting in durable storage, and
            // they differ only in the label - uncertain delivery versus a
            // cancellation the user's own click wrote. The Retry affordance,
            // its gating, and the error slot are identical.
            return (
              <li
                key={record.clientMutationId}
                className={CLASS.row}
                data-kind={kind}
                data-busy={hubRefused ? "true" : undefined}
              >
                <span className={CLASS.rowText}>
                  {hubRefused && (
                    <span className={CLASS.rowReason} data-busy-reason="">
                      Retrying —{" "}
                    </span>
                  )}
                  <span>
                    {kind === "canceled"
                      ? "Canceled by Stop"
                      : kind === "waiting"
                        ? "Waiting to send"
                        : "Delivery uncertain"}
                  </span>
                  {" — "}
                  <span>{recordPreview(record)}</span>
                  {retryError !== undefined && <span role="alert">{retryError}</span>}
                </span>
                <div className={CLASS.rowActions}>
                  {canEditQueuedMessage(record) && (
                    <IconButton
                      label="Edit message"
                      icon={<span aria-hidden="true">✎</span>}
                      size="sm"
                      disabled={rowBusy || busy || onEditRecovery === undefined}
                      onClick={() => void handleLocalAction(record, "edit")}
                    />
                  )}
                  {canRemoveQueuedMessage(record) && (
                    <IconButton
                      label="Remove from queue"
                      icon={<span aria-hidden="true">✕</span>}
                      size="sm"
                      variant="danger"
                      disabled={rowBusy || busy}
                      onClick={() => void handleLocalAction(record, "remove")}
                    />
                  )}
                  {kind !== "waiting" && (
                    <Button
                      size="sm"
                      variant="quiet"
                      disabled={
                        rowBusy ||
                        // Retry is offered only when retryBlockedMutation can
                        // actually act. Its snapshot-level refusals - every
                        // notLoaded or restartRequired snapshot, any ref without
                        // mutation authority, and a restart-blocking obligation
                        // (a Stop, or a snapshot the daemon reports as
                        // restartRequired/resumeRequired) - are shared with it as
                        // retryBlockedBySnapshot (stores/threads.ts), so a
                        // recovery-fenced local session keeps Retry disabled until
                        // the explicit Resume action restores it; without the
                        // obligation term an idle fenced row would offer a Retry
                        // that always fails.
                        retryBlockedBySnapshot(model?.status.type, mutationAuthority, recoveryObligated) ||
                        // A storage-blocked ref refuses the press too - but its
                        // press would first hang on the wedged storage read
                        // before failing, so mirror the refusal here and let the
                        // button return when discovery's reconcile retry clears
                        // the state.
                        reconciliationStorageBlocked
                      }
                      onClick={() => void handleRetry(record)}
                    >
                      {rowBusy ? "Retrying…" : "Retry"}
                    </Button>
                  )}
                </div>
              </li>
            );
          }
          if (record.recoveryKind === "orphaned") {
            return (
              <li key={record.clientMutationId} className={CLASS.row} data-kind="orphaned">
                <span className={CLASS.rowText}>
                  <span>Destination deleted</span>
                  {" — "}
                  <span>{recordPreview(record)}</span>
                </span>
                <div className={CLASS.rowActions}>
                  <Button size="sm" variant="quiet" disabled={rowBusy} onClick={() => void handleCopy(record)}>
                    Copy
                  </Button>
                </div>
              </li>
            );
          }
          // A refused control has to say so. Without the reason this row is
          // indistinguishable from a real queued message -- the header even
          // counts it as queued -- which is how a rejected Steer or Stop left
          // nothing on screen at all (kata 2f41).
          //
          // An interrupt is not a message: it carries no input, so its preview
          // is empty and "Edit message" would offer to resend a Stop as
          // whatever the user then types. Say what failed instead.
          const isInterrupt = record.method === "turn/interrupt";
          const reason = record.recoveryReason;
          return (
            <li
              key={record.clientMutationId}
              className={CLASS.row}
              data-kind={isInterrupt ? "refused-stop" : record.recoveryKind === "editing" ? "editing" : "refused"}
            >
              <span className={CLASS.rowText}>
                {isInterrupt ? "Stop didn't reach the session" : recordPreview(record)}
                {reason ? <span className={CLASS.rowReason}> — {reason}</span> : null}
              </span>
              <div className={CLASS.rowActions}>
                {isInterrupt ? (
                  // A Stop has nothing to edit and nothing to resend, but it
                  // still needs a way off the strip. Without one its recovery
                  // record is permanent and keeps being counted as queued, so
                  // every failed Stop leaves a scar the user cannot clear.
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
                    disabled={rowBusy || busy || onEditRecovery === undefined}
                    disabledReason={
                      onEditRecovery === undefined
                        ? "Open the session Composer to edit this message"
                        : busy
                          ? "Wait for the current message to finish sending"
                          : ""
                    }
                    onClick={() => {
                      const releaseRecoveryOperation = tryAcquireRecoveryOperation?.();
                      if (tryAcquireRecoveryOperation && !releaseRecoveryOperation) return;
                      void Promise.resolve(onEditRecovery?.(record)).finally(() => releaseRecoveryOperation?.());
                    }}
                  />
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
