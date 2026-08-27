import { expect, test } from "vitest";
import type { CommandDescriptor } from "../../../protocol/types.gen";
import type { ScopedCommand } from "../../../shell/palette/commands";
import {
  evaluateSlashLabel,
  filterSlashMenuItems,
  mergeSlashCommands,
  parseSlashToken,
  type SlashMenuItem,
  spliceSlashCommand,
} from "./slashCompletion";

// --- parseSlashToken ---------------------------------------------------
//
// Ported from Beautiful UI's prompt-bar trailing-token regex
// (/(^|\s)(\/)([\w:-]*)$/, slash only, colon added for qualified
// "/plugin:name" commands - see slashCompletion.ts's own header comment) run
// against the draft's text UP TO the caret, never the whole draft: that's
// what makes "caret mid-draft" behave like the caret was the end of the
// string for matching purposes, while text after the caret is simply
// invisible to the match.

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

// --- mergeSlashCommands / filterSlashMenuItems ----------------------------
//
// 2026-08-14: the inline slash menu now merges session-scoped BUILT-INS
// (shell/palette/commands.ts's sessionBuiltinCommands - "the composer is
// where you act on this session") with the plugin catalog, instead of the
// catalog alone.

const CATALOG: CommandDescriptor[] = [
  { name: "review", description: "review the diff" },
  { name: "release", description: "cut a release" },
  { name: "standup", description: "post standup" },
];

function builtin(id: string, hint: string, overrides: Partial<ScopedCommand> = {}): ScopedCommand {
  return { id, title: id, hint, keywords: [], scope: "session", ...overrides };
}

test("merges built-ins ahead of the catalog, each carrying its own invocation and hint", () => {
  const items = mergeSlashCommands([builtin("goal", "sets the session goal")], CATALOG);
  expect(items).toEqual([
    { key: "builtin:goal", invocation: "/goal", label: "goal", hint: "sets the session goal", kind: "builtin" },
    { key: "plugin::review", invocation: "/review", label: "review", hint: "review the diff", kind: "plugin" },
    { key: "plugin::release", invocation: "/release", label: "release", hint: "cut a release", kind: "plugin" },
    { key: "plugin::standup", invocation: "/standup", label: "standup", hint: "post standup", kind: "plugin" },
  ]);
});

test("a qualified plugin command's invocation is /pluginName:name, matching slashCommandInvocation", () => {
  const items = mergeSlashCommands([], [{ name: "review", pluginName: "p", source: "plugin" }]);
  expect(items).toEqual([
    { key: "plugin:p:review", invocation: "/p:review", label: "review", hint: "plugin: p", kind: "plugin" },
  ]);
});

test("a plugin entry with no description falls back to naming its provenance, not a blank hint", () => {
  const items = mergeSlashCommands([], [{ name: "review", source: "user" }]);
  expect(items[0]?.hint).toBe("plugin: user");
});

test("an unavailableReason built-in is dropped from the menu entirely - no disabled-row affordance here", () => {
  const items = mergeSlashCommands(
    [builtin("compact", "free up token space", { unavailableReason: "not available right now" })],
    [],
  );
  expect(items).toEqual([]);
});

test("filterSlashMenuItems filters the merged list to labels starting with the query, case-insensitive", () => {
  const items = mergeSlashCommands([builtin("goal", "sets the session goal")], CATALOG);
  expect(filterSlashMenuItems(items, "re").map((i) => i.label)).toEqual(["review", "release"]);
  expect(filterSlashMenuItems(items, "RE").map((i) => i.label)).toEqual(["review", "release"]);
});

test("fuzzy matching finds simplify from a non-prefix query", () => {
  const items = mergeSlashCommands([], [], [{ name: "simplify", description: "rewrite" }]);
  expect(filterSlashMenuItems(items, "smp").map((item) => item.invocation)).toEqual(["/simplify"]);
});

test("skills merge after commands with canonical labels, invocations, and descriptions", () => {
  const items = mergeSlashCommands(
    [builtin("goal", "sets the session goal")],
    [{ name: "review", description: "review the diff" }],
    [{ name: "plugin:review", description: "review with the skill" }],
  );
  expect(items).toEqual([
    { key: "builtin:goal", invocation: "/goal", label: "goal", hint: "sets the session goal", kind: "builtin" },
    { key: "plugin::review", invocation: "/review", label: "review", hint: "review the diff", kind: "plugin" },
    {
      key: "skill:plugin:review",
      invocation: "/plugin:review",
      label: "plugin:review",
      hint: "review with the skill",
      kind: "skill",
    },
  ]);
});

test("fuzzy matching is case-insensitive and label-only", () => {
  const items = mergeSlashCommands(
    [],
    [
      { name: "review", description: "SMP appears only in the description" },
      { name: "Simplify", description: "rewrite" },
    ],
  );
  expect(filterSlashMenuItems(items, "SMP").map((item) => item.label)).toEqual(["Simplify"]);
});

test("fuzzy matching excludes labels that are not subsequences", () => {
  const items = mergeSlashCommands([], [{ name: "simplify" }, { name: "sample" }, { name: "support" }]);
  expect(filterSlashMenuItems(items, "smp").map((item) => item.label)).toEqual(["simplify", "sample"]);
});

test("ranking prefers exact, then contiguousness and beginning", () => {
  const items: SlashMenuItem[] = [
    { key: "scattered", invocation: "/axbyc", label: "axbyc", hint: "", kind: "skill" },
    { key: "notBeginning", invocation: "/zabc", label: "zabc", hint: "", kind: "skill" },
    { key: "exact", invocation: "/abc", label: "abc", hint: "", kind: "skill" },
    { key: "contiguous", invocation: "/abcde", label: "abcde", hint: "", kind: "skill" },
  ];
  expect(filterSlashMenuItems(items, "abc").map((item) => item.key)).toEqual([
    "exact",
    "contiguous",
    "notBeginning",
    "scattered",
  ]);
});

test("ranking prefers the narrowest span, then earliest match start", () => {
  const items: SlashMenuItem[] = [
    { key: "wide", invocation: "/za12e", label: "za12e", hint: "", kind: "skill" },
    { key: "narrow", invocation: "/a1e", label: "a1e", hint: "", kind: "skill" },
    { key: "late", invocation: "/xxa1e", label: "xxa1e", hint: "", kind: "skill" },
    { key: "early", invocation: "/za1e", label: "za1e", hint: "", kind: "skill" },
  ];
  expect(filterSlashMenuItems(items, "ae").map((item) => item.key)).toEqual(["narrow", "early", "late", "wide"]);
});

test("ranking keeps original merge order for complete ties", () => {
  const items: SlashMenuItem[] = [
    { key: "first", invocation: "/ab", label: "ab", hint: "", kind: "skill" },
    { key: "second", invocation: "/AB", label: "AB", hint: "", kind: "skill" },
  ];
  expect(filterSlashMenuItems(items, "a").map((item) => item.key)).toEqual(["first", "second"]);
});

test("ranking chooses the best valid embedding instead of the first character occurrences", () => {
  const items: SlashMenuItem[] = [
    { key: "alternate", invocation: "/abxabc", label: "abxabc", hint: "", kind: "skill" },
    { key: "competitor", invocation: "/a1bc", label: "a1bc", hint: "", kind: "skill" },
  ];
  expect(filterSlashMenuItems(items, "abc").map((item) => item.key)).toEqual(["alternate", "competitor"]);
});

test("repeated characters retain a beginning embedding that catches up later", () => {
  const items: SlashMenuItem[] = [
    { key: "competitor", invocation: "/zabxba", label: "zabxba", hint: "", kind: "skill" },
    { key: "repeated", invocation: "/aababa", label: "aababa", hint: "", kind: "skill" },
  ];
  expect(filterSlashMenuItems(items, "abba").map((item) => item.key)).toEqual(["repeated", "competitor"]);
});

test("command and skill rows with the same name retain distinct keys", () => {
  const items = mergeSlashCommands(
    [builtin("review", "review command")],
    [],
    [{ name: "review", description: "review skill" }],
  );
  expect(items.map((item) => ({ key: item.key, kind: item.kind, invocation: item.invocation }))).toEqual([
    { key: "builtin:review", kind: "builtin", invocation: "/review" },
    { key: "skill:review", kind: "skill", invocation: "/review" },
  ]);
});

test("qualified skill labels can be filtered and spliced with their canonical invocation", () => {
  const items = mergeSlashCommands([], [], [{ name: "plugin:review", description: "review skill" }]);
  const [skill] = filterSlashMenuItems(items, "pr");
  const text = "Use /pr on this";
  const token = parseSlashToken(text, "Use /pr".length)!;
  expect(skill?.label).toBe("plugin:review");
  expect(spliceSlashCommand(text, token, skill!.invocation)).toEqual({
    text: "Use /plugin:review on this",
    caret: "Use /plugin:review".length,
  });
});

test("filterSlashMenuItems with an empty query returns the whole merged list, in order", () => {
  const items = mergeSlashCommands([builtin("goal", "sets the session goal")], CATALOG, [{ name: "simplify" }]);
  expect(filterSlashMenuItems(items, "").map((i) => i.label)).toEqual([
    "goal",
    "review",
    "release",
    "standup",
    "simplify",
  ]);
});

test("filterSlashMenuItems matching nothing returns an empty list", () => {
  const items = mergeSlashCommands([builtin("goal", "sets the session goal")], CATALOG);
  expect(filterSlashMenuItems(items, "zzz")).toEqual([]);
});

test("a colon is a valid token character (a typed qualified prefix keeps the menu alive)", () => {
  expect(parseSlashToken("/plugin:re", 10)).toEqual({ start: 0, end: 10, query: "plugin:re" });
});

test("the slash token follows the documented bare or singly-qualified name grammar", () => {
  expect(parseSlashToken("/-bad", 5)).toBeNull();
  expect(parseSlashToken("/plugin:", 8)).toBeNull();
  expect(parseSlashToken("/plugin:one:two", 16)).toBeNull();
  expect(parseSlashToken(`/${"a".repeat(129)}`, 130)).toBeNull();
});

test("the parser accepts a name at the exact 128-character limit", () => {
  const name = "a".repeat(128);
  expect(parseSlashToken(`/${name}`, name.length + 1)).toEqual({ start: 0, end: name.length + 1, query: name });
});

test("max-length repeated fuzzy labels stay within the deterministic operation bound", () => {
  const label = "a".repeat(128);
  const query = "a".repeat(127);
  const result = evaluateSlashLabel(label, query);
  expect(result.embedding).toEqual({ start: 0, end: 126, longestRun: 127 });
  expect(result.operations).toBeLessThan(5_000_000);
});

// --- spliceSlashCommand ---------------------------------------------------

test("splices the chosen command in at the token start, with a trailing space, caret after the space", () => {
  const token = { start: 0, end: 2, query: "x" };
  const result = spliceSlashCommand("/x", token, "/review");
  expect(result).toEqual({ text: "/review ", caret: "/review ".length });
});

test("completion does not double a suffix space", () => {
  const token = parseSlashToken("Use /sim on this", 8)!;
  expect(spliceSlashCommand("Use /sim on this", token, "/simplify").text).toBe("Use /simplify on this");
});

test("completion does not double a suffix tab or newline", () => {
  const tabText = "Use /sim\ton this";
  const tabToken = parseSlashToken(tabText, "Use /sim".length)!;
  expect(spliceSlashCommand(tabText, tabToken, "/simplify").text).toBe("Use /simplify\ton this");

  const newlineText = "Use /sim\non this";
  const newlineToken = parseSlashToken(newlineText, "Use /sim".length)!;
  expect(spliceSlashCommand(newlineText, newlineToken, "/simplify").text).toBe("Use /simplify\non this");
});

test("splices a qualified plugin invocation in whole, not just the bare name", () => {
  const token = { start: 0, end: 2, query: "x" };
  const result = spliceSlashCommand("/x", token, "/p:review");
  expect(result).toEqual({ text: "/p:review ", caret: "/p:review ".length });
});

test("preserves text before the token", () => {
  const token = { start: 6, end: 8, query: "x" };
  const result = spliceSlashCommand("hello /x", token, "/review");
  expect(result).toEqual({ text: "hello /review ", caret: "hello /review ".length });
});

test("preserves text after the token without adding a duplicate suffix space", () => {
  const token = { start: 0, end: 4, query: "rev" };
  const result = spliceSlashCommand("/rev and more", token, "/review");
  expect(result).toEqual({ text: "/review and more", caret: "/review".length });
});
