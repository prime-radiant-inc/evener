// @vitest-environment node
import { expect, test } from "vitest";
import { firstLine, formatClockTime, formatDurationMs, formatTokenCount } from "./format";

// --- formatTokenCount -----------------------------------------------------
// Parity: renderer-format.js:582-587. Below 1000 is a plain rounded
// integer; at/above 1000 it's round(n/1000)+"k" with no further scaling -
// 1,000,000 reads "1000k", never "1M".

test("formatTokenCount: renders small counts as plain integers", () => {
  expect(formatTokenCount(0)).toBe("0");
  expect(formatTokenCount(1)).toBe("1");
  expect(formatTokenCount(999)).toBe("999");
});

test("formatTokenCount: rounds a fractional count under 1000", () => {
  expect(formatTokenCount(12.6)).toBe("13");
});

test("formatTokenCount: 1000 and above uses the k suffix, rounded, no decimal", () => {
  expect(formatTokenCount(1000)).toBe("1k");
  expect(formatTokenCount(1499)).toBe("1k");
  expect(formatTokenCount(1500)).toBe("2k");
  expect(formatTokenCount(12345)).toBe("12k");
});

test("formatTokenCount: never scales past k, even for very large counts", () => {
  expect(formatTokenCount(1_000_000)).toBe("1000k");
});

test("formatTokenCount: a negative count clamps to 0", () => {
  expect(formatTokenCount(-5)).toBe("0");
});

test("formatTokenCount: a non-finite count clamps to 0", () => {
  expect(formatTokenCount(NaN)).toBe("0");
  expect(formatTokenCount(Infinity)).toBe("0");
});

// --- formatDurationMs -------------------------------------------------------
// Parity: renderer-format.js:636 (formatToolDuration) - same tier
// breakpoints/rounding as the terminal-footer variant, but floored at 1ms so
// nothing ever prints the dishonest "0ms". Used for turn separators, whose
// duration is real server-measured time (never fabricated client-side).

test("formatDurationMs: sub-1000ms renders whole milliseconds", () => {
  expect(formatDurationMs(1)).toBe("1ms");
  expect(formatDurationMs(250)).toBe("250ms");
  expect(formatDurationMs(999)).toBe("999ms");
});

test("formatDurationMs: floors at 1ms - a sub-millisecond duration never prints 0ms", () => {
  expect(formatDurationMs(0)).toBe("1ms");
  expect(formatDurationMs(0.2)).toBe("1ms");
});

test("formatDurationMs: 1000-9999ms renders one decimal of seconds, trailing .0 stripped", () => {
  expect(formatDurationMs(1000)).toBe("1s");
  expect(formatDurationMs(1500)).toBe("1.5s");
  expect(formatDurationMs(9999)).toBe("10s"); // (9999/1000).toFixed(1) rounds to "10.0"
});

test("formatDurationMs: 10000ms and above renders whole seconds, no decimal", () => {
  expect(formatDurationMs(10000)).toBe("10s");
  expect(formatDurationMs(65432)).toBe("65s");
});

// --- firstLine --------------------------------------------------------------
// A general-purpose "first non-blank line, clipped" helper: used for the
// think-block settled preview and the system-notice group's "first event"
// mention. Deliberately simpler than legacy's reasoningGist (no filler-word
// stripping, no last-sentence preference) - the wave-4 scope calls for a
// "first-line preview", not a reproduction of that specific legacy
// heuristic.

test("firstLine: returns the only line untouched when short", () => {
  expect(firstLine("hello world", 80)).toBe("hello world");
});

test("firstLine: takes the first line of a multi-line string", () => {
  expect(firstLine("first line\nsecond line", 80)).toBe("first line");
});

test("firstLine: skips leading blank lines", () => {
  expect(firstLine("\n\n  \nactual content", 80)).toBe("actual content");
});

test("firstLine: trims surrounding whitespace on the chosen line", () => {
  expect(firstLine("   padded text   \nmore", 80)).toBe("padded text");
});

test("firstLine: an all-blank string yields an empty string", () => {
  expect(firstLine("   \n  \n", 80)).toBe("");
});

test("firstLine: clips a long line and appends an ellipsis", () => {
  const long = "a".repeat(100);
  const result = firstLine(long, 10);
  expect(result).toBe("aaaaaaaaaa…");
});

test("firstLine: a line exactly at the max length is not clipped", () => {
  const exact = "a".repeat(10);
  expect(firstLine(exact, 10)).toBe(exact);
});

// --- formatClockTime --------------------------------------------------------
// The slack-lean speaker header's time slot: local "HH:MM" from an
// ItemModel.startedAt ISO timestamp. Expected values are computed through the
// same Date parsing the helper uses, never hardcoded, so the suite is
// timezone-independent (CI and dev machines differ).

test("formatClockTime: renders a valid ISO timestamp as local HH:MM", () => {
  const iso = "2026-07-29T12:41:00";
  const parsed = new Date(iso);
  const expected = `${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}`;
  expect(formatClockTime(iso)).toBe(expected);
});

test("formatClockTime: a Zulu ISO with seconds projects to the correct local hour and minute", () => {
  const iso = "2026-07-29T04:41:37Z";
  const parsed = new Date(iso);
  const expected = `${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}`;
  expect(formatClockTime(iso)).toBe(expected);
});

test("formatClockTime: zero-pads single-digit hours and minutes", () => {
  const iso = "2026-07-29T01:05:00Z";
  expect(formatClockTime(iso)).toMatch(/^\d{2}:\d{2}$/);
});

test("formatClockTime: undefined input yields undefined, so a header with no time shows no time", () => {
  expect(formatClockTime(undefined)).toBeUndefined();
});

test("formatClockTime: an unparseable string yields undefined rather than a guess", () => {
  expect(formatClockTime("not a timestamp")).toBeUndefined();
  expect(formatClockTime("")).toBeUndefined();
});
