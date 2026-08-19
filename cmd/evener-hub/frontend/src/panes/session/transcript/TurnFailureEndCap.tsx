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
import type { TurnModel } from "../../../protocol/model";
import type { TurnError } from "../../../protocol/types.gen";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
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
// assistant works in that same turn - appwire_projection.go:131-168), so its
// text is the honest thing to re-issue on retry. Absent (an empty or
// item-less turn), there is nothing to retry.
function retryText(turn: TurnModel): string | undefined {
  const text = turn.items.find((it) => it.type === "userMessage")?.text.trim();
  return text ? text : undefined;
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
export function originatingInput(turns: TurnModel[], turnId: string): string | undefined {
  const found = turns.findIndex((t) => t.id === turnId);
  const from = found === -1 ? turns.length - 1 : found;
  for (let i = from; i >= 0; i--) {
    const turn = turns[i];
    const text = turn && retryText(turn);
    if (text) return text;
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
  // Selected down to a plain string so this cap re-renders only when the text
  // it would re-issue actually changes, not on every delta the thread takes.
  const priorInput = useThreadsStore((s) =>
    sessionRef === undefined ? undefined : originatingInput(s.threads.get(sessionRef)?.turns ?? [], turn.id),
  );
  const text = retryText(turn) ?? priorInput;
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
