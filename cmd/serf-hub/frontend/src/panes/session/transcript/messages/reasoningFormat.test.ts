import { test, expect } from "vitest";
import { joinedReasoningParagraphs, reasoningPreview, thoughtSeconds } from "./reasoningFormat";

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
  expect(joinedReasoningParagraphs([["Hel", "lo "], ["wor", "ld"]])).toEqual(["Hello ", "world"]);
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

// --- reasoningPreview -------------------------------------------------------

test("undefined summaries yields an empty preview", () => {
  expect(reasoningPreview(undefined)).toBe("");
});

test("preview is the first line of the first non-empty paragraph", () => {
  expect(reasoningPreview([["First paragraph.\nmore of it"], ["Second paragraph"]])).toBe("First paragraph.");
});

test("preview skips a leading empty paragraph to find the first real one", () => {
  expect(reasoningPreview([[], ["The real first line"]])).toBe("The real first line");
});

test("preview clips a long first line to the given max length", () => {
  // A single unbroken run of non-space characters, so the clip boundary
  // can't coincidentally land on whitespace trimEnd() would also eat.
  const long = "x".repeat(100);
  const preview = reasoningPreview([[long]], 20);
  expect(preview).toBe("x".repeat(20) + "…");
});

// --- thoughtSeconds ----------------------------------------------------------
// Only ever computes from REAL server timestamps on the item
// (ItemModel.startedAt/completedAt) - never a client wall clock. As of this
// wave, internal/appprojector never actually stamps a reasoning item's
// StartedAt/CompletedAt (confirmed against appwire_projection.go and
// apptranscript.go's reasoning-item construction sites - neither sets
// either field), so in practice this always returns undefined today; the
// function stays correct and ready for whenever the backend adds real
// timestamps, rather than fabricating one now.

test("undefined startedAt/completedAt yields no duration", () => {
  expect(thoughtSeconds(undefined, undefined)).toBeUndefined();
  expect(thoughtSeconds("2026-01-01T00:00:00.000Z", undefined)).toBeUndefined();
  expect(thoughtSeconds(undefined, "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("an unparseable timestamp yields no duration rather than a garbage number", () => {
  expect(thoughtSeconds("not a date", "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("computes whole elapsed seconds, rounded, from two real ISO timestamps", () => {
  expect(thoughtSeconds("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.400Z")).toBe(4);
  expect(thoughtSeconds("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.600Z")).toBe(5);
});

test("a sub-second elapsed span floors at 1s, never 0s", () => {
  expect(thoughtSeconds("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.200Z")).toBe(1);
});
