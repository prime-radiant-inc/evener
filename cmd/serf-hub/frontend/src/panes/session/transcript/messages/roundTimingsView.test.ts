// @vitest-environment node
import { expect, test } from "vitest";
import { roundTimingsSummary } from "./roundTimingsView";

function raw(fields: Record<string, number> = {}) {
  return { roundTimings: { round: 0, total_round_ns: 6_411_312_958, ...fields } };
}

test("undefined raw yields undefined (no round_timings detail attached)", () => {
  expect(roundTimingsSummary(undefined)).toBeUndefined();
});

test("a non-object raw yields undefined (defensive narrowing)", () => {
  expect(roundTimingsSummary("garbage")).toBeUndefined();
  expect(roundTimingsSummary(null)).toBeUndefined();
  expect(roundTimingsSummary(42)).toBeUndefined();
});

test("raw with no roundTimings key yields undefined", () => {
  expect(roundTimingsSummary({ compaction: {} })).toBeUndefined();
});

test("a roundTimings value missing round or total_round_ns yields undefined", () => {
  expect(roundTimingsSummary({ roundTimings: { total_round_ns: 100 } })).toBeUndefined();
  expect(roundTimingsSummary({ roundTimings: { round: 0 } })).toBeUndefined();
});

test("the kata's own example: llm dominates, sub-ms fields are dropped rather than rounded up", () => {
  // Round 0 total=6.411312958s llm=4.935822084s context=8.625µs
  // tools=1.462408667s prompt=83ns history=12.5µs tool_defs=0s
  // persistence=8.742792ms after_action=500ns overhead=4.317707ms
  const summary = roundTimingsSummary(
    raw({
      llm_call_ns: 4_935_822_084,
      context_mgmt_ns: 8_625,
      tool_exec_ns: 1_462_408_667,
      system_prompt_ns: 83,
      history_expand_ns: 12_500,
      tool_defs_ns: 0,
      persistence_ns: 8_742_792,
      after_action_ns: 500,
      loop_overhead_ns: 4_317_707,
    }),
  );
  expect(summary).toBeDefined();
  expect(summary?.round).toBe(0);
  expect(summary?.totalMs).toBeCloseTo(6411.312958, 3);
  // Only llm, tools, persistence, overhead round to >= 1ms.
  expect(summary?.phases.map((p) => p.label)).toEqual(["LLM", "Tools", "Persistence", "Overhead"]);
  expect(summary?.dominant?.label).toBe("LLM");
  expect(summary?.dominant?.ms).toBe(4936);
  // context (8.625µs), prompt (83ns), history (12.5µs), after_action (500ns)
  // all round under 1ms and are counted as omitted, not silently vanished.
  expect(summary?.omittedCount).toBe(4);
});

test("phases are sorted descending by rounded duration, not field order", () => {
  const summary = roundTimingsSummary(raw({ tool_exec_ns: 40_000_000, llm_call_ns: 10_000_000 }));
  expect(summary?.phases.map((p) => p.label)).toEqual(["Tools", "LLM"]);
});

test("percent is rounded from the raw ns fraction of the total, not from already-rounded ms", () => {
  // total 3.333ms (rounds to 3ms), llm 1.666ms (rounds to 2ms). The precise
  // ns fraction is ~50%; computing from the rounded ms (2/3) would give 67%.
  const summary = roundTimingsSummary(raw({ total_round_ns: 3_333_000, llm_call_ns: 1_666_000 }));
  expect(summary?.phases[0]?.ms).toBe(2);
  expect(summary?.phases[0]?.pct).toBe(50);
});

test("a phase whose share rounds to 0% still appears if its own duration rounds to >= 1ms", () => {
  const summary = roundTimingsSummary(raw({ total_round_ns: 10_000_000_000, tool_exec_ns: 1_000_000 }));
  const phase = summary?.phases[0];
  expect(phase?.label).toBe("Tools");
  expect(phase?.ms).toBe(1);
  expect(phase?.pct).toBe(0);
});

test("no phase reaching 1ms yields an empty phases array and no dominant, total still present", () => {
  const summary = roundTimingsSummary(raw({ total_round_ns: 500, llm_call_ns: 400 }));
  expect(summary?.phases).toEqual([]);
  expect(summary?.dominant).toBeUndefined();
  expect(summary?.omittedCount).toBe(1);
});

test("a zero or negative phase value is skipped, not shown as a 0ms row", () => {
  const summary = roundTimingsSummary(raw({ llm_call_ns: 0, tool_exec_ns: 5_000_000 }));
  expect(summary?.phases.map((p) => p.label)).toEqual(["Tools"]);
  expect(summary?.omittedCount).toBe(0);
});
