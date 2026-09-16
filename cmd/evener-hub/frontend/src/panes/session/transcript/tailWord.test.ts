import { expect, test } from "vitest";
import { splitTrailingWord } from "./tailWord";

test("splits at the whitespace before the final word, keeping that whitespace with the head", () => {
  expect(splitTrailingWord("List the directory")).toEqual(["List the ", "directory"]);
  expect(splitTrailingWord("Ran go test ./...")).toEqual(["Ran go test ", "./..."]);
});

test("a single word (or empty text) is its own atom: no head, all trailing", () => {
  expect(splitTrailingWord("word")).toEqual(["", "word"]);
  expect(splitTrailingWord("")).toEqual(["", ""]);
});

test("trailing whitespace with nothing after it does not create an empty atom", () => {
  expect(splitTrailingWord("word ")).toEqual(["", "word "]);
});

test("interior runs of whitespace stay whole in the head", () => {
  expect(splitTrailingWord("a  b")).toEqual(["a  ", "b"]);
});

test("multi-line text splits at the final word of its last line", () => {
  expect(splitTrailingWord("line one\nline two end")).toEqual(["line one\nline two ", "end"]);
  expect(splitTrailingWord("\nword")).toEqual(["\n", "word"]);
});
