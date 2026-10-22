// The failed-turn diagnostic offers continuation without consuming the composer.
//
// Retry has exactly two owners. The canonical latest turn belongs to
// threadsStore (the shared isLatestRetryTurn predicate), and the unresolved own
// submission belongs to the durable outbox projection (ownPendingTurnInput).
// This component projects those two reads; it never captures them as authority.
// continueTurn rechecks both at activation.
import type { TurnError } from "@evener/appwire-client";
import { isLatestRetryTurn, sessionActionError } from "@evener/appwire-client";
import { useState } from "react";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, Disclosure, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useRetryPendingState } from "../composer/queue/pendingTurnsStore";
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
  const pending = useRetryPendingState(sessionRef ?? "");
  const isLatest = model !== undefined && isLatestRetryTurn(model, turnId);
  // An unloaded durable read is unknown, not empty: Retry stays inactive until
  // the outbox owner answers for this ref.
  const retryBlocked = !pending.loaded || pending.unresolved;

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
        {sessionRef !== undefined && isLatest && (
          <Button variant="primary" size="sm" disabled={retryBlocked} onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
      </div>
    </div>
  );
}
