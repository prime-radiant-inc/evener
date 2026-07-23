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
import type { TurnModel } from "../../../protocol/model";
import type { TurnError } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { classifyTurnError } from "./turnFailure";
import styles from "./turnfailure.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turnfailure.module.css", "cap"),
  head: requireClass(styles.head, "turnfailure.module.css", "head"),
  message: requireClass(styles.message, "turnfailure.module.css", "message"),
  hint: requireClass(styles.hint, "turnfailure.module.css", "hint"),
  actions: requireClass(styles.actions, "turnfailure.module.css", "actions"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// The turn that failed opened with the user's own input as its first item
// (EventUserInput opens a turn, then inserts the userMessage, before the
// assistant works in that same turn - appwire_projection.go:131-168), so its
// text is the honest thing to re-issue on retry. Absent (an empty or
// item-less turn), there is nothing to retry.
function retryText(turn: TurnModel): string | undefined {
  const text = turn.items.find((it) => it.type === "userMessage")?.text.trim();
  return text ? text : undefined;
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
  const text = retryText(turn);
  const canRetry = sessionRef !== undefined && text !== undefined;

  // Recovery re-issues the turn's originating input via the existing
  // threadsStore.send action (turn/start). For a connection-class failure the
  // hub's auto-resume layer transparently relaunches a dead daemon, so a single
  // call serves both the "Retry" and "Reconnect & retry" labels; a failed
  // re-issue surfaces on the shared toast singleton, never a silent swallow.
  async function retry() {
    if (sessionRef === undefined || text === undefined) return;
    try {
      await threadsStore.getState().send(sessionRef, text);
    } catch (e) {
      toasts.push("error", `${info.recoveryLabel} failed: ${errorMessage(e)}`);
    }
  }

  return (
    <div className={CLASS.cap} data-testid="turn-failure" data-turn-error="true">
      <div className={CLASS.head}>
        <Chip tone="danger">{info.badge}</Chip>
        <span className={CLASS.message}>{info.message}</span>
      </div>
      {info.hint && <div className={CLASS.hint}>{info.hint}</div>}
      {canRetry && (
        <div className={CLASS.actions}>
          <Button variant="primary" size="sm" onClick={() => void retry()}>
            {info.recoveryLabel}
          </Button>
        </div>
      )}
    </div>
  );
}
