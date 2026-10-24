import type { ThreadModel } from "./model";
import { isTurnActive } from "./submitRouting";

/** The new text message submitted by a failed turn's Retry action. */
export const TURN_CONTINUATION_TEXT = "continue";

// The canonical owner's identity and live state, never a viewport's rows or a
// last-visible-history heuristic. `thread.turns` is the display structure the
// reducer derives from history plus overlay, so an older page being the last
// loaded row cannot make a historical failure look like the tail.
type RetryTurnThread = Pick<ThreadModel, "turns" | "runningTurnId" | "history" | "status">;

/** The conversation's canonical last display turn, by identity only. */
export function latestTurnId(model: { turns: readonly { id: string }[] } | undefined): string | undefined {
  return model?.turns[model.turns.length - 1]?.id;
}

// Whether `failedTurnId` is the conversation's live tail, which is the shared
// identity half of failed-turn Retry. This is NOT send admission: the caller
// still establishes that the row is that turn's own failure, applies its
// capabilities, loads pending readiness, and rechecks at activation.
//
// - Invalidated or failed history cannot establish the live tail until the
//   existing owner refreshes it. Absent history carries no invalidation
//   signal, so the canonical turns and live state are used instead.
// - A running identity is a veto: a different one prevents Retry even when
//   its turn has produced no display items yet. It is never permission to
//   bypass the latest-turn test below; the source failure must still be the
//   last canonical display turn.
// - Active status without a running identity is not coherent live state, so
//   it cannot authorize Retry.
// - Otherwise the last canonical turn is the tail. Ids are compared by
//   identity only, never lexically or by inferred order.
export function isLatestRetryTurn(thread: RetryTurnThread, failedTurnId: string): boolean {
  const history = thread.history;
  if (history?.invalidatedAtGeneration !== undefined || history?.failed !== undefined) return false;

  const runningTurnId = thread.runningTurnId;
  if (runningTurnId !== undefined && runningTurnId !== failedTurnId) return false;
  if (runningTurnId === undefined && isTurnActive(thread.status.type)) return false;

  return latestTurnId(thread) === failedTurnId;
}
