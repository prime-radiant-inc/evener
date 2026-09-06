import { expect, it } from "vitest";
import {
  addFallback,
  assertFallbacksCurrent,
  collectFallbacks,
} from "./launchFallbacks";

it("preserves fallback order and rejects duplicates", () => {
  const current = ["fake/first"];
  expect(addFallback(current, "fake/second")).toEqual([
    "fake/first",
    "fake/second",
  ]);
  expect(() => addFallback(current, "fake/first")).toThrow();
  expect(current).toEqual(["fake/first"]);
});
it("distinguishes inheritance from explicitly disabling fallbacks", () => {
  expect(collectFallbacks([], false)).toBeUndefined();
  expect(collectFallbacks([], true)).toEqual([]);
  expect(collectFallbacks(["fake/first"], true)).toEqual(["fake/first"]);
});
it("treats reordered fallbacks and explicit emptiness as conflicting changes", () => {
  expect(() =>
    assertFallbacksCurrent(["a", "b"], ["b", "a"], undefined),
  ).toThrow();
  expect(() => assertFallbacksCurrent(undefined, [], ["a"])).toThrow();
});
it("allows unchanged and convergent edits", () => {
  expect(() => assertFallbacksCurrent(["a"], ["a"], ["b"])).not.toThrow();
  expect(() => assertFallbacksCurrent(["a"], [], [])).not.toThrow();
  expect(() => assertFallbacksCurrent([], undefined, undefined)).not.toThrow();
});
