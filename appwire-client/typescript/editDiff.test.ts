// @vitest-environment node

import { expect, test } from "vitest";
import {
  classifyDiffLineKinds,
  type DiffLineKind,
  diffStats,
  type EditOperation,
  editBatchDiffText,
  editDiffText,
  editOperationsOf,
} from "./index";

test("editDiffText frames every old line as removed and every new line as added", () => {
  expect(editDiffText("a.go", "one\ntwo", "uno")).toBe("--- a.go\n+++ a.go\n-one\n-two\n+uno");
});

test("diffStats counts content lines and never the file headers", () => {
  expect(diffStats(editDiffText("a.go", "one\ntwo", "uno"))).toEqual({ added: 1, removed: 2 });
  expect(diffStats("*** Update File: a.go\n@@\n context\n+new\n-old\n+more")).toEqual({ added: 2, removed: 1 });
});

test("editOperationsOf decodes canonical edits[] and legacy single-edit shapes", () => {
  expect(editOperationsOf({ file_path: "a.md", edits: [{ old_string: "a", new_string: "b" }] })).toEqual<
    EditOperation[]
  >([{ oldString: "a", newString: "b" }]);

  expect(
    editOperationsOf({
      file_path: "a.md",
      edits: [
        { old_string: "a", new_string: "b", replace_all: true },
        { old_string: "c", new_string: "d", replace_all: false },
      ],
    }),
  ).toEqual<EditOperation[]>([
    { oldString: "a", newString: "b" },
    { oldString: "c", newString: "d" },
  ]);

  expect(
    editOperationsOf({
      file_path: "a.md",
      old_string: "legacy-old",
      new_string: "legacy-new",
      replace_all: true,
    }),
  ).toEqual<EditOperation[]>([{ oldString: "legacy-old", newString: "legacy-new" }]);

  expect(editOperationsOf({ file_path: "a.md", old_string: "only-old" })).toEqual<EditOperation[]>([
    { oldString: "only-old", newString: "" },
  ]);
  expect(editOperationsOf({ file_path: "a.md", new_string: "only-new" })).toEqual<EditOperation[]>([
    { oldString: "", newString: "only-new" },
  ]);
});

test("editOperationsOf rejects malformed and pre-repair inputs", () => {
  expect(editOperationsOf({ edits: null, old_string: "a", new_string: "b" })).toBeNull();
  expect(editOperationsOf({ edits: [null] })).toBeNull();
  expect(editOperationsOf({ edits: [] })).toBeNull();
  expect(editOperationsOf({ edits: "not-json" })).toBeNull();
  expect(editOperationsOf({ edits: JSON.stringify([{ old_string: "a", new_string: "b" }]) })).toBeNull();
  expect(editOperationsOf({ edits: { old_string: "a", new_string: "b" } })).toBeNull();
  expect(editOperationsOf({ edits: [{ old_str: "a", new_str: "b" }] })).toBeNull();
  expect(editOperationsOf({ edits: [{ old_string: "a" }] })).toBeNull();
  expect(editOperationsOf({ edits: [{ old_string: 123, new_string: "b" }] })).toBeNull();
  expect(editOperationsOf({ edits: [{ old_string: "a", new_string: "b", replace_all: "true" }] })).toBeNull();
  expect(editOperationsOf({ file_path: "a.md" })).toBeNull();
  expect(editOperationsOf({})).toBeNull();
});

test("editBatchDiffText matches legacy editDiffText for 1 edit and inserts @@ edit N @@ for N>1 edits", () => {
  expect(editBatchDiffText("a.go", [{ oldString: "one\ntwo", newString: "uno" }])).toBe(
    editDiffText("a.go", "one\ntwo", "uno"),
  );
  expect(
    editBatchDiffText("a.go", [
      { oldString: "one", newString: "uno" },
      { oldString: "two\nthree", newString: "dos" },
    ]),
  ).toBe("--- a.go\n+++ a.go\n@@ edit 1 @@\n-one\n+uno\n@@ edit 2 @@\n-two\n-three\n+dos");
  expect(editBatchDiffText("a.go", [])).toBe("");
});

test("classifyDiffLineKinds and diffStats treat Markdown ---/+++ content lines as del/add in flat and multi-edit diffs", () => {
  const flat = editDiffText("a.md", "-- heading\n--", "++ heading\n++");
  expect(classifyDiffLineKinds(flat)).toEqual<DiffLineKind[]>(["header", "header", "del", "del", "add", "add"]);
  expect(diffStats(flat)).toEqual({ added: 2, removed: 2 });

  const batch = editBatchDiffText("a.md", [
    { oldString: "-- first", newString: "++ first" },
    { oldString: "--", newString: "++" },
  ]);
  expect(classifyDiffLineKinds(batch)).toEqual<DiffLineKind[]>([
    "header",
    "header",
    "header",
    "del",
    "add",
    "header",
    "del",
    "add",
  ]);
  expect(diffStats(batch)).toEqual({ added: 2, removed: 2 });
});

test("classifyDiffLineKinds matches DiffBlock line kinds and resets header state on diff --git / index", () => {
  const gitDiff = [
    "diff --git a/a.md b/a.md",
    "index 1111111..2222222 100644",
    "--- a/a.md",
    "+++ b/a.md",
    "@@ -1,3 +1,3 @@",
    " context",
    "--- old",
    "+++ new",
    "\\ No newline at end of file",
    "diff --git a/b.md b/b.md",
    "--- a/b.md",
    "+++ b/b.md",
    "--- deleted",
    "",
  ].join("\n");
  expect(classifyDiffLineKinds(gitDiff)).toEqual<DiffLineKind[]>([
    "header",
    "header",
    "header",
    "header",
    "header",
    "context",
    "del",
    "add",
    "meta",
    "header",
    "header",
    "header",
    "del",
  ]);
  expect(diffStats(gitDiff)).toEqual({ added: 1, removed: 2 });
});
