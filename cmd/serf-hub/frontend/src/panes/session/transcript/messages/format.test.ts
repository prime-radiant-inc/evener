import { test, expect } from "vitest";
import { imagePlaceholder, formatTokenCount, formatDurationMs, firstLine } from "./format";

// --- imagePlaceholder ----------------------------------------------------
// Parity: renderer-format.js:41-45 (imagePlaceholderForCount). Wave-4 T2 is
// deliberately text-only here - real thumbnails are T4's job (transcript/
// flow/** media work); this is the honest placeholder line in the meantime.

test("imagePlaceholder: zero images renders nothing", () => {
  expect(imagePlaceholder(0)).toBe("");
});

test("imagePlaceholder: a negative count also renders nothing (defensive)", () => {
  expect(imagePlaceholder(-1)).toBe("");
});

test("imagePlaceholder: exactly one image", () => {
  expect(imagePlaceholder(1)).toBe("[image]");
});

test("imagePlaceholder: more than one image shows the count", () => {
  expect(imagePlaceholder(2)).toBe("[2 images]");
  expect(imagePlaceholder(5)).toBe("[5 images]");
});

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
