// @vitest-environment node

import { expect, test } from "vitest";
import { diffStats, editDiffText } from "./editDiff";

test("editDiffText frames every old line as removed and every new line as added", () => {
  expect(editDiffText("a.go", "one\ntwo", "uno")).toBe("--- a.go\n+++ a.go\n-one\n-two\n+uno");
});

test("diffStats counts content lines and never the file headers", () => {
  expect(diffStats(editDiffText("a.go", "one\ntwo", "uno"))).toEqual({ added: 1, removed: 2 });
  expect(diffStats("*** Update File: a.go\n@@\n context\n+new\n-old\n+more")).toEqual({ added: 2, removed: 1 });
});
