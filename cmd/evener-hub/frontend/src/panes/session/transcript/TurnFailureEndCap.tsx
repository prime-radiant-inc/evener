// Turn-failure end-cap: the diagnostic that closes a failed turn (parity-m4
// §9:237, renderer.js:4259-4278 diagnostic card + 4428-4521 recovery actions).
// It reads TurnModel.error (the wire's TurnError, reducer.ts:216) and renders a
// taxonomy chip + the error message + an optional hint, plus a recovery action
// that re-issues the turn. The originating-input lookback and its image-byte
// recovery (originatingInput, findOriginatingItem, originFromItem, retryItem)
// live in the shared package's turnRetryInput module, which the native error
// row's Retry shares.
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

import type { OriginatingInput, TurnError, TurnModel } from "@evener/appwire-client";
import { findOriginatingItem, originFromItem, retryItem, sessionActionError } from "@evener/appwire-client";
import { useMemo, useState } from "react";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, Disclosure, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hint: requireClass(styles.hint, "turnfailure.module.css", "hint"),
  hintText: requireClass(styles.hintText, "turnfailure.module.css", "hintText"),
};

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
  // Selected by ITEM reference, never by payload value: computing the retry
  // payload copies every image's base64 bytes, which would freeze the UI if it
  // ran on each unrelated store update. The reducer's immutable folds hand
  // back the same item reference for untouched turns (reducer.ts's mapItem),
  // and zustand's default Object.is equality then skips the re-render, so the
  // payload is recomputed only when the originating item itself changes.
  const priorItem = useThreadsStore((s) =>
    sessionRef === undefined ? undefined : findOriginatingItem(s.threads.get(sessionRef)?.turns ?? [], turn.id),
  );
  const priorOrigin = useMemo(() => (priorItem === undefined ? undefined : originFromItem(priorItem)), [priorItem]);
  const ownItem = retryItem(turn);
  const ownOrigin = useMemo(
    () => (ownItem === undefined ? undefined : originFromItem(ownItem)),
    // ownItem is derived from the turn prop, not the store: its identity
    // already changes exactly when the failed turn re-renders with new items.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ownItem],
  );
  // A blank item on the failed turn (whitespace text, no images) yields no
  // origin: fall back to the lookback, exactly as originatingInput skips
  // unusable inputs. An explicit images-unavailable on the failed turn still
  // wins — it names the exchange's own lost bytes, not an older prompt.
  const origin: OriginatingInput | undefined = ownOrigin === undefined ? priorOrigin : ownOrigin;
  const input = origin?.kind === "retry" ? origin.input : undefined;
  const canRetry = sessionRef !== undefined && input !== undefined;
  const unavailableCount = origin?.kind === "images-unavailable" ? origin.sourceImageCount : 0;
  const showReattachNote = unavailableCount > 0;

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
          <Disclosure open={hintOpen} onOpenChange={setHintOpen} summary="What can I do?">
            <div className={CLASS.hintText}>{info.hint}</div>
          </Disclosure>
        )}
        {canRetry && (
          <Button variant="primary" size="sm" onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
        {showReattachNote && (
          <span className={CLASS.hint} data-testid="turn-failure-reattach" data-count={unavailableCount}>
            {unavailableCount === 1
              ? "Attached image unavailable — re-attach the image to retry."
              : `Attached images unavailable — re-attach ${unavailableCount} images to retry.`}
          </span>
        )}
      </div>
    </div>
  );
}
