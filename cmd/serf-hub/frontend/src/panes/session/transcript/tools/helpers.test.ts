import { test, expect } from "vitest";
import {
  clip,
  tailSlice,
  tailFold,
  formatToolDuration,
  formatByteCount,
  lineCount,
  parseArgs,
  parseJSONObject,
  str,
  rememberedArgs,
  trailingBracketFooter,
} from "./helpers";
import type { ItemModel } from "../../../../protocol/model";

// --- clip ---------------------------------------------------------------

test("clip: text at or under the limit passes through unchanged", () => {
  expect(clip("hello", 10)).toBe("hello");
  expect(clip("hello", 5)).toBe("hello");
});

test("clip: text over the limit is head-truncated with an ellipsis", () => {
  expect(clip("hello world", 5)).toBe("hello…");
});

test("clip: empty string stays empty", () => {
  expect(clip("", 5)).toBe("");
});

// --- tailSlice ------------------------------------------------------------

test("tailSlice: text at or under max passes through unchanged", () => {
  expect(tailSlice("hello", 10)).toBe("hello");
});

test("tailSlice: text over max keeps only the last max chars", () => {
  expect(tailSlice("abcdefghij", 4)).toBe("ghij");
});

test("tailSlice: a cut that would split a UTF-16 surrogate pair advances by one instead", () => {
  // U+1F600 (😀) is the surrogate pair D83D DE00. Keeping the last 1 code
  // unit alone would split it; tailSlice must drop the orphaned low
  // surrogate rather than split the pair.
  const text = "x😀"; // "x" + full emoji
  expect(tailSlice(text, 1)).toBe("");
  expect(tailSlice(text, 2)).toBe("😀");
});

// --- tailFold ---------------------------------------------------------

test("tailFold: text under budget passes through with no elision note", () => {
  expect(tailFold("hello", 10)).toBe("hello");
});

test("tailFold: text over budget gets an honest elision line then the tail slice", () => {
  const text = "a".repeat(20);
  expect(tailFold(text, 5)).toBe("earlier output not retained — showing the last 5 chars\n" + "a".repeat(5));
});

// --- formatToolDuration ---------------------------------------------------

test("formatToolDuration: sub-1000ms rounds and never shows 0ms", () => {
  expect(formatToolDuration(0)).toBe("1ms");
  expect(formatToolDuration(0.4)).toBe("1ms");
  expect(formatToolDuration(499)).toBe("499ms");
  expect(formatToolDuration(999)).toBe("999ms");
});

test("formatToolDuration: 1s-10s shows one decimal, trailing .0 stripped", () => {
  expect(formatToolDuration(1000)).toBe("1s");
  expect(formatToolDuration(1500)).toBe("1.5s");
  expect(formatToolDuration(9999)).toBe("10s");
});

test("formatToolDuration: 10s and above rounds to whole seconds", () => {
  expect(formatToolDuration(10000)).toBe("10s");
  expect(formatToolDuration(65400)).toBe("65s");
});

// --- formatByteCount -------------------------------------------------------

test("formatByteCount: never unit-scales, singular only for exactly 1", () => {
  expect(formatByteCount(0)).toBe("0 bytes");
  expect(formatByteCount(1)).toBe("1 byte");
  expect(formatByteCount(2)).toBe("2 bytes");
  expect(formatByteCount(2_000_000)).toBe("2000000 bytes");
});

// --- lineCount -------------------------------------------------------------

test("lineCount: empty string has zero lines", () => {
  expect(lineCount("")).toBe(0);
});

test("lineCount: a single line with no trailing newline counts as one", () => {
  expect(lineCount("one line")).toBe(1);
});

test("lineCount: a trailing newline is not counted as an extra empty line", () => {
  expect(lineCount("a\nb\nc\n")).toBe(3);
});

test("lineCount: only ONE trailing empty element is dropped, not more", () => {
  expect(lineCount("a\nb\n\n")).toBe(3); // "a", "b", "" - the blank line is real content
});

// --- parseArgs / str -------------------------------------------------------

test("parseArgs: parses a well-formed JSON object", () => {
  expect(parseArgs('{"a":"b","n":1}')).toEqual({ a: "b", n: 1 });
});

test("parseArgs: malformed JSON degrades to an empty object, never throws", () => {
  expect(() => parseArgs("{not json")).not.toThrow();
  expect(parseArgs("{not json")).toEqual({});
});

test("parseArgs: undefined input degrades to an empty object", () => {
  expect(parseArgs(undefined)).toEqual({});
});

test("parseArgs: a valid JSON array or primitive is not an object - degrades to empty", () => {
  expect(parseArgs("[1,2,3]")).toEqual({});
  expect(parseArgs("42")).toEqual({});
  expect(parseArgs("null")).toEqual({});
});

test("str: reads a string field, undefined for a missing or non-string field", () => {
  expect(str({ a: "hello" }, "a")).toBe("hello");
  expect(str({ a: 1 }, "a")).toBeUndefined();
  expect(str({}, "a")).toBeUndefined();
});

test("parseJSONObject: parses well-formed JSON text into an object, mirrors parseArgs' leniency", () => {
  expect(parseJSONObject('{"x":1}')).toEqual({ x: 1 });
  expect(parseJSONObject("not json")).toBeUndefined();
  expect(parseJSONObject(undefined)).toBeUndefined();
  expect(parseJSONObject("[1]")).toBeUndefined();
});

// --- rememberedArgs ---------------------------------------------------
// Ground truth (verified live against the real reducer, not assumed):
// internal/appprojector/appwire_projection.go's EventToolCallEnd case never
// sets ThreadItem.ArgumentsJSON on the struct literal it builds for
// item/completed - only item/started carries it. protocol/reducer.ts's
// item/completed handler replaces the whole item via wireItemToModel(item)
// (mergeReasoning only carries over reasoningSummaries from the prior
// state), so a settled ItemModel's argumentsJSON is undefined even though
// the SAME call's item/started notification had it. rememberedArgs is a
// same-file mitigation: cache a call's parsed args the first time they're
// seen (while live), keyed by callId, and serve that cache once the
// wire-settled item's own args go missing - this only helps a call
// observed live at least once; a cold-opened historical transcript that
// never saw item/started has no cache entry and still degrades to {}.

function tItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test("rememberedArgs: returns the parsed args when present", () => {
  const item = tItem({ callId: "call_remember_a", argumentsJSON: '{"pattern":"x"}' });
  expect(rememberedArgs(item)).toEqual({ pattern: "x" });
});

test("rememberedArgs: a later call with the SAME callId but no argumentsJSON reuses the cached args", () => {
  const live = tItem({ callId: "call_remember_b", argumentsJSON: '{"path":"src"}' });
  rememberedArgs(live); // simulates the item/started render
  const settled = tItem({ callId: "call_remember_b", argumentsJSON: undefined, output: "done" });
  expect(rememberedArgs(settled)).toEqual({ path: "src" });
});

test("rememberedArgs: a different callId never sees another call's cached args", () => {
  rememberedArgs(tItem({ callId: "call_remember_c1", argumentsJSON: '{"a":1}' }));
  const other = tItem({ callId: "call_remember_c2", argumentsJSON: undefined });
  expect(rememberedArgs(other)).toEqual({});
});

test("rememberedArgs: falls back to the item id when callId is absent", () => {
  const live = tItem({ id: "item_remember_d", callId: undefined, argumentsJSON: '{"z":9}' });
  rememberedArgs(live);
  const settled = tItem({ id: "item_remember_d", callId: undefined, argumentsJSON: undefined });
  expect(rememberedArgs(settled)).toEqual({ z: 9 });
});

test("rememberedArgs: never observed live and no argumentsJSON at all degrades to {} (the documented remaining gap)", () => {
  const neverSeenLive = tItem({ callId: "call_remember_never_live", argumentsJSON: undefined });
  expect(rememberedArgs(neverSeenLive)).toEqual({});
});

// --- trailingBracketFooter ----------------------------------------------
// Ground truth: several agent-side formatters (formatShellResult,
// formatJobStop, formatDelegateSend, formatJobReadOutput) all end their
// plain-text output in a "[... · ...]" bracketed footer summarizing the
// call's own outcome. This extracts that footer's inner text verbatim
// (the tool already wrote a good human summary; no need to re-derive
// individual fields from it) rather than the whole preceding body.

test("trailingBracketFooter: extracts the inner text of a trailing bracket", () => {
  expect(trailingBracketFooter("some output\n[job job_1 · completed · already_terminal]")).toBe(
    "job job_1 · completed · already_terminal",
  );
});

test("trailingBracketFooter: trailing whitespace after the closing bracket is tolerated", () => {
  expect(trailingBracketFooter("x\n[exit 0]\n\n")).toBe("exit 0");
});

test("trailingBracketFooter: undefined when the text doesn't end in a bracket", () => {
  expect(trailingBracketFooter("plain text, no footer")).toBeUndefined();
});

test("trailingBracketFooter: undefined for an empty string", () => {
  expect(trailingBracketFooter("")).toBeUndefined();
});
