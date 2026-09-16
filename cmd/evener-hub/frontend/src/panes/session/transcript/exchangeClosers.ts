// Exchange-close detection for the terminal-message treatment (option B:
// the final agent prose concluding an exchange gets the neutral wash). An
// EXCHANGE is one thing the user asked plus everything the agent did about
// it; the closer fires on the LAST text-bearing agentMessage item before the
// next userMessage or the transcript end. Mirrors exchangeOpeners.ts, which
// fires on the FIRST agentMessage after each userMessage. Computed once per
// transcript model at the Session level for the same reason: a TurnBlock
// renders one turn in isolation and cannot see this relation across turns.
import type { TurnModel } from "@evener/appwire-client";

export function exchangeClosersFor(turns: TurnModel[]): ReadonlySet<string> {
  const closers = new Set<string>();
  let pending: string | undefined;
  let inExchange = false;
  // Any in-progress item anywhere in the exchange suppresses its close: the
  // wash means concluded work, so a streaming tail, a running tool, or a
  // live turn later in the exchange all hold it off until everything
  // settles. Reset per exchange like pending.
  let exchangeLive = false;
  // A failed or interrupted turn poisons its whole exchange: the exchange
  // ended abnormally, so even earlier settled prose must not wear the
  // concluded-work wash. Reset only when the next userMessage opens a new
  // exchange (roborev PR 1260).
  let exchangeFailed = false;

  const flush = () => {
    if (pending !== undefined && !exchangeLive && !exchangeFailed) closers.add(pending);
    pending = undefined;
    inExchange = false;
    exchangeLive = false;
    exchangeFailed = false;
  };

  for (const turn of turns) {
    // A failed turn closes with the TurnFailureEndCap diagnostic, never with
    // the prose wash: any prose in it belongs to broken work, and the cap is
    // already the turn's visual close. Its userMessage still ends the
    // previous exchange, but its agent prose never becomes a closer. The
    // error check is deliberately broader than asTurnError's message-string
    // narrow; the status check mirrors the projector's own isTerminalTurn
    // (projector.ts): an interrupted turn carries no error object, but its
    // partial prose was cut off, not concluded.
    const failed = turn.error != null || turn.status === "failed" || turn.status === "interrupted";
    for (const item of turn.items) {
      if (item.status === "inProgress" && inExchange) exchangeLive = true;
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
    // A live turn suppresses the close even when none of its items is
    // individually in-progress: between item/completed and the next
    // item/started the turn is still working, and closing there would paint
    // the wash only to clear it on the next item (roborev PR 1260).
    // Evaluated after the item scan so a turn that OPENS the exchange
    // mid-turn still counts as live work.
    if (turn.status === "inProgress" && inExchange) exchangeLive = true;
    // A failed or interrupted turn poisons the exchange it belongs to -
    // evaluated after the scan for the same mid-turn-open reason. A failed
    // turn before any exchange opens nothing, so it poisons nothing.
    if (failed && inExchange) exchangeFailed = true;
  }
  flush();

  return closers;
}
