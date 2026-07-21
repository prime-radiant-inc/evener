import { test, expect } from "vitest";
import { turnMetaParts } from "./turnMeta";
import type { TurnModel } from "../../../../protocol/model";

function turn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "completed", items: [], ...overrides };
}

test("a turn with none of duration/usage/cost yields an all-empty parts object", () => {
  expect(turnMetaParts(turn())).toEqual({});
});

test("duration is formatted via formatDurationMs when durationMs is a number", () => {
  expect(turnMetaParts(turn({ durationMs: 1500 }))).toEqual({ duration: "1.5s" });
});

test("durationMs of exactly 0 is still shown (a real, server-reported zero) - `?? handling` is about ABSENCE, not falsy", () => {
  expect(turnMetaParts(turn({ durationMs: 0 }))).toEqual({ duration: "1ms" });
});

test("tokens render as up/down arrows using formatTokenCount, from a SerfUsage-shaped usage value", () => {
  const parts = turnMetaParts(turn({ usage: { inputTokens: 1200, outputTokens: 340 } }));
  expect(parts.tokens).toBe("↑1k ↓340");
});

test("usage with only one of inputTokens/outputTokens present treats the other as 0", () => {
  expect(turnMetaParts(turn({ usage: { outputTokens: 50 } })).tokens).toBe("↑0 ↓50");
});

test("usage present but both token counts are zero/absent yields no tokens segment", () => {
  expect(turnMetaParts(turn({ usage: {} })).tokens).toBeUndefined();
  expect(turnMetaParts(turn({ usage: { inputTokens: 0, outputTokens: 0 } })).tokens).toBeUndefined();
});

test("a non-object usage value (defensively narrowed) yields no tokens segment", () => {
  expect(turnMetaParts(turn({ usage: "garbage" })).tokens).toBeUndefined();
  expect(turnMetaParts(turn({ usage: null })).tokens).toBeUndefined();
});

test("cost passes through verbatim when it's a non-empty string", () => {
  expect(turnMetaParts(turn({ cost: "$0.0234" })).cost).toBe("$0.0234");
});

test("an empty-string cost yields no cost segment", () => {
  expect(turnMetaParts(turn({ cost: "" })).cost).toBeUndefined();
});

test("a non-string cost value (defensively narrowed) yields no cost segment", () => {
  expect(turnMetaParts(turn({ cost: 42 })).cost).toBeUndefined();
});

test("all three segments combine when all three are present", () => {
  const parts = turnMetaParts(
    turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" }),
  );
  expect(parts).toEqual({ duration: "65s", tokens: "↑500 ↓120", cost: "$1.00" });
});
