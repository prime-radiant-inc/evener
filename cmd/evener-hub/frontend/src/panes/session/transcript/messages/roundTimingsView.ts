// Reads a round_timings systemMessage item's structured detail (kata 7zkv):
// the raw wire numbers (agent/events/round_timings.go's RoundTimings struct,
// nanosecond ints, JSON-tagged `_ns`) attached under item.raw's
// "roundTimings" key by internal/appprojector's roundTimingsRaw. Narrowed
// from `unknown` the same defensive way turnUsageTokens narrows turn.usage
// (detailsAccounting.ts) - anything not shaped like the real wire payload (an
// older daemon predating this field, garbage) yields undefined so the caller
// falls back to the item's own prose text.
//
// Designed for the question someone turning Round timings ON is actually
// asking: which phase dominated a slow round, the model or the tools? A flat
// nanosecond-precision key=value dump technically contains that answer but
// does nothing to surface it - see SystemNoticeItem.tsx's RoundTimingsLine
// for the summary/detail split this produces.

const NS_PER_MS = 1_000_000;

export interface RoundTimingsPhase {
  label: string;
  ms: number;
  // 0-100, rounded to a whole percent of the round's total.
  pct: number;
}

export interface RoundTimingsSummary {
  round: number;
  totalMs: number;
  // The single largest phase - what answers "where did this round go".
  // Undefined only when every phase rounded under 1ms (phases is then empty
  // too), which cannot happen for a real measurement since loop overhead
  // always accounts for whatever the tracked phases don't.
  dominant?: RoundTimingsPhase;
  // Every phase whose rounded duration is at least 1ms, sorted descending.
  // A phase under 1ms carries no decision-relevant information at this
  // precision (kata 7zkv: "prompt=83ns ... cannot inform any decision") and
  // is dropped rather than misleadingly rounded up to a false "1ms".
  phases: RoundTimingsPhase[];
  // How many phases were dropped for rounding to under 1ms, so the detail
  // view can say "+ N phases under 1ms" instead of silently under-listing.
  omittedCount: number;
}

// PHASE_FIELDS mirrors appwire_projection.go's roundTimingsAnnouncement field
// order/naming (llm, tools, context, prompt, history, tool_defs, persistence,
// after_action, overhead) so a reader who has seen the old raw line
// recognizes the same names, just under real formatting.
const PHASE_FIELDS: ReadonlyArray<{ key: string; label: string }> = [
  { key: "llm_call_ns", label: "LLM" },
  { key: "tool_exec_ns", label: "Tools" },
  { key: "context_mgmt_ns", label: "Context" },
  { key: "system_prompt_ns", label: "Prompt" },
  { key: "history_expand_ns", label: "History" },
  { key: "tool_defs_ns", label: "Tool defs" },
  { key: "persistence_ns", label: "Persistence" },
  { key: "after_action_ns", label: "After action" },
  { key: "loop_overhead_ns", label: "Overhead" },
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export function roundTimingsSummary(raw: unknown): RoundTimingsSummary | undefined {
  if (!isRecord(raw)) return undefined;
  const rt = raw.roundTimings;
  if (!isRecord(rt)) return undefined;

  const round = rt.round;
  const totalNs = rt.total_round_ns;
  if (typeof round !== "number" || typeof totalNs !== "number" || totalNs < 0) return undefined;

  const phases: RoundTimingsPhase[] = [];
  let omittedCount = 0;
  for (const { key, label } of PHASE_FIELDS) {
    const ns = rt[key];
    if (typeof ns !== "number" || ns <= 0) continue;
    const ms = Math.round(ns / NS_PER_MS);
    if (ms < 1) {
      omittedCount++;
      continue;
    }
    const pct = totalNs > 0 ? Math.round((ns / totalNs) * 100) : 0;
    phases.push({ label, ms, pct });
  }
  phases.sort((a, b) => b.ms - a.ms);

  return { round, totalMs: totalNs / NS_PER_MS, dominant: phases[0], phases, omittedCount };
}
