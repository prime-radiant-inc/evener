// Extracts a TurnSeparator's displayable parts from a TurnModel.
//
// TurnModel.usage/cost are typed `unknown` in protocol/model.ts (reducer.ts
// passes the wire's Turn.usage/Turn.cost straight through without re-typing
// them - see wireToTurnModel), so both are narrowed at the point of use. Their
// REAL runtime shape, per protocol/types.gen.ts's own Turn interface, is
// `usage?: SerfUsage` ({inputTokens?, outputTokens?, cacheReadTokens?,
// totalTokens?}) and `cost?: string` (already formatted server-side, e.g.
// "$0.0234"). turnUsageTokens (chrome/detailsAccounting.ts) owns the usage
// narrowing, shared with the session-details panel's own token derivation so
// the two surfaces read one turn's tokens by exactly one rule.
import type { TurnModel } from "../../../../protocol/model";
import { turnUsageTokens } from "../../chrome/detailsAccounting";
import { formatDurationMs, formatTokenCount } from "./format";

export interface TurnMetaParts {
  duration?: string;
  tokens?: string;
  cost?: string;
}

// turnMetaParts mirrors legacy's own turnMetaParts (renderer-format.js:666-675):
// each of duration/tokens/cost is present only when the turn actually
// carries real data for it - fields may be absent (a turn still in
// progress, or a source that never reports cost), and this never fabricates
// a placeholder for a missing one.
export function turnMetaParts(turn: TurnModel): TurnMetaParts {
  const parts: TurnMetaParts = {};

  if (typeof turn.durationMs === "number") {
    parts.duration = formatDurationMs(turn.durationMs);
  }

  const usage = turnUsageTokens(turn);
  if (usage) {
    parts.tokens = `↑${formatTokenCount(usage.inputTokens)} ↓${formatTokenCount(usage.outputTokens)}`;
  }

  if (typeof turn.cost === "string" && turn.cost !== "") {
    parts.cost = turn.cost;
  }

  return parts;
}
