// Turn-failure end-cap: the diagnostic that closes a failed turn (parity-m4
// §9:237, renderer.js:4259-4278 diagnostic card + 4428-4521 recovery actions).
// It reads TurnModel.error (the wire's TurnError, reducer.ts:216) and renders a
// taxonomy chip + the error message + an optional hint, plus a recovery action
// that re-issues the turn.
//
// Design system: the failure "colour" is carried entirely by the danger Chip
// (its tone is allowlisted in token-contract.test.ts); this component's own CSS
// module stays on neutral ink/edge tokens, because a non-widget stylesheet may
// not reference --danger (the same posture as tools/sandboxEscalation). The
// recovery button is NOT danger-toned - re-issuing a turn is not destructive, so
// spending danger on it would misread under color-is-attention.
//
// The recovery action needs the session ref, which reaches TurnBlock only once
// Session.tsx (a controller-owned chokepoint) passes it down. Until that
// one-line wiring lands, the diagnostic still renders in full - only the action
// button is withheld (see .superpowers/sdd/w8-t3-report.md).

import { useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import type { TurnError } from "../../../protocol/types.gen";
import { type InputAttachment, threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hint: requireClass(styles.hint, "turnfailure.module.css", "hint"),
  hintSummary: requireClass(styles.hintSummary, "turnfailure.module.css", "hintSummary"),
};

// The turn that failed opened with the user's own input as its first item
// (EventUserInput opens a turn, then inserts the userMessage, before the
// assistant works in that same turn - appwire_projection.go:131-168), so it
// is the honest thing to re-issue on retry. Absent (an empty or item-less
// turn), there is nothing to retry.
export interface RetryInput {
  text: string;
  attachments?: InputAttachment[];
  // How many images the originating item carried, including ones whose bytes
  // did not survive to the model (a sha-routed src with no inline data) and
  // therefore could not become attachments. The retry clicker reports the
  // difference as dropped; it must come from the ORIGINATING item, which for
  // a reloaded failure sits in an earlier turn than the failed one.
  sourceImageCount: number;
}

// retryImages recovers the originating input's image bytes from the model's
// display-ready ItemImage shape (reducer.ts's imagesToItemImages resolves the
// wire's inline mediaType+data bytes to a data: URI src, which is exactly what
// a live userMessage item carries). Each image becomes an InputAttachment the
// send path already knows how to wire (threads.ts's buildInput), with markers
// recovered from the translated "(attached image N)" prose the submit
// boundary wrote (attachmentMarkers.ts) - falling back to 1-based position
// for prose that carries no marker (a reloaded transcript's text). Images
// whose bytes are unavailable (a sha-routed src with no inline data) are
// dropped: resending a name with no bytes would fabricate an attachment.
function retryImages(item: ItemModel | undefined, text: string): InputAttachment[] | undefined {
  const images = item?.images;
  if (!images || images.length === 0) return undefined;
  const markers = Array.from(text.matchAll(/\(attached image (\d+)(?::[^)]*)?\)/g), (match) => Number(match[1]));
  const attachments: InputAttachment[] = [];
  images.forEach((image, index) => {
    const dataUri = /^data:([^;,]+)?;base64,(.*)$/s.exec(image.src);
    if (!dataUri) return;
    attachments.push({
      marker: markers[index] ?? index + 1,
      mediaType: dataUri[1] || "image/png",
      data: dataUri[2] ?? "",
      ...(image.name ? { name: image.name } : {}),
    });
  });
  return attachments.length > 0 ? attachments : undefined;
}

function retryItem(turn: TurnModel): ItemModel | undefined {
  return turn.items.find((it) => it.type === "userMessage");
}

function retryInput(turn: TurnModel): RetryInput | undefined {
  const item = retryItem(turn);
  const text = item?.text.trim() ?? "";
  const attachments = retryImages(item, text);
  // An image-only input is retryable: buildInput and the server both accept
  // empty text with attachments (parity-m5-composer §B). Text is required
  // only when there is nothing else to send.
  if (!text && !attachments) return undefined;
  const sourceImageCount = item?.images?.length ?? 0;
  return attachments ? { text, attachments, sourceImageCount } : { text, sourceImageCount };
}

/**
 * The input that opened the exchange `turnId` ended, searched backwards from
 * that turn.
 *
 * A LIVE failure keeps the input in its own turn, where retryText finds it. A
 * RELOADED one does not: one persisted transcript entry becomes one turn
 * (apptranscript.go's ProjectTurn), so a failure entry is a turn holding only
 * the failure, and the input sits in an earlier one. Retry was therefore
 * offered while a reader watched a failure happen and withheld from the reader
 * who came back to it - the same failure, the same recovery, present or absent
 * on nothing but when you looked.
 *
 * The search stops AT the failed turn: a later prompt is a different exchange,
 * and re-issuing it would answer a question the reader did not ask.
 */
export function originatingInput(turns: TurnModel[], turnId: string): RetryInput | undefined {
  const found = turns.findIndex((t) => t.id === turnId);
  const from = found === -1 ? turns.length - 1 : found;
  for (let i = from; i >= 0; i--) {
    const turn = turns[i];
    const input = turn && retryInput(turn);
    if (input) return input;
  }
  return undefined;
}

export function TurnFailureEndCap({
  error,
  turn,
  sessionRef,
}: {
  error: TurnError;
  turn: TurnModel;
  sessionRef?: string;
}) {
  const info = classifyTurnError(error);
  const toasts = useToasts();
  const [hintOpen, setHintOpen] = useState(false);
  // Selected as a JSON string (compared by value, not identity) so this cap
  // re-renders only when what it would re-issue actually changes, not on
  // every delta the thread takes.
  const priorInputJson = useThreadsStore((s) =>
    sessionRef === undefined
      ? undefined
      : JSON.stringify(originatingInput(s.threads.get(sessionRef)?.turns ?? [], turn.id) ?? null),
  );
  const priorInput =
    priorInputJson === undefined ? undefined : ((JSON.parse(priorInputJson) as RetryInput | null) ?? undefined);
  const input = retryInput(turn) ?? priorInput;
  const canRetry = sessionRef !== undefined && input !== undefined;

  // Recovery re-issues the turn's originating input via the existing
  // threadsStore.send action (turn/start), images included: the originating
  // userMessage item still carries the bytes the first send projected
  // (projectUserInputImages), so a retry that resent text alone would answer
  // a different question than the one asked. Bytes that did not survive to
  // the model (a sha-routed src with no inline data, e.g. a reloaded turn)
  // cannot be re-issued; those are dropped and named in a warning toast so
  // the silent text-only resend this fixes never recurs. For a
  // connection-class failure the hub's auto-resume layer transparently
  // relaunches a dead daemon, so a single call serves both the "Retry" and
  // "Reconnect & retry" labels; a failed re-issue surfaces on the shared
  // toast singleton, never a silent swallow.
  async function retry() {
    if (sessionRef === undefined || input === undefined) return;
    try {
      const dropped = input.sourceImageCount - (input.attachments?.length ?? 0);
      await threadsStore.getState().send(sessionRef, input.text, input.attachments);
      if (dropped > 0) {
        toasts.push(
          "warning",
          `Retried without ${dropped === 1 ? "an attached image" : `${dropped} attached images`} - re-attach ${dropped === 1 ? "it" : "them"} to ask about ${dropped === 1 ? "it" : "them"} again.`,
        );
      }
    } catch (e) {
      toasts.push("error", sessionActionError(`${info.recoveryLabel} failed`, e));
    }
  }

  return (
    <div className={CLASS.cap} data-testid="turn-failure" data-turn-error="true">
      <div className={CLASS.head}>
        <Chip tone="danger">{info.badge}</Chip>
        <span className={CLASS.message}>{info.message}</span>
        {info.hint && (
          <button
            type="button"
            className={CLASS.hintSummary}
            aria-expanded={hintOpen}
            onClick={() => setHintOpen((o) => !o)}
          >
            What can I do?
          </button>
        )}
        {hintOpen && info.hint && <div className={CLASS.hint}>{info.hint}</div>}
        {canRetry && (
          <Button variant="primary" size="sm" onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
      </div>
    </div>
  );
}
