// @vitest-environment node

import { expect, test } from "vitest";
import { buildComposerInput, buildInput, formatQuoteBlock, mergeDraftText } from "./composerInput";

// Skill names reach buildInput from drafts, recovery records and queue
// projections. Any of those can carry a stray space, an empty string or a
// repeat, and the wire must never receive an empty or duplicated skill item:
// the backend rejects an empty canonical name, and a repeat renders duplicate
// markers and sends the same selection twice.
test("buildInput trims, drops empty names and dedupes skill selections preserving order", () => {
  const input = buildInput("hello", undefined, [" pkg:probe ", "", "pkg:probe", "pkg:other", "pkg:other"]);

  expect(input.filter((item) => item.type === "skill")).toEqual([
    { type: "skill", name: "pkg:probe" },
    { type: "skill", name: "pkg:other" },
  ]);
});

test("buildInput emits no skill items when every name is empty", () => {
  const input = buildInput("hello", undefined, ["", "   "]);

  expect(input.filter((item) => item.type === "skill")).toEqual([]);
});

test("buildInput keeps same-spelling skills and commands distinct and dedupes each kind", () => {
  expect(buildInput("DATA_318", undefined, ["probe"], [" probe ", "", "probe", "pkg:probe"])).toEqual([
    { type: "text", text: "DATA_318" },
    { type: "skill", name: "probe" },
    { type: "command", name: "probe" },
    { type: "command", name: "pkg:probe" },
  ]);
});

test("wire editing locations follow attachment translation without activating prose", () => {
  const text = "😀 [image 1] /same /same /same";
  const mentions = [
    { kind: "command" as const, name: "same", offset: text.indexOf("/same") },
    { kind: "skill" as const, name: "same", offset: text.indexOf("/same") + 6 },
  ];
  const input = buildComposerInput(
    text,
    [{ marker: 1, mediaType: "image/png", data: "AQID", name: "one.png" }],
    ["same"],
    ["same"],
    mentions,
  );
  expect(input[0]).toEqual({
    type: "text",
    text: "😀 (attached image 1: one.png) /same /same /same",
    mentions: [
      { kind: "command", name: "same", offset: 31 },
      { kind: "skill", name: "same", offset: 37 },
    ],
  });
  expect(input.filter((item) => item.type === "command")).toEqual([{ type: "command", name: "same" }]);
  expect(input.filter((item) => item.type === "skill")).toEqual([{ type: "skill", name: "same" }]);
  expect(mentions[0]?.offset).toBe(text.indexOf("/same"));
});

test("mergeDraftText appends after exactly one blank line, or replaces a blank draft", () => {
  expect(mergeDraftText("", "queued text")).toBe("queued text");
  expect(mergeDraftText("  \n", "queued text")).toBe("queued text");
  expect(mergeDraftText("my draft  \n\n", "queued text")).toBe("my draft\n\nqueued text");
  expect(mergeDraftText("my draft", "  spaced")).toBe("my draft\n\n  spaced");
});

test("mergeDraftText can put the addition in front with no separator", () => {
  expect(mergeDraftText("rest", "> quote\n\n", "prefix")).toBe("> quote\n\nrest");
});

test("formatQuoteBlock turns a single line into one '> ' line plus a trailing blank line", () => {
  expect(formatQuoteBlock("hello world")).toBe("> hello world\n\n");
});

test("formatQuoteBlock gives each line of a multi-line selection its own '> ' prefix", () => {
  expect(formatQuoteBlock("first line\nsecond line\nthird line")).toBe("> first line\n> second line\n> third line\n\n");
});

test("formatQuoteBlock trims leading/trailing whitespace-only lines before quoting", () => {
  expect(formatQuoteBlock("\n\n  middle  \n\n")).toBe("> middle\n\n");
});

test("formatQuoteBlock normalizes CRLF line endings to a single '> ' prefix per line", () => {
  expect(formatQuoteBlock("a\r\nb")).toBe("> a\n> b\n\n");
});

test("formatQuoteBlock preserves a blank line in the middle of the selection as an empty '>' line", () => {
  expect(formatQuoteBlock("first\n\nsecond")).toBe("> first\n> \n> second\n\n");
});

test("formatQuoteBlock formats an empty or whitespace-only selection to an empty string", () => {
  expect(formatQuoteBlock("")).toBe("");
  expect(formatQuoteBlock("   \n  ")).toBe("");
});
