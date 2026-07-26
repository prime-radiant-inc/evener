import { expect, test } from "vitest";
import { toolCallDuration } from "./toolMeta";

test("formats a sub-second duration in ms", () => {
  expect(toolCallDuration({ startedAt: "2026-01-01T00:00:00.000Z", completedAt: "2026-01-01T00:00:00.038Z" })).toBe(
    "38ms",
  );
});

test("formats a multi-second duration in s", () => {
  expect(toolCallDuration({ startedAt: "2026-01-01T00:00:00.000Z", completedAt: "2026-01-01T00:00:11.000Z" })).toBe(
    "11s",
  );
});

test("missing startedAt yields undefined (a call still in flight or a source that never stamped it)", () => {
  expect(toolCallDuration({ completedAt: "2026-01-01T00:00:00.038Z" })).toBeUndefined();
});

test("missing completedAt yields undefined", () => {
  expect(toolCallDuration({ startedAt: "2026-01-01T00:00:00.000Z" })).toBeUndefined();
});

test("both absent yields undefined", () => {
  expect(toolCallDuration({})).toBeUndefined();
});

test("a completedAt before startedAt (clock skew / bad data) yields undefined rather than a negative duration", () => {
  expect(toolCallDuration({ startedAt: "2026-01-01T00:00:01.000Z", completedAt: "2026-01-01T00:00:00.000Z" })).toBe(
    undefined,
  );
});

test("an instantaneous call (equal startedAt/completedAt) is a real 0ms duration, not absence - shows the floored 1ms", () => {
  expect(toolCallDuration({ startedAt: "2026-01-01T00:00:00.000Z", completedAt: "2026-01-01T00:00:00.000Z" })).toBe(
    "1ms",
  );
});

test("an unparseable timestamp yields undefined rather than NaN math", () => {
  expect(toolCallDuration({ startedAt: "not-a-date", completedAt: "2026-01-01T00:00:00.038Z" })).toBeUndefined();
});
