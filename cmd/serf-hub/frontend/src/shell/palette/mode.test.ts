// @vitest-environment node
import { expect, test } from "vitest";
import { computeMode } from "./mode";

// The palette recomputes its mode from state on every keystroke, in this
// exact priority (parity-m6-surfaces.md §2.2, search.js:123-127):
//   command-args if a command is selected, else command-filter if the input
//   starts with "/", else search.

test("a selected command forces command-args mode, regardless of the query text", () => {
  expect(computeMode({ query: "", hasSelectedCommand: true })).toBe("command-args");
  expect(computeMode({ query: "anything", hasSelectedCommand: true })).toBe("command-args");
  // A selected command wins even when the query still starts with "/".
  expect(computeMode({ query: "/model", hasSelectedCommand: true })).toBe("command-args");
});

test("a leading slash with no selected command is command-filter mode", () => {
  expect(computeMode({ query: "/", hasSelectedCommand: false })).toBe("command-filter");
  expect(computeMode({ query: "/mod", hasSelectedCommand: false })).toBe("command-filter");
});

test("anything else is search mode", () => {
  expect(computeMode({ query: "", hasSelectedCommand: false })).toBe("search");
  expect(computeMode({ query: "hello", hasSelectedCommand: false })).toBe("search");
  // A slash NOT at the start does not trigger command-filter.
  expect(computeMode({ query: "a/b", hasSelectedCommand: false })).toBe("search");
});
