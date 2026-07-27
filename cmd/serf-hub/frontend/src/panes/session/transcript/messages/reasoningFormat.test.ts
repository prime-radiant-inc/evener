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

test("rejects impossible calendar timestamps while accepting leap days and offsets", () => {
  expect(thoughtDurationMs("2026-02-30T00:00:00.000Z", "2026-03-03T00:00:00.000Z")).toBeUndefined();
  expect(thoughtDurationMs("2026-13-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")).toBeUndefined();
  expect(thoughtDurationMs("2024-02-29T00:00:00.000Z", "2024-03-01T00:00:00.000Z")).toBe(86_400_000);
  expect(thoughtDurationMs("2026-01-01T00:00:00.000-05:00", "2026-01-01T01:00:00.000-05:00")).toBe(3_600_000);
  expect(thoughtDurationMs("2026-01-01T00:00:00.12Z", "2026-01-01T00:00:01.123456Z")).toBe(1_003);
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

test("lastMeaningfulThoughtLine clips without splitting Unicode code points", () => {
  expect(lastMeaningfulThoughtLine([`a${"🙂".repeat(10)}`], 5)).toBe("a🙂🙂🙂🙂…");
});

test("lastMeaningfulThoughtLine removes only common Markdown decoration from the plain preview", () => {
  expect(lastMeaningfulThoughtLine(["## plan", "- **ship** `it`"], 80)).toBe("ship it");
});

test("lastMeaningfulThoughtLine extracts readable Markdown without damaging identifiers", () => {
  expect(lastMeaningfulThoughtLine(["__bold__ _em_"], 80)).toBe("bold em");
  expect(lastMeaningfulThoughtLine(["![diagram](https://x/image_(1).png)"], 80)).toBe("diagram");
  expect(lastMeaningfulThoughtLine(["[docs](https://x/path_(1)/docs)"], 80)).toBe("docs");
  expect(lastMeaningfulThoughtLine(["> > - [ ] **task**"], 80)).toBe("task");
  expect(lastMeaningfulThoughtLine(["> ## heading"], 80)).toBe("heading");
  expect(lastMeaningfulThoughtLine(["## heading `code` ordinary_path_name"], 80)).toBe(
    "heading code ordinary_path_name",
  );
});

test("formatThoughtDuration chooses the seconds tier before rounding", () => {
  expect(formatThoughtDuration(9_999)).toBe("10.0s");
  expect(formatThoughtDuration(10_000)).toBe("10s");
});
