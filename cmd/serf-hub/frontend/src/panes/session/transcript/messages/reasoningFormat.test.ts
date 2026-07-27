// @vitest-environment node
import { expect, test } from "vitest";
import {
  formatThoughtDuration,
  joinedReasoningParagraphs,
  lastMeaningfulThoughtLine,
  thoughtDurationMs,
} from "./reasoningFormat";

// --- joinedReasoningParagraphs ---------------------------------------------
// reasoningSummaries is string[][] - per summaryIndex chunk lists
// (protocol/model.ts). Each index joins to one paragraph; empty (or
// all-whitespace) paragraphs are dropped, matching the wave-4 scope's
// "empty thoughts removed".

test("undefined summaries yields no paragraphs", () => {
  expect(joinedReasoningParagraphs(undefined)).toEqual([]);
});

test("an empty summaries array yields no paragraphs", () => {
  expect(joinedReasoningParagraphs([])).toEqual([]);
});

test("joins each summaryIndex's chunks into one paragraph", () => {
  expect(
    joinedReasoningParagraphs([
      ["Hel", "lo "],
      ["wor", "ld"],
    ]),
  ).toEqual(["Hello ", "world"]);
});

test("drops a paragraph whose chunks join to an empty string", () => {
  expect(joinedReasoningParagraphs([["real content"], [], ["more"]])).toEqual(["real content", "more"]);
});

test("drops a paragraph whose chunks join to all-whitespace", () => {
  expect(joinedReasoningParagraphs([["real"], ["  ", "\n"]])).toEqual(["real"]);
});

test("all paragraphs empty yields an empty array (the whole thought is empty)", () => {
  expect(joinedReasoningParagraphs([[], ["   "]])).toEqual([]);
});

// --- thoughtDurationMs -------------------------------------------------------
// Computes from REAL timestamps only - never a client wall clock. The wire
// pair (ItemModel.startedAt/completedAt) wins when present; otherwise falls
// back to the client-observed arrival pair (ItemModel.observedStartedAt/
// observedCompletedAt, stamped by the reducer - see model.ts's own comment).
// Both absent (hydrated/historical items) yields no duration.

test("undefined startedAt/completedAt yields no duration", () => {
  expect(thoughtDurationMs(undefined, undefined)).toBeUndefined();
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", undefined)).toBeUndefined();
  expect(thoughtDurationMs(undefined, "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("an unparseable timestamp yields no duration rather than a garbage number", () => {
  expect(thoughtDurationMs("not a date", "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("preserves actual elapsed milliseconds from two real ISO timestamps", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.400Z")).toBe(4_400);
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.600Z")).toBe(4_600);
});

test("a sub-second elapsed span stays sub-second", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.200Z")).toBe(200);
});

test("neither wire nor observed pair present yields no duration", () => {
  expect(thoughtDurationMs(undefined, undefined, undefined, undefined)).toBeUndefined();
});

test("falls back to the observed pair when the wire pair is absent", () => {
  expect(thoughtDurationMs(undefined, undefined, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.400Z")).toBe(4_400);
});

test("the wire pair wins when both the wire and observed pairs are present", () => {
  expect(
    thoughtDurationMs(
      "2026-01-01T00:00:00.000Z",
      "2026-01-01T00:00:10.000Z",
      "2026-01-01T00:00:00.000Z",
      "2026-01-01T00:00:02.000Z",
    ),
  ).toBe(10_000);
});

test("a backwards timestamp pair is unavailable rather than a fabricated positive duration", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:02.000Z", "2026-01-01T00:00:01.000Z")).toBeUndefined();
});

test("the exact timestamp boundary remains an honest zero", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:01.000Z", "2026-01-01T00:00:01.000Z")).toBe(0);
});

test("formatThoughtDuration uses milliseconds and sensible second tiers", () => {
  expect(formatThoughtDuration(0)).toBe("0ms");
  expect(formatThoughtDuration(250)).toBe("250ms");
  expect(formatThoughtDuration(1_500)).toBe("1.5s");
  expect(formatThoughtDuration(10_000)).toBe("10s");
});

test("lastMeaningfulThoughtLine chooses the final nonblank line and trims it", () => {
  expect(lastMeaningfulThoughtLine(["first line\nsecond line\n  ", "\nthird line"], 80)).toBe("third line");
});

test("lastMeaningfulThoughtLine clips only when the final line exceeds the bound", () => {
  expect(lastMeaningfulThoughtLine(["first", "a very long final line"], 11)).toBe("a very long…");
  expect(lastMeaningfulThoughtLine(["first", "short"], 11)).toBe("short");
});

test("lastMeaningfulThoughtLine removes only common Markdown decoration from the plain preview", () => {
  expect(lastMeaningfulThoughtLine(["## plan", "- **ship** `it`"], 80)).toBe("ship it");
});
