// QueueStrip: the queue strip UI (parity-m5-composer.md §B) - real queue
// rows from model.queue with promote/edit/cancel actions (expectedEntryId-
// guarded, Conflict-safe per stores/threads.ts), the drain-as-steer
// affordance, and optimistic pending queue rows from this stream's own
// pendingTurnsStore. Self-contained per the wave's integration seam: reads
// only props (documented on QueueStripProps below) plus the threads store
// and pendingTurnsStore - Composer.tsx/Session.tsx are outside this manifest,
// so mounting this inside Composer's own tree happens at the wave
// integration merge (T6), not here.
import { type ReactNode, useState } from "react";
import { WireError } from "../../../../protocol/errors";
import type { TurnCancelQueuedResponse } from "../../../../protocol/types.gen";
import type { InputAttachment } from "../../../../stores/threads";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, IconButton, type IconButtonProps, Tooltip, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { submitWithPendingTracking, usePendingTurnEntries } from "./pendingTurnsStore";
import { queueEntryPreviewText, truncateForDisplay } from "./queueDisplay";
import styles from "./queuestrip.module.css";

const CLASS = {
  strip: requireClass(styles.strip, "queuestrip.module.css", "strip"),
  header: requireClass(styles.header, "queuestrip.module.css", "header"),
  title: requireClass(styles.title, "queuestrip.module.css", "title"),
  list: requireClass(styles.list, "queuestrip.module.css", "list"),
  row: requireClass(styles.row, "queuestrip.module.css", "row"),
  rowPending: requireClass(styles.rowPending, "queuestrip.module.css", "rowPending"),
  rowText: requireClass(styles.rowText, "queuestrip.module.css", "rowText"),
  rowActions: requireClass(styles.rowActions, "queuestrip.module.css", "rowActions"),
};

// Session.tsx's own loadOlder catch is this wave's reference implementation
// for the failure-feedback convention (T1) - this local helper matches it
// verbatim, per that file's own established per-file-duplication style
// (sandboxEscalation.tsx carries an identical copy rather than a shared
// util).
function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function isQueuedDrainPartial(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "queuedDrainPartial";
}

export interface QueueStripProps {
  ref: string;
  // Returns the composer's CURRENT text/attachments at the moment the drain
  // affordance is used - drainAsSteer atomically appends this to the queue
  // before draining the whole thing into the active turn as one steering
  // message (see stores/threads.ts's own drainAsSteer doc comment for why
  // this is a getter, not a cached value).
  getComposerText(): { text: string; attachments?: InputAttachment[] };
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
  onRestoreToComposer(text: string, attachments?: InputAttachment[]): void;
  // Called once a drain-as-steer request succeeds, so the integration can
  // clear the composer's own text/attachment state (mirrors the legacy
  // "the textarea clears" behavior on a successful drain).
  onDrainSuccess(): void;
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
  onDrainSuccess,
}: QueueStripProps): ReactNode {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const pendingQueueEntries = usePendingTurnEntries(sessionRef, "queue");
  const toasts = useToasts();
  // Keyed by daemon-minted entryId (stable across a re-render even as
  // indices shift), not row index - mirrors the legacy renderer's own
  // setQueuedRowActionsDisabled keying.
  const [busyEntryIds, setBusyEntryIds] = useState<ReadonlySet<string>>(new Set());
  const [draining, setDraining] = useState(false);

  const queue = model?.queue ?? null;
  const depth = queue?.depth ?? 0;
  // The wrap is hidden at depth 0 UNLESS an optimistic pending queue entry
  // is still in flight, so a just-submitted message doesn't visually
  // disappear before the daemon confirms it (parity §B).
  const visible = depth > 0 || pendingQueueEntries.length > 0;

  if (!model || !visible) return null;

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

  // reportRemovedImages surfaces the shared "images weren't restored"
  // warning both a plain cancel and an edit's own cancel step can produce -
  // a contract row for both flows, not just edit's.
  function reportRemovedImages(result: TurnCancelQueuedResponse): void {
    const n = result.removedImages ?? 0;
    if (n <= 0) return;
    const noun = n === 1 ? "image attachment" : "image attachments";
    const pronoun = n === 1 ? "it" : "them";
    toasts.push("warning", `Removed from the queue, but ${n} ${noun} weren't restored - please re-attach ${pronoun}.`);
  }

  async function handlePromote(index: number, entryId: string): Promise<void> {
    setRowBusy(entryId, true);
    try {
      await threadsStore.getState().promoteQueuedAsSteer(sessionRef, index, entryId);
      // Success is entirely rendered by the daemon's own thread/queueChanged
      // (row removed) + serf/steering/injected (transcript shows it) - no
      // local mirror, per parity §B.
    } catch (err) {
      toasts.push("error", `Couldn't send this message now: ${errorMessage(err)}`);
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleCancel(index: number, entryId: string): Promise<void> {
    setRowBusy(entryId, true);
    try {
      const result = await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
      reportRemovedImages(result);
    } catch (err) {
      toasts.push("error", `Couldn't remove this message from the queue: ${errorMessage(err)}`);
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleEdit(index: number, entryId: string, fullText: string): Promise<void> {
    setRowBusy(entryId, true);
    // FIRST - loser-safe: the user's text is safely in the composer
    // regardless of whether the cancel below succeeds (contract row).
    onRestoreToComposer(fullText);
    try {
      const result = await threadsStore.getState().cancelQueued(sessionRef, index, entryId);
      reportRemovedImages(result);
    } catch (err) {
      toasts.push("error", `Moved to the composer, but couldn't remove it from the queue: ${errorMessage(err)}`);
    } finally {
      setRowBusy(entryId, false);
    }
  }

  async function handleDrain(): Promise<void> {
    const { text, attachments } = getComposerText();
    setDraining(true);
    try {
      await submitWithPendingTracking(
        {
          ref: sessionRef,
          method: "drain",
          text,
          attachments,
          // Covers BOTH an immediate perform() rejection and - later,
          // asynchronously - the 10s timeout reaper (there is no other way
          // to observe that second case from this handler's own try/catch
          // below, which only wraps the initial await).
          onFailure: (err) => {
            const message = errorMessage(err);
            toasts.push(
              "error",
              isQueuedDrainPartial(err) ? `Queued, but drain failed: ${message}` : `Drain failed: ${message}`,
            );
          },
        },
        () => threadsStore.getState().drainAsSteer(sessionRef, text, attachments),
      );
      onDrainSuccess();
    } catch {
      // Already reported via onFailure above; swallow so the rejection
      // doesn't reach React as an unhandled promise rejection from this
      // fire-and-forget click handler.
    } finally {
      setDraining(false);
    }
  }

  return (
    <section className={CLASS.strip}>
      <div className={CLASS.header}>
        <h3 className={CLASS.title}>Queued messages ({depth})</h3>
        <Tooltip label="Send your message and everything queued into the current turn">
          <Button variant="quiet" size="sm" onClick={() => void handleDrain()} disabled={draining}>
            Steer now
          </Button>
        </Tooltip>
      </div>
      <ul className={CLASS.list}>
        {Array.from({ length: rowCount }, (_, index) => {
          const entryId = ids?.[index];
          const fullText = texts?.[index];
          const displayText = truncateForDisplay(preview?.[index] ?? fullText ?? "");
          const busy = entryId !== undefined && busyEntryIds.has(entryId);
          const actionsAvailable = hasIds && entryId !== undefined;
          const imageOnly = hasTexts && (fullText ?? "").trim() === "";
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
                  label="Send now"
                  icon={<span aria-hidden="true">⇧</span>}
                  size="sm"
                  disabled={!actionsAvailable || busy}
                  disabledReason={actionsAvailable ? undefined : ACTIONS_UNAVAILABLE_REASON}
                  onClick={() => {
                    if (entryId !== undefined) void handlePromote(index, entryId);
                  }}
                />
                <ActionButton
                  label="Edit message"
                  icon={<span aria-hidden="true">✎</span>}
                  size="sm"
                  disabled={!editAvailable || busy}
                  disabledReason={editDisabledReason({ actionsAvailable, hasTexts, imageOnly })}
                  onClick={() => {
                    if (entryId !== undefined && fullText !== undefined) void handleEdit(index, entryId, fullText);
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
            <span className={CLASS.rowText}>{queueEntryPreviewText(entry.text, entry.imageCount)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
