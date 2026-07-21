import { test, expect } from "vitest";
import { clip, tailSlice, tailFold, formatToolDuration, formatByteCount, lineCount, parseArgs, parseJSONObject, str } from "./helpers";

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
