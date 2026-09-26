// QueueStrip: the queue strip UI (parity-m5-composer.md §B) - real queue
// rows from model.queue with promote/edit/cancel actions (expectedEntryId-
// guarded, Conflict-safe per stores/threads.ts), the drain-as-steer
// affordance, and optimistic pending queue rows from this stream's own
// pendingTurnsStore. Self-contained per the wave's integration seam: reads
// only props (documented on QueueStripProps below) plus the threads store
// and pendingTurnsStore - Composer.tsx/Session.tsx are outside this manifest,
// so mounting this inside Composer's own tree happens at the wave
// integration merge (T6), not here.

import type { InputItem } from "@evener/appwire-client";
import { canonicalSkillNames, errorText, STEER_UNAVAILABLE, sessionActionError } from "@evener/appwire-client";
import { type ReactNode, useState } from "react";
import { copyToClipboard } from "../../../../shell/palette/commands";
import { controlsFor, pressLocalRecoveryFenced, pressRefusal } from "../../../../stores/liveControls";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { InputAttachment } from "../../../../stores/threads";
import {
  readMutationPersistence,
  retryBlockedBySnapshot,
  threadsStore,
  useThreadsStore,
} from "../../../../stores/threads";
import { Button, IconButton, type IconButtonProps, Tooltip, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  discardRecoveryPendingTurn,
  type PendingTurnEntry,
  resendRecoveryPendingTurn,
  retryBlockedPendingTurn,
  submitWithPendingTracking,
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "./pendingTurnsStore";
import { queueEntryPreviewText, skillMarkers, truncateForDisplay } from "./queueDisplay";
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
  onRestoreToComposer(text: string, attachments?: InputAttachment[], skillNames?: readonly string[]): void;
  activeRecoveryId?: string;
  onEditRecovery?(record: MutationRecoveryRecord): void;
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

// ActionButton wraps an IconButton in a Tooltip only when there's a reason
// worth explaining (a disabled control) - an always-enabled button needs no
// tooltip beyond its own accessible name (IconButton's `label`).
function ActionButton({ disabledReason, ...iconButtonProps }: { disabledReason?: string } & IconButtonProps) {
  if (!disabledReason) return <IconButton {...iconButtonProps} />;
  return (
    <Tooltip label={disabledReason}>
      <IconButton {...iconButtonProps} />
    </Tooltip>
  );
}

const ACTIONS_UNAVAILABLE_REASON = "Queue actions aren't available for this session";

// The recovery-fenced reading of the same refusal: the hub rejects every queue
// action (turn/promoteQueuedAsSteer, turn/drainAsSteer, turn/cancelQueued) for
// a fenced session until the explicit Resume clears it, so the press names
// that path rather than a generic unavailability.
const RECOVERY_ACTIONS_UNAVAILABLE_REASON = "Queue actions aren't available until this session is resumed";

function recordContent(record: MutationOutboxRecord): { text: string; imageCount: number; skillNames: string[] } {
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
  return { text, imageCount: input.filter((item) => item.type === "image").length, skillNames };
}

function recordPreview(record: MutationOutboxRecord): string {
  const { text, imageCount, skillNames } = recordContent(record);
  const preview = [queueEntryPreviewText(text, imageCount), skillMarkers(skillNames)]
    .filter((part) => part !== "")
    .join(" ");
  return truncateForDisplay(preview);
}

// A pending entry already carries its canonical skill names, so it renders the
// same text-plus-markers preview a durable record does. Without the markers an
// entry with no text and no images (a skill-only submission) would be blank
// until the authoritative queue row replaces it.
function pendingPreview(entry: PendingTurnEntry): string {
  return [queueEntryPreviewText(entry.text, entry.imageCount), skillMarkers(entry.skillNames)]
    .filter((part) => part !== "")
    .join(" ");
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
  getComposerText,
  onRestoreToComposer,
  activeRecoveryId,
  onEditRecovery,
  onDrainSuccess,
  busy,
  onDrainBusyChange,
}: QueueStripProps): ReactNode {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const mutationAuthority = useThreadsStore((s) => s.mutationAuthorityRefs.has(sessionRef));
  const recoveryObligated = useThreadsStore((s) => s.restartBlockingObligations.has(sessionRef));
  const pendingQueueEntries = usePendingTurnEntries(sessionRef, "queue").filter(
    // A queue-method row Stop canceled (or one whose delivery turned unknown)
    // is a durable row below, not a bare pending one - the same slot rule the
    // blocked state already follows.
    (entry) => entry.state !== "blockedUnknown" && entry.state !== "canceled",
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
  ].sort((left, right) => left.record.intentSequence - right.record.intentSequence);
  const toasts = useToasts();
  // Keyed by daemon-minted entryId (stable across a re-render even as
  // indices shift), not row index - mirrors the legacy renderer's own
  // setQueuedRowActionsDisabled keying.
  const [busyEntryIds, setBusyEntryIds] = useState<ReadonlySet<string>>(new Set());
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

  async function handlePromote(
    index: number,
    entryId: string,
    displayText: string,
    skillNames?: readonly string[],
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
        onRestoreToComposer(fullText, undefined, skillNames);
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
    // The recovery fence, re-read live at the press: the hub refuses
    // turn/drainAsSteer for the obligation's whole window, so an offered press
    // could only mint durable intent that parks until the explicit Resume
    // action clears it. Ahead of the busy report so a refusal never churns
    // the shared busy state.
    if (pressLocalRecoveryFenced(sessionRef)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    const refusal = pressRefusal(sessionRef, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      return;
    }
    const { text, attachments, hasPending, skillNames } = getComposerText();
    if (hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    // The UI-side half of the skillInput gate, mirroring Composer's own
    // submitAction: a target that does not advertise the capability keeps
    // the draft and hears why instead of enqueueing a drain the store (and
    // the hub) would refuse.
    if ((skillNames?.length ?? 0) > 0 && model?.capabilities.skillInput !== true) {
      toasts.push("error", "Skill selections aren't supported on this session yet; your draft is kept");
      return;
    }
    let wonRecoveryResend = true;
    onDrainBusyChange(true);
    try {
      await submitWithPendingTracking(
        {
          ref: sessionRef,
          recoveryId: activeRecoveryId,
          text,
          attachments,
          skillNames,
          onFailure: (err) => {
            toasts.push("error", sessionActionError("Drain failed", err));
          },
        },
        async () => {
          if (activeRecoveryId) {
            wonRecoveryResend = await resendRecoveryPendingTurn(
              activeRecoveryId,
              sessionRef,
              "drain",
              text,
              attachments ?? [],
              skillNames,
            );
            return;
          }
          return threadsStore.getState().drainAsSteer(sessionRef, text, attachments, skillNames);
        },
      );
      if (!wonRecoveryResend) toasts.push("info", "This message was already sent in another tab.");
      onDrainSuccess();
    } catch {
      // Already reported via onFailure above; swallow so the rejection
      // doesn't reach React as an unhandled promise rejection from this
      // fire-and-forget click handler.
    } finally {
      onDrainBusyChange(false);
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
      if (!(await retryBlockedPendingTurn(record.clientMutationId, sessionRef))) {
        // The projection is already refreshed at this point:
        // retryBlockedPendingTurn is mutateThenRefresh, which awaits its refresh
        // before resolving, so this decides on the state that refresh observed
        // (issue #1722 - a second refresh here read the same durable rows and
        // fed nothing).
        const { outbox } = await readMutationPersistence(sessionRef);
        const current = outbox.find((entry) => entry.clientMutationId === record.clientMutationId);
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
      await discardRecoveryPendingTurn(record.clientMutationId, sessionRef);
    } catch (error) {
      toasts.push("error", `Couldn't dismiss this row: ${errorText(error)}`);
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  async function handleCopy(record: MutationRecoveryRecord): Promise<void> {
    setRowBusy(record.clientMutationId, true);
    try {
      const { text, skillNames } = recordContent(record);
      const content = [text, skillMarkers(skillNames)].filter((part) => part !== "").join("\n");
      await copyToClipboard(content);
      toasts.push("success", "Copied message");
    } catch (error) {
      toasts.push("error", `Couldn't copy message: ${errorText(error)}`);
    } finally {
      setRowBusy(record.clientMutationId, false);
    }
  }

  return (
    <section className={CLASS.strip}>
      <div className={CLASS.header}>
        <h3 className={CLASS.title}>Queued messages ({depth + pendingQueueEntries.length + durableEntries.length})</h3>
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
          // The daemon's preview names skills only generically ("[skill]" /
          // "[N skills]"), while the pending and durable rows for the SAME
          // submission name them from the entry's own canonical selections.
          // When the preview is NOTHING BUT that generic placeholder - the
          // skill-only case, where it carries no information the named markers
          // do not - the placeholder is redundant and is dropped rather than
          // doubled ("[skill] [skill: pkg:probe]"). Every other preview keeps
          // whatever it holds: prose (even when only the daemon supplies it),
          // an image placeholder, or a mix of them, with the named markers
          // appended after it. The preview text is truncated first so a
          // full-length line can never push the markers past the display cap.
          const namedMarkers = skillMarkers(entrySkillNames ?? []);
          const entryPreview = truncateForDisplay(preview?.[index] ?? fullText ?? "");
          const genericSkillPlaceholder = /^\[\d*\s*skills?\]$/i.test(entryPreview.trim());
          const previewText = genericSkillPlaceholder ? "" : entryPreview;
          const displayText = [previewText, namedMarkers].filter((part) => part !== "").join(" ");
          const busy = entryId !== undefined && busyEntryIds.has(entryId);
          const actionsAvailable = hasIds && entryId !== undefined;
          // A blank-text entry is uneditable only when it carries nothing
          // else restorable - a skill-only entry's chips ARE the content.
          const imageOnly = hasTexts && (fullText ?? "").trim() === "" && (entrySkillNames?.length ?? 0) === 0;
          const editAvailable = actionsAvailable && hasTexts && !imageOnly;

          return (
            // key: entryId is the real, stable identity whenever the daemon
            // reports one; index is only a last-resort fallback for a
            // degraded/old daemon that reports no ids array at all, where no
            // better identity exists.
            <li key={entryId ?? index} className={CLASS.row}>
              <span className={CLASS.rowText}>{displayText}</span>
              <div className={CLASS.rowActions}>
                <ActionButton
                  label="Steer now"
                  icon={<span aria-hidden="true">⇧</span>}
                  size="sm"
                  disabled={!actionsAvailable || !controls.drain || busy}
                  disabledReason={
                    !actionsAvailable
                      ? ACTIONS_UNAVAILABLE_REASON
                      : controls.drain
                        ? undefined
                        : (controls.reason.drain ?? STEER_UNAVAILABLE)
                  }
                  onClick={() => {
                    if (entryId !== undefined) {
                      // The ghost's display text: the row's full text, or - for a
                      // blank row - the same stripped preview the row renders
                      // above: the image placeholder for an image-only row (its
                      // whole content), and nothing for a skill-only row, where
                      // the named markers carry the whole content and the raw
                      // "[skill]" placeholder would only double it.
                      const rowText = fullText ?? "";
                      const displayText = rowText.trim() !== "" ? rowText : previewText;
                      void handlePromote(index, entryId, displayText, entrySkillNames);
                    }
                  }}
                />
                <ActionButton
                  label="Edit message"
                  icon={<span aria-hidden="true">✎</span>}
                  size="sm"
                  disabled={!editAvailable || busy}
                  disabledReason={editDisabledReason({ actionsAvailable, hasTexts, imageOnly })}
                  onClick={() => {
                    if (entryId !== undefined && fullText !== undefined) {
                      void handleEdit(index, entryId, fullText, entrySkillNames);
                    }
                  }}
                />
                <ActionButton
                  label="Remove from queue"
                  icon={<span aria-hidden="true">✕</span>}
                  variant="danger"
                  size="sm"
                  disabled={!actionsAvailable || busy}
                  disabledReason={actionsAvailable ? undefined : ACTIONS_UNAVAILABLE_REASON}
                  onClick={() => {
                    if (entryId !== undefined) void handleCancel(index, entryId);
                  }}
                />
              </div>
            </li>
          );
        })}
        {pendingQueueEntries.map((entry) => (
          <li key={entry.id} className={`${CLASS.row} ${CLASS.rowPending}`}>
            <span className={CLASS.rowText}>{pendingPreview(entry)}</span>
          </li>
        ))}
        {durableEntries.map(({ kind, record }) => {
          const rowBusy = busyEntryIds.has(record.clientMutationId);
          const retryError = retryErrors.get(record.clientMutationId);
          if (kind === "blocked" || kind === "canceled") {
            // Delivery-uncertain rows and Stop-canceled rows share one slot
            // (stop-cancellation-outbox §6 Display): both are this client's
            // own undelivered submissions sitting in durable storage, and
            // they differ only in the label - uncertain delivery versus a
            // cancellation the user's own click wrote. The Retry affordance,
            // its gating, and the error slot are identical.
            return (
              <li key={record.clientMutationId} className={CLASS.row}>
                <span className={CLASS.rowText}>
                  <span>{kind === "canceled" ? "Canceled by Stop" : "Delivery uncertain"}</span>
                  {" — "}
                  <span>{recordPreview(record)}</span>
                  {retryError !== undefined && <span role="alert">{retryError}</span>}
                </span>
                <div className={CLASS.rowActions}>
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
                      retryBlockedBySnapshot(model?.status.type, mutationAuthority, recoveryObligated)
                    }
                    onClick={() => void handleRetry(record)}
                  >
                    {rowBusy ? "Retrying…" : "Retry"}
                  </Button>
                </div>
              </li>
            );
          }
          if (record.recoveryKind === "orphaned") {
            return (
              <li key={record.clientMutationId} className={CLASS.row}>
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
            <li key={record.clientMutationId} className={CLASS.row}>
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
                  <ActionButton
                    label="Dismiss"
                    icon={<span aria-hidden="true">✕</span>}
                    size="sm"
                    disabled={rowBusy}
                    onClick={() => void handleDismiss(record)}
                  />
                ) : (
                  <ActionButton
                    label="Edit message"
                    icon={<span aria-hidden="true">✎</span>}
                    size="sm"
                    disabled={rowBusy || onEditRecovery === undefined}
                    disabledReason={
                      onEditRecovery === undefined ? "Open the session Composer to edit this message" : undefined
                    }
                    onClick={() => onEditRecovery?.(record)}
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
