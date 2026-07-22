// @vitest-environment node
import { expect, test } from "vitest";
import { commandScore, fuzzyScore } from "./commandScore";

// commandScore/fuzzyScore port search.js:590-617 verbatim: best of a
// substring match (worth 200 - matchIndex) or a fuzzy subsequence match
// (word-boundary + consecutive-streak rewards) across id + title + keywords;
// a command scoring negative is excluded entirely. `q` arrives pre-lowercased
// and pre-trimmed (renderCommands lowercases it), so these tests pass lower.

test("fuzzyScore is 0 for an empty needle", () => {
  expect(fuzzyScore("", "model")).toBe(0);
});

test("fuzzyScore returns -1 when the needle is not a subsequence", () => {
  expect(fuzzyScore("xyz", "model")).toBe(-1);
});

test("fuzzyScore rewards a word-boundary first char over a mid-word one", () => {
  // 'd' at index 0 of "drain": 10 base + 8 boundary + 4 streak - 0 gap = 22.
  expect(fuzzyScore("d", "drain")).toBe(22);
  // 'r' at index 1 of "drain": 10 base + 0 boundary + 0 streak - 1 gap = 9.
  expect(fuzzyScore("r", "drain")).toBe(9);
});

test("fuzzyScore accumulates the consecutive-character streak", () => {
  // m(22) + o(18) + d(22) = 62, hand-computed from search.js:603-617.
  expect(fuzzyScore("mod", "model")).toBe(62);
});

test("commandScore is 0 for an empty query", () => {
  expect(commandScore({ id: "model", title: "Switch model", keywords: [] }, "")).toBe(0);
});

test("commandScore prefers an exact substring worth 200 - matchIndex", () => {
  expect(commandScore({ id: "model", title: "Switch model", keywords: [] }, "model")).toBe(200);
  // "odel" is at index 1 of id "model" -> 199, which beats any fuzzy score.
  expect(commandScore({ id: "model", title: "Switch model", keywords: [] }, "odel")).toBe(199);
});

test("commandScore searches keywords, not just id and title", () => {
  expect(
    commandScore({ id: "help", title: "Show keyboard shortcuts", keywords: ["?", "keys", "shortcuts"] }, "keys"),
  ).toBe(200);
});

test("commandScore is negative when no field matches, so the command is excluded", () => {
  expect(commandScore({ id: "new", title: "New session", keywords: [] }, "zzz")).toBeLessThan(0);
});
