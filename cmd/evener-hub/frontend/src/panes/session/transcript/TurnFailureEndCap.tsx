// The failed-turn diagnostic offers continuation without consuming the composer.
import type { TurnError } from "@evener/appwire-client";
import { isLatestRetryTurn, sessionActionError } from "@evener/appwire-client";
import { ownPendingTurnInput } from "@evener/appwire-client/state/mutation";
import { useEffect, useState } from "react";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, Disclosure, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import {
  ensureRetryReflectionResolved,
  usePendingTurnEntries,
  usePendingTurnReadiness,
  useRetryAdmissionInFlight,
  useRetryAwaitingReflection,
} from "../composer/queue/pendingTurnsStore";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hintText: requireClass(styles.hintText, "turnfailure.module.css", "hintText"),
};

export interface TurnFailureEndCapProps {
  error: TurnError;
  sessionRef?: string;
  // The source turn this end-cap closes. Retry belongs to that turn's own
  // failure, so the action is projected from the canonical latest-turn rule
  // over this id, never from the fact that a failure rendered here.
  turnId: string;
}

export function TurnFailureEndCap({ error, sessionRef, turnId }: TurnFailureEndCapProps) {
  const info = classifyTurnError(error);
  const [hintOpen, setHintOpen] = useState(false);
  // The live model, not a captured viewport row: closing and reopening,
  // refreshing history, or a newer turn landing all move this subscription
  // through the same owner the activation rechecks.
  const thread = useThreadsStore((state) => (sessionRef === undefined ? undefined : state.threads.get(sessionRef)));
  const retryable = sessionRef !== undefined && thread !== undefined && isLatestRetryTurn(thread, turnId);

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
        {retryable && <RetryAction sessionRef={sessionRef} turnId={turnId} recoveryLabel={info.recoveryLabel} />}
      </div>
    </div>
  );
}

// Retry stays visible while its submission is unresolved but cannot act: the
// pending projection is the durable owner, and an unread projection is
// unknown, not empty, so the action waits for its own read before enabling.
function RetryAction({
  sessionRef,
  turnId,
  recoveryLabel,
}: {
  sessionRef: string;
  turnId: string;
  recoveryLabel: string;
}) {
  const toasts = useToasts();
  const pendingLoaded = usePendingTurnReadiness(sessionRef);
  const pendingEntries = usePendingTurnEntries(sessionRef);
  const admissionInFlight = useRetryAdmissionInFlight(sessionRef);
  const awaitingReflection = useRetryAwaitingReflection(sessionRef, turnId);
  const pending = ownPendingTurnInput(pendingEntries);
  useEffect(() => {
    if (awaitingReflection) ensureRetryReflectionResolved(sessionRef);
  }, [awaitingReflection, sessionRef]);
  async function retry() {
    try {
      await threadsStore.getState().continueTurn(sessionRef, turnId);
    } catch (e) {
      toasts.push("error", sessionActionError(`${recoveryLabel} failed`, e));
    }
  }

  return (
    <Button
      variant="primary"
      size="sm"
      disabled={!pendingLoaded || pending || admissionInFlight || awaitingReflection}
      onClick={() => void retry()}
    >
      {recoveryLabel}
    </Button>
  );
}
