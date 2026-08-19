// Pure logic backing SessionActionsMenu's "Fork" action. A session-chrome
// menu item has no specific transcript message as its context (unlike the
// legacy per-message fork-into-composer, issue #42), so it forks from the
// most recent turn that actually carries a userMessage item.
import type { ThreadModel } from "../../../protocol/model";

export interface LastUserMessage {
  turnId: string;
  text: string;
}

// Scans turns backward (not just turns[length-1]) because the very last
// turn may not be user-initiated at all - e.g. a goal-continuation turn
// settles via item/completed with no userMessage item of its own
// (protocol/reducer.ts's own EventGoalContinuation note). Within the
// winning turn, takes the FIRST userMessage item (the turn's own opening
// message), not the last.
export function lastUserMessageText(model: ThreadModel): LastUserMessage | undefined {
  for (let i = model.turns.length - 1; i >= 0; i--) {
    const turn = model.turns[i];
    if (!turn) continue;
    const item = turn.items.find((it) => it.type === "userMessage" && it.text);
    if (item) return { turnId: turn.id, text: item.text };
  }
  return undefined;
}
