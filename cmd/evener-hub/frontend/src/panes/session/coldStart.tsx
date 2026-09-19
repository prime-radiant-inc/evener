import { SYSTEM_PRELUDE_TURN_ID, type ThreadModel, type TurnModel } from "@evener/appwire-client";
import { Skeleton } from "../../widgets";
import { useAwaitingFirstFrameSend, usePendingTurnEntries } from "./composer/queue/pendingTurnsStore";
import styles from "./session.module.css";

const THREAD_TERMINAL_STATUSES = new Set(["closed", "systemError"]);
const TURN_TERMINAL_STATUSES = new Set(["cancelled", "canceled", "completed", "error", "failed", "interrupted"]);

function isThreadTerminalStatus(status: string): boolean {
  return THREAD_TERMINAL_STATUSES.has(status);
}

function isTurnTerminalStatus(status: string): boolean {
  return TURN_TERMINAL_STATUSES.has(status.toLowerCase());
}

function realTurns(turns: readonly TurnModel[]): TurnModel[] {
  return turns.filter((turn) => turn.id !== SYSTEM_PRELUDE_TURN_ID);
}

function hasAuthoritativeFrame(turn: TurnModel): boolean {
  return turn.items.some((item) => item.type !== "userMessage" && item.type !== "systemMessage");
}

function shouldShowColdStart(model: ThreadModel, hasPendingSend: boolean, awaitingFirstFrame: boolean): boolean {
  if (model.status.type === "restartRequired" || isThreadTerminalStatus(model.status.type)) return false;

  const turns = realTurns(model.turns);
  if (turns.length === 0) return hasPendingSend || awaitingFirstFrame;
  if (turns.length !== 1) return false;

  const [firstTurn] = turns;
  if (!firstTurn || hasAuthoritativeFrame(firstTurn) || isTurnTerminalStatus(firstTurn.status)) return false;
  return hasPendingSend || awaitingFirstFrame;
}

export function useColdStartSkeleton(sessionRef: string, model: ThreadModel | null | undefined): boolean {
  const pendingSends = usePendingTurnEntries(sessionRef, "send");
  const awaitingFirstFrame = useAwaitingFirstFrameSend(sessionRef);
  // A canceled send provably never reached the daemon, so no turn is coming
  // because of it - counting it held the skeleton up forever, clearable only
  // by Retry or thread teardown. The same exclusion the Composer's
  // ownPendingSend and PendingChips' isOptimistic carry; the canceled row
  // itself stays visible where its Retry affordance lives (QueueStrip).
  const hasPendingSend = pendingSends.some((entry) => entry.state !== "canceled");
  return model !== null && model !== undefined && shouldShowColdStart(model, hasPendingSend, awaitingFirstFrame);
}

export function ColdStartSkeleton() {
  return (
    <div className={styles.coldStart} data-testid="cold-start-skeleton">
      <Skeleton lines={3} />
    </div>
  );
}
