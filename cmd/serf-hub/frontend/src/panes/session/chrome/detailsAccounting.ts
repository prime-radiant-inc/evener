// Pure derivations behind the session-details panel's accounting rows. Kept
// dependency-free of React (same convention as statusFormat.ts) so each is
// trivially unit-testable.

import type { ThreadModel, TurnModel } from "../../../protocol/model";

// TokenPair is one turn's or one session's up/down token counts, already
// resolved to real numbers.
export interface TokenPair {
  inputTokens: number;
  outputTokens: number;
}

// turnUsageTokens narrows one turn's usage to its up/down counts, or null when
// the turn carries no token data at all.
//
// TurnModel.usage is typed `unknown` (reducer.ts's wireToTurnModel passes the
// wire's Turn.usage straight through without re-typing it), so the narrowing
// lives here - shared with the transcript's own per-turn stamp (messages/
// turnMeta.ts) so both surfaces read a turn's tokens by exactly one rule. The
// real runtime shape is types.gen.ts's SerfUsage; anything else on the wire is
// treated as no data, and so is a pair of zeroes (Go's unset zero value, not a
// measurement of zero - the same rule SerfThread.Usage's own contract states).
export function turnUsageTokens(turn: TurnModel): TokenPair | null {
  const usage = turn.usage;
  if (typeof usage !== "object" || usage === null) return null;
  const { inputTokens, outputTokens } = usage as { inputTokens?: unknown; outputTokens?: unknown };
  const input = typeof inputTokens === "number" ? inputTokens : 0;
  const output = typeof outputTokens === "number" ? outputTokens : 0;
  if (input === 0 && output === 0) return null;
  return { inputTokens: input, outputTokens: output };
}

// SessionTokens carries a token total together with WHAT it counts. `scope`
// exists because the two available sources cover different amounts of the
// session and conflating them would print a partial figure under a
// full-session label:
//
//   "session" - the daemon's own cumulative total (SerfThread.Usage, from the
//     persisted SessionMeta.CumulativeUsage), or a sum over turns that are
//     provably the WHOLE transcript. Either way it accounts for the entire
//     session.
//   "loaded"  - a sum over only the turns this client holds. thread/read
//     windows turns via turnLimit and reports the truncation through
//     olderCursor, so once a cursor is present the earlier turns' tokens are
//     simply not in hand. The panel must label such a figure as covering the
//     loaded turns, never the session.
export interface SessionTokens {
  inputTokens: number;
  outputTokens: number;
  scope: "session" | "loaded";
}

// sessionTokens picks the most complete honest token total available.
//
// The thread-level cumulative usage is preferred: the daemon accumulated it
// across every turn, including ones no client ever loaded. It is absent for
// real sessions though - a fork child's meta is written by
// agent/fork.go's writeForkChild, which stamps no CumulativeUsage at all, and
// any session whose meta predates that field has none either. The transcript
// still records per-turn usage in both cases (it is what the per-turn stamps
// in the transcript render from), so summing the loaded turns recovers the
// figure the panel would otherwise have to omit.
//
// Returns null when there is no token data anywhere. A total of zero is
// treated as no data rather than a real "0 tokens": the wire's zero here is
// Go's unset zero value, and SerfThread.Usage's own contract is that absent
// token data must not render as "↑0 ↓0".
export function sessionTokens(model: ThreadModel): SessionTokens | null {
  const cumulative = model.usage;
  if (cumulative) {
    const inputTokens = cumulative.inputTokens ?? 0;
    const outputTokens = cumulative.outputTokens ?? 0;
    if (inputTokens > 0 || outputTokens > 0) return { inputTokens, outputTokens, scope: "session" };
  }

  let inputTokens = 0;
  let outputTokens = 0;
  for (const turn of model.turns) {
    const usage = turnUsageTokens(turn);
    if (!usage) continue;
    inputTokens += usage.inputTokens;
    outputTokens += usage.outputTokens;
  }
  if (inputTokens === 0 && outputTokens === 0) return null;
  // An olderCursor is thread/read's own signal that it truncated the window,
  // so the turns in hand are a suffix of the transcript, not all of it.
  return { inputTokens, outputTokens, scope: model.olderCursor ? "loaded" : "session" };
}

// formatTimestamp renders an instant in the reader's own locale and timezone,
// which is what someone reading "when did this session start" wants. An
// unparseable instant reports absent rather than letting the platform's
// "Invalid Date" string reach the panel.
export function formatTimestamp(iso: string | undefined): string | undefined {
  if (!iso) return undefined;
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return undefined;
  return new Date(ms).toLocaleString();
}
