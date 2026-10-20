// The failed-turn diagnostic offers continuation without consuming the composer.
import type { TurnError } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { useState } from "react";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, Disclosure, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hintText: requireClass(styles.hintText, "turnfailure.module.css", "hintText"),
};

export function TurnFailureEndCap({ error, sessionRef }: { error: TurnError; sessionRef?: string }) {
  const info = classifyTurnError(error);
  const toasts = useToasts();
  const [hintOpen, setHintOpen] = useState(false);
  async function retry() {
    if (sessionRef === undefined) return;
    try {
      await threadsStore.getState().continueTurn(sessionRef);
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
        {sessionRef !== undefined && (
          <Button variant="primary" size="sm" onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        )}
      </div>
    </div>
  );
}
