// @vitest-environment node

import { expect, test } from "vitest";
import type { ActivityDelegate } from "./activityData";
import { parseActivityTree } from "./activityData";
import { delegateEndingText, delegateModel, delegatePacket, delegateTiming } from "./delegateDetails";
import { subagentOutcomesResponse } from "./testing/subagentWireFixtures";

function delegate(overrides: Partial<ActivityDelegate> = {}): ActivityDelegate {
  return {
    delegateId: "d",
    childSessionId: "s",
    childRef: "local:s",
    branch: {},
    ...overrides,
  };
}

test("uses live timing for a running delegate and falls back from invalid quiet anchor", () => {
  const result = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:00:00Z",
      latestActivityAt: "not-a-date",
      runningForMs: 10,
      quietForMs: 10,
    }),
    Date.parse("2026-09-07T00:01:00Z"),
  );

  expect(result).toEqual({
    startedAt: "2026-09-07T00:00:00Z",
    durationMs: 60_000,
    quietForMs: 60_000,
    durationLive: true,
    quietLive: true,
    terminal: false,
  });
});

test("freezes a terminal delegate at valid end-start or snapshot duration", () => {
  expect(
    delegateTiming(
      delegate({
        terminal: true,
        runStartedAt: "2026-09-07T00:00:00Z",
        runEndedAt: "2026-09-07T00:00:05Z",
        durationMs: 99_000,
        latestActivityAt: "2026-09-07T00:00:04Z",
      }),
      Date.parse("2026-09-07T01:00:00Z"),
    ),
  ).toMatchObject({
    durationMs: 5_000,
    durationLive: false,
    quietLive: false,
    terminal: true,
  });
  expect(delegateTiming(delegate({ terminal: true, runStartedAt: "bad", durationMs: 0 }), 1_000)).toMatchObject({
    durationMs: 0,
    durationLive: false,
    terminal: true,
  });
  expect(
    delegateTiming(
      delegate({
        terminal: true,
        runStartedAt: "2026-09-07T00:00:05Z",
        runEndedAt: "2026-09-07T00:00:00Z",
        durationMs: 7_000,
      }),
      Date.parse("2026-09-07T01:00:00Z"),
    ),
  ).toMatchObject({ durationMs: 7_000 });
});

test("does not infer terminal state from a stale outcome on a resumed run", () => {
  const result = delegateTiming(
    delegate({
      outcome: "completed",
      status: "running",
      runStartedAt: "2026-09-07T00:00:00Z",
      runEndedAt: "bad",
    }),
    Date.parse("2026-09-07T00:00:02Z"),
  );

  expect(result.terminal).toBe(false);
  expect(result.durationMs).toBe(2_000);
  expect(result.durationLive).toBe(true);
  expect(result.endedAt).toBeUndefined();
});

test("uses frozen timing values only when live anchors are unavailable", () => {
  const result = delegateTiming(delegate({ runningForMs: 0, quietForMs: Number.POSITIVE_INFINITY }), 1_000);

  expect(result).toMatchObject({
    durationMs: 0,
    durationLive: false,
    terminal: false,
  });
  expect(result.quietForMs).toBeUndefined();
  expect(result.quietLive).toBe(false);
});

test("falls back to safe frozen values for invalid clocks and rejects unsafe snapshots", () => {
  const result = delegateTiming(
    delegate({
      runningForMs: 5,
      quietForMs: 6,
      runStartedAt: "2026-09-07T00:00:00Z",
    }),
    Number.NaN,
  );

  expect(result).toMatchObject({
    durationMs: 5,
    quietForMs: 6,
    durationLive: false,
    quietLive: false,
  });
  expect(
    delegateTiming(delegate({ runningForMs: Number.MAX_SAFE_INTEGER + 1, quietForMs: -1 }), Number.POSITIVE_INFINITY),
  ).toMatchObject({ durationMs: undefined, quietForMs: undefined });
  const future = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:01:00Z",
      latestActivityAt: "2026-09-07T00:02:00Z",
    }),
    Date.parse("2026-09-07T00:00:00Z"),
  );
  expect(future).toMatchObject({
    durationMs: 0,
    quietForMs: 0,
    durationLive: true,
    quietLive: true,
  });
});

test("anchors quiet age at the newer valid activity or start timestamp", () => {
  const result = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:01:00Z",
      latestActivityAt: "2026-09-07T00:00:10Z",
    }),
    Date.parse("2026-09-07T00:02:00Z"),
  );

  expect(result.quietForMs).toBe(60_000);
});

test("does not infer a quiet age from runStartedAt alone", () => {
  const result = delegateTiming(delegate({ runStartedAt: "2026-09-07T00:00:00Z" }), Date.parse("2026-09-07T00:01:00Z"));

  expect(result.durationMs).toBe(60_000);
  expect(result.quietForMs).toBeUndefined();
  expect(result.quietLive).toBe(false);
});

test("selects the resolved model and exposes requested model only when distinct", () => {
  expect(
    delegateModel(
      delegate({
        resolvedModel: "resolved",
        model: "model",
        requestedModel: "requested",
        reasoningEffort: " high ",
      }),
    ),
  ).toEqual({
    model: "resolved",
    requestedModel: "requested",
    reasoning: " high ",
  });
  expect(delegateModel(delegate({ model: " ", requestedModel: "requested" }))).toEqual({ model: "requested" });
});

test("formats string packets as markdown and JSON values as pretty JSON", () => {
  expect(delegatePacket("")).toEqual({ text: "", format: "markdown" });
  expect(delegatePacket("null", true)).toEqual({
    text: '"null"',
    format: "json",
  });
  expect(delegatePacket(null)).toEqual({ text: "null", format: "json" });
  expect(delegatePacket(false)).toEqual({ text: "false", format: "json" });
  expect(delegatePacket(0)).toEqual({ text: "0", format: "json" });
  expect(delegatePacket({ ok: true })).toEqual({
    text: '{\n  "ok": true\n}',
    format: "json",
  });
  expect(delegatePacket(undefined)).toBeUndefined();
  const structured = delegatePacket({ nested: [1, false, null] }, true);
  expect(structured).toBeDefined();
  if (structured) expect(JSON.parse(structured.text)).toEqual({ nested: [1, false, null] });
});

test("omits packets that JSON cannot serialize", () => {
  const circular: Record<string, unknown> = {};
  circular.self = circular;
  expect(delegatePacket(circular)).toBeUndefined();
});

// How a subagent's run ended, in words (#3327): the hub's error when it sent
// one, else the reason code said plainly. A snake_case code never shows.
test("says a failed run's cause as the hub recorded it", () => {
  const tree = parseActivityTree(subagentOutcomesResponse().data);
  const failed = tree?.root.entries.find((entry) => entry.kind === "delegate" && entry.delegate.delegateId === "dlg_failed");
  if (failed?.kind !== "delegate") throw new Error("no failed delegate in the corpus");
  expect(failed.delegate.reason).toBe("failed");
  expect(delegateEndingText(failed.delegate)).toBe("provider returned 500");
});

test.each([
  ["failed", "failed", "failed"],
  ["failed", "run_error", "failed with an error"],
  ["failed", "ended_without_report", "ended without reporting"],
  ["failed", "terminal_error", "ended with an error"],
  ["failed", "missing_terminal", "ended without reporting"],
  ["failed", "runtime_lost", "runtime lost"],
  ["failed", "input_persist_failed", "couldn't save its input"],
  ["cancelled", "cancelled", "cancelled"],
  ["stopped", "stopped_by_parent", "stopped by its coordinator"],
  ["exhausted", "tool_round_budget_exhausted", "ran out of tool rounds"],
  ["exhausted", "turn_budget_exhausted", "ran out of turns"],
])("says %s's reason %s as %j", (outcome, reason, words) => {
  expect(delegateEndingText({ outcome, reason })).toBe(words);
});

test("never shows a code it doesn't know, but keeps a reason already in words", () => {
  expect(delegateEndingText({ outcome: "failed", reason: "quota_window_closed" })).toBe("failed");
  expect(delegateEndingText({ outcome: "stopped", reason: "parent_went_away" })).toBe("stopped");
  expect(delegateEndingText({ outcome: "failed", reason: "model refused the task" })).toBe("model refused the task");
});

test("says nothing for a run that ended well", () => {
  expect(delegateEndingText({ outcome: "completed" })).toBeUndefined();
  expect(delegateEndingText({})).toBeUndefined();
});
