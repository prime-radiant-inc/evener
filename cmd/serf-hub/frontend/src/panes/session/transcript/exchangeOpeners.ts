// Exchange-open detection for the speaker-eyebrow treatment (tiered-density
// spec, 2026-07-27). An EXCHANGE is one thing the user asked plus everything
// the agent did about it; the eyebrow fires on the first agentMessage item
// after each userMessage, scanning turns in wire order. Computed once per
// transcript model at the Session level - a TurnBlock renders one turn in
// isolation and cannot see this relation across turn boundaries.
import type { TurnModel } from "../../../protocol/model";

export function exchangeOpenersFor(turns: TurnModel[]): ReadonlySet<string> {
  const openers = new Set<string>();
  let awaitingAgent = false;

  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.type === "userMessage") {
        // Queued/steered user messages before any reply do not each open an
        // exchange - the first agent reply still owns the one opener slot.
        awaitingAgent = true;
      } else if (item.type === "agentMessage") {
        if (awaitingAgent) openers.add(item.id);
        awaitingAgent = false;
      }
    }
  }

  return openers;
}
