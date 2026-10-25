// The failed-turn diagnostic offers continuation without consuming the composer.
//
// Retry has one shared decision. The canonical latest turn belongs to
// threadsStore, and the pending view belongs to the durable outbox projection;
// this component feeds both into retryEligibility and renders its answer. It
// never captures them as authority: continueTurn calls the same decision on
// live state at activation.
import type { TurnError } from "@evener/appwire-client";
import { isTurnError, retryEligibility, sessionActionError } from "@evener/appwire-client";
import { useState } from "react";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, Disclosure, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useRetryPendingProjection } from "../composer/queue/pendingTurnsStore";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hintText: requireClass(styles.hintText, "turnfailure.module.css", "hintText"),
};

export function TurnFailureEndCap({
  error,
  turnId,
  sessionRef,
}: {
  error: TurnError;
  // The failed turn this end-cap belongs to. Retry is offered only while this
  // is the canonical session model's latest turn.
  turnId: string;
  sessionRef?: string;
}) {
  const info = classifyTurnError(error);
  const toasts = useToasts();
  const [hintOpen, setHintOpen] = useState(false);
  const model = useThreadsStore((state) => (sessionRef === undefined ? undefined : state.threads.get(sessionRef)));
  // Always called: hooks cannot depend on whether the caller passed a ref.
  // An empty ref simply has no projected entries.
  const pending = useRetryPendingProjection(sessionRef ?? "");
  // The one shared eligibility decision, rendered as-is. The end-cap mounts
  // only for a turn whose projected error is a real TurnError (TurnBlock's
  // asTurnError), so the displayed failure is the clicked-turn fact; the
  // activation rechecks the canonical turn through the same input.
  const decision =
    model === undefined
      ? null
      : retryEligibility({
          clickedTurnId: turnId,
          thread: model,
          clickedTurnFailed: isTurnError(error),
          pendingMutations: pending.entries,
          pendingMutationsReady: pending.loaded,
          // Browser Retry is durably routed: the enqueue funnel owns the live
          // client requirement and reports its refusal to the press. Moving
          // that check into eligibility would turn a reported failure into a
          // silent no-op.
          serviceReady: true,
        });
  // Not-latest and not-failed hide the action; the pending and service
  // answers keep it visible but disabled.
  const offersRetry =
    decision !== null && decision.reason !== "not-latest-turn" && decision.reason !== "clicked-turn-not-failed";
  const retryBlocked = decision === null || !decision.allowed;

  async function retry() {
    if (sessionRef === undefined) return;
    try {
      await threadsStore.getState().continueTurn(sessionRef, turnId);
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
        {sessionRef !== undefined && offersRetry && (
          <Button variant="primary" size="sm" disabled={retryBlocked} onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
      </div>
    </div>
  );
}
