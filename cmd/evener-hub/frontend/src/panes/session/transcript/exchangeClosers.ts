// Exchange-close detection for the terminal-message treatment (option B:
// the final agent prose concluding an exchange gets the neutral wash). An
// EXCHANGE is one thing the user asked plus everything the agent did about
// it; the closer fires on the LAST text-bearing agentMessage item before the
// next userMessage or the transcript end. Mirrors exchangeOpeners.ts, which
// fires on the FIRST agentMessage after each userMessage. Computed once per
// transcript model at the Session level for the same reason: a TurnBlock
// renders one turn in isolation and cannot see this relation across turns.
import type { TurnModel } from "../../../protocol/model";

export function exchangeClosersFor(turns: TurnModel[]): ReadonlySet<string> {
  const closers = new Set<string>();
  let pending: string | undefined;
  let inExchange = false;

  const flush = () => {
    if (pending !== undefined) closers.add(pending);
    pending = undefined;
    inExchange = false;
  };

  for (const turn of turns) {
    // A failed turn closes with the TurnFailureEndCap diagnostic, never with
    // the prose wash: any prose in it belongs to broken work, and the cap is
    // already the turn's visual close. Its userMessage still ends the
    // previous exchange, but its agent prose never becomes a closer. The
    // check is deliberately broader than asTurnError's message-string narrow:
    // any recorded turn error suppresses the wash.
    const failed = turn.error != null;
    for (const item of turn.items) {
      if (item.type === "userMessage") {
        // A new exchange opens: the previous exchange (if any) closes on
        // whatever prose it last produced. Queued user messages before any
        // reply just re-arm; nothing closes until prose exists.
        flush();
        inExchange = true;
      } else if (item.type === "agentMessage" && inExchange && !failed) {
        if (item.text) pending = item.id;
      }
    }
  }
  flush();

  return closers;
}
