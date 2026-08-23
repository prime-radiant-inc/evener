// @vitest-environment node
import { expect, test } from "vitest";
import type { TurnModel } from "../../../../protocol/model";
import { turnMetaParts } from "./turnMeta";

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

test("tokens render as up/down arrows using formatTokenCount, from a EvenerUsage-shaped usage value", () => {
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

test("a non-EvenerUsage usage value (defensively narrowed at runtime) yields no tokens segment", () => {
  // TurnModel.usage is typed EvenerUsage | undefined, but the wire could
  // carry unexpected shapes from old daemons or bridged sessions. The
  // narrowing in turnUsageTokens is defensive at runtime regardless of the
  // static type, so cast through unknown to exercise that path.
  expect(
    turnMetaParts(turn({ usage: "garbage" } as unknown as undefined) as unknown as TurnModel).tokens,
  ).toBeUndefined();
  expect(turnMetaParts(turn({ usage: null } as unknown as undefined) as unknown as TurnModel).tokens).toBeUndefined();
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
