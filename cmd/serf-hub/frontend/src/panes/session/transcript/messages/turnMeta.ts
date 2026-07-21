// Extracts a TurnSeparator's displayable parts from a TurnModel.
//
// TurnModel.usage/cost are typed `unknown` in protocol/model.ts (reducer.ts
// passes the wire's Turn.usage/Turn.cost straight through without
// re-typing them - see wireToTurnModel). protocol/ is T1-owned and out of
// this stream's scope to edit, so this file narrows them itself rather than
// widening the shared model. Their REAL runtime shape, per
// protocol/types.gen.ts's own Turn interface, is `usage?: SerfUsage`
// ({inputTokens?, outputTokens?, cacheReadTokens?, totalTokens?}) and
// `cost?: string` (already formatted server-side, e.g. "$0.0234") - this
// file's local guards target exactly that shape.
import type { TurnModel } from "../../../../protocol/model";
import { formatDurationMs, formatTokenCount } from "./format";

export interface TurnMetaParts {
  duration?: string;
  tokens?: string;
  cost?: string;
}

interface SerfUsageLike {
  inputTokens?: unknown;
  outputTokens?: unknown;
}

function isUsageLike(value: unknown): value is SerfUsageLike {
  return typeof value === "object" && value !== null;
}

function numberField(value: unknown): number {
  return typeof value === "number" ? value : 0;
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

  if (isUsageLike(turn.usage)) {
    const input = numberField(turn.usage.inputTokens);
    const output = numberField(turn.usage.outputTokens);
    if (input || output) parts.tokens = `↑${formatTokenCount(input)} ↓${formatTokenCount(output)}`;
  }

  if (typeof turn.cost === "string" && turn.cost !== "") {
    parts.cost = turn.cost;
  }

  return parts;
}
