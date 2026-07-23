import { expect, test } from "vitest";
import { isSinglePaneRoute } from "./singlePane";

test("isSinglePaneRoute is true for a /thread/{ref} share link", () => {
  expect(isSinglePaneRoute("/thread/ref_abc123")).toBe(true);
});

test("isSinglePaneRoute is true for a URI-encoded ref (still one segment)", () => {
  expect(isSinglePaneRoute("/thread/ref%20with%20space")).toBe(true);
});

test("isSinglePaneRoute is false for a /thread path with no ref", () => {
  expect(isSinglePaneRoute("/thread/")).toBe(false);
  expect(isSinglePaneRoute("/thread")).toBe(false);
});

test("isSinglePaneRoute is false for a /thread path with a nested segment", () => {
  // Anchored end ($) + slash-free segment: /thread/a/b is not a share link.
  expect(isSinglePaneRoute("/thread/a/b")).toBe(false);
});

test("isSinglePaneRoute is false for a path that merely starts with /thread", () => {
  // Guards against a prefix (non-anchored) match - /threading is not /thread.
  expect(isSinglePaneRoute("/threading")).toBe(false);
});

test("isSinglePaneRoute is false for every non-thread route", () => {
  expect(isSinglePaneRoute("/")).toBe(false);
  expect(isSinglePaneRoute("/s/ref_abc123")).toBe(false);
  expect(isSinglePaneRoute("/new")).toBe(false);
  expect(isSinglePaneRoute("/settings")).toBe(false);
  expect(isSinglePaneRoute("/settings/credentials")).toBe(false);
});
