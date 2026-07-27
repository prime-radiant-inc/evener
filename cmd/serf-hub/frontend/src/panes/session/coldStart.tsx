import type { ThreadModel, TurnModel } from "../../protocol/model";
import { Skeleton } from "../../widgets";
import { usePendingTurnEntries } from "./composer/queue/pendingTurnsStore";
import styles from "./session.module.css";
import { SYSTEM_PRELUDE_TURN_ID } from "./transcript/transcriptVisibility";

const TERMINAL_STATUSES = new Set(["cancelled", "canceled", "completed", "error", "failed", "interrupted"]);
const RUNNING_TURN_STATUSES = new Set(["active", "in_progress", "inprogress", "running", "started"]);

function isTerminalStatus(status: string): boolean {
  return TERMINAL_STATUSES.has(status.toLowerCase());
}

function realTurns(turns: readonly TurnModel[]): TurnModel[] {
  return turns.filter((turn) => turn.id !== SYSTEM_PRELUDE_TURN_ID);
}

function hasAuthoritativeFrame(turn: TurnModel): boolean {
  return turn.items.some((item) => item.type !== "userMessage" && item.type !== "systemMessage");
}

function shouldShowColdStart(model: ThreadModel, hasPendingSend: boolean): boolean {
  if (isTerminalStatus(model.status.type)) return false;

  const turns = realTurns(model.turns);
  if (turns.length === 0) return hasPendingSend;
  if (turns.length !== 1) return false;

  const [firstTurn] = turns;
  if (!firstTurn || hasAuthoritativeFrame(firstTurn) || isTerminalStatus(firstTurn.status)) return false;

  return (
    hasPendingSend ||
    model.activeTurnId === firstTurn.id ||
    model.status.type === "active" ||
    RUNNING_TURN_STATUSES.has(firstTurn.status.toLowerCase())
  );
}

export function useColdStartSkeleton(sessionRef: string, model: ThreadModel | null | undefined): boolean {
  const pendingSends = usePendingTurnEntries(sessionRef, "send");
  return model !== null && model !== undefined && shouldShowColdStart(model, pendingSends.length > 0);
}

export function ColdStartSkeleton() {
  return (
    <div className={styles.coldStart} data-testid="cold-start-skeleton">
      <Skeleton lines={3} />
    </div>
  );
}
