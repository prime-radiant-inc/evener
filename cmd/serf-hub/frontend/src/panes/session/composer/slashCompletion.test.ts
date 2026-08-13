import { expect, test } from "vitest";
import type { CommandDescriptor } from "../../../protocol/types.gen";
import { filterSlashCommands, parseSlashToken, spliceSlashCommand } from "./slashCompletion";

// --- parseSlashToken ---------------------------------------------------
//
// Ported from Beautiful UI's prompt-bar trailing-token regex
// (/(^|\s)(\/)([\w-]*)$/, slash only - see slashCompletion.ts's own header
// comment) run against the draft's text UP TO the caret, never the whole
// draft: that's what makes "caret mid-draft" behave like the caret was the
// end of the string for matching purposes, while text after the caret is
// simply invisible to the match.

test("an empty draft has no slash token", () => {
  expect(parseSlashToken("", 0)).toBeNull();
});

test("a bare leading slash at the caret triggers with an empty query", () => {
  expect(parseSlashToken("/", 1)).toEqual({ start: 0, end: 1, query: "" });
});

test("a leading '/x' triggers with the typed body as its query", () => {
  expect(parseSlashToken("/x", 2)).toEqual({ start: 0, end: 2, query: "x" });
});

test("a slash in the middle of a word does not trigger ('foo/bar')", () => {
  expect(parseSlashToken("foo/bar", 7)).toBeNull();
});

test("a slash immediately after a word char does not trigger even mid-token ('foo/b')", () => {
  expect(parseSlashToken("foo/b", 5)).toBeNull();
});

test("a token followed by a space does not trigger once the caret trails the space ('/x ')", () => {
  expect(parseSlashToken("/x ", 3)).toBeNull();
});

test("the SAME text still triggers with the caret parked right after the token, before the space", () => {
  expect(parseSlashToken("/x ", 2)).toEqual({ start: 0, end: 2, query: "x" });
});

test("caret mid-draft triggers off the token it trails, ignoring text after the caret", () => {
  // Caret sits right after "wor" - "ld friend" is still unwritten as far as
  // the parser is concerned, since it never looks past the caret.
  const text = "hello /world friend";
  const caret = "hello /wor".length;
  expect(parseSlashToken(text, caret)).toEqual({ start: "hello ".length, end: caret, query: "wor" });
});

test("no slash before the caret at all means no token", () => {
  expect(parseSlashToken("hello there", 5)).toBeNull();
});

test("a hyphen is a valid token character", () => {
  expect(parseSlashToken("/foo-bar", 8)).toEqual({ start: 0, end: 8, query: "foo-bar" });
});

test("whitespace other than a plain space (e.g. a newline) still anchors a leading slash", () => {
  const text = "line one\n/go";
  expect(parseSlashToken(text, text.length)).toEqual({ start: 9, end: 12, query: "go" });
});

test("a slash preceded by a non-space, non-start character (a digit) does not trigger", () => {
  expect(parseSlashToken("1/foo", 5)).toBeNull();
});

test("a trailing non-word character right after the token breaks the match ('/foo.')", () => {
  expect(parseSlashToken("/foo.", 5)).toBeNull();
});

// --- filterSlashCommands -------------------------------------------------

const CATALOG: CommandDescriptor[] = [
  { name: "review", description: "review the diff" },
  { name: "release", description: "cut a release" },
  { name: "standup", description: "post standup" },
];

test("filters the catalog to names starting with the query", () => {
  expect(filterSlashCommands(CATALOG, "re").map((c) => c.name)).toEqual(["review", "release"]);
});

test("an empty query returns the whole catalog, in order", () => {
  expect(filterSlashCommands(CATALOG, "").map((c) => c.name)).toEqual(["review", "release", "standup"]);
});

test("a query matching nothing returns an empty list", () => {
  expect(filterSlashCommands(CATALOG, "zzz")).toEqual([]);
});

test("matching is case-insensitive", () => {
  expect(filterSlashCommands(CATALOG, "RE").map((c) => c.name)).toEqual(["review", "release"]);
});

// --- spliceSlashCommand ---------------------------------------------------

test("splices the chosen command in at the token start, with a trailing space, caret after the space", () => {
  const token = { start: 0, end: 2, query: "x" };
  const result = spliceSlashCommand("/x", token, "review");
  expect(result).toEqual({ text: "/review ", caret: "/review ".length });
});

test("preserves text before the token", () => {
  const token = { start: 6, end: 8, query: "x" };
  const result = spliceSlashCommand("hello /x", token, "review");
  expect(result).toEqual({ text: "hello /review ", caret: "hello /review ".length });
});

test("preserves text after the token (caret was mid-draft)", () => {
  const token = { start: 0, end: 4, query: "rev" };
  const result = spliceSlashCommand("/rev and more", token, "review");
  expect(result).toEqual({ text: "/review  and more", caret: "/review ".length });
});
