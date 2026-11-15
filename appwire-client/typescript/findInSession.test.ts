// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  findIndexIn,
  findMatches,
  findNeedle,
  findReadable,
  matchPosition,
  stepMatch,
  textMatchesFind,
} from "./findInSession";

describe("what counts as a find match", () => {
  it("ignores case and the shape of whitespace on both sides", () => {
    const needle = findNeedle("  Flaky\n  SETTLE ");
    expect(textMatchesFind("the flaky\tsettle test", needle)).toBe(true);
    expect(textMatchesFind("the flaky\n\nsettle test", needle)).toBe(true);
    expect(textMatchesFind("the flakysettle test", needle)).toBe(false);
  });

  it("matches nothing for a query with no words", () => {
    expect(findNeedle(" \n\t")).toBe("");
    expect(textMatchesFind("anything at all", findNeedle("   "))).toBe(false);
    expect(findMatches(["anything"], "  ", (row) => row)).toEqual([]);
  });

  it("locates the match in the text a snippet is cut from", () => {
    const readable = findReadable("  One\n\nTwo  THREE ");
    const at = findIndexIn(readable, findNeedle("two three"));
    expect(readable.slice(at, at + "two three".length).toLowerCase()).toBe("two three");
  });

  it("locates a match that follows a character its lower case makes longer", () => {
    // "İ" lower-cases to "i" plus a combining dot: two units for one
    // character, so an offset into the lower-cased copy is not an offset into
    // the text a snippet is cut from.
    const readable = findReadable("İ needle");
    const at = findIndexIn(readable, findNeedle("needle"));
    expect(readable.slice(at, at + "needle".length)).toBe("needle");
  });

  it("finds a later occurrence when the first sits inside a character's expansion", () => {
    // "İ" lower-cases to "i" plus a combining dot, and this text also holds
    // that dot as a character of its own. The needle's first lower-cased
    // occurrence starts on the expansion's dot, where no character of the text
    // begins; its next starts at the dot that is a character. A snippet is cut
    // at the second, so the walk has to try past the first.
    const readable = findReadable("İb\u0307b");
    expect(findIndexIn(readable, findNeedle("\u0307b"))).toBe(2);
  });
});

describe("stepping between matches", () => {
  const matches = [2, 5, 9];

  it("starts at the newest and walks older, then stops", () => {
    expect(stepMatch(matches, null, -1)).toBe(9);
    expect(stepMatch(matches, 9, -1)).toBe(5);
    expect(stepMatch(matches, 2, -1)).toBeNull();
    expect(stepMatch(matches, 5, 1)).toBe(9);
    expect(stepMatch(matches, 9, 1)).toBeNull();
    expect(stepMatch([], null, -1)).toBeNull();
  });

  it("steps from a current row that is no longer a match", () => {
    expect(stepMatch(matches, 6, -1)).toBe(5);
    expect(stepMatch(matches, 6, 1)).toBe(9);
  });

  it("reports the position oldest first", () => {
    expect(matchPosition(matches, 5)).toEqual({ position: 2, count: 3 });
    expect(matchPosition(matches, null)).toEqual({ position: 3, count: 3 });
    expect(matchPosition([], null)).toEqual({ position: 0, count: 0 });
  });
});
