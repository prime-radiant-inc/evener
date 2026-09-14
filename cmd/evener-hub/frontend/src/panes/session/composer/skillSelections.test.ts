import { expect, test } from "vitest";
import { addSkillSelection, removeSkillSelection } from "./skillSelections";

test("addSkillSelection appends a canonical name that is not yet selected", () => {
  expect(addSkillSelection([], "pkg:probe")).toEqual(["pkg:probe"]);
  expect(addSkillSelection(["pkg:probe"], "pkg:other")).toEqual(["pkg:probe", "pkg:other"]);
});

test("addSkillSelection deduplicates a name that is already selected", () => {
  expect(addSkillSelection(["pkg:probe"], "pkg:probe")).toEqual(["pkg:probe"]);
});

test("addSkillSelection preserves the existing selection order", () => {
  expect(addSkillSelection(["pkg:first", "pkg:second"], "pkg:third")).toEqual(["pkg:first", "pkg:second", "pkg:third"]);
});

test("addSkillSelection canonicalizes the incoming name and the existing list", () => {
  expect(addSkillSelection([], "   ")).toEqual([]);
  expect(addSkillSelection(["pkg:probe"], " pkg:probe ")).toEqual(["pkg:probe"]);
  expect(addSkillSelection([" pkg:probe ", ""], "pkg:other")).toEqual(["pkg:probe", "pkg:other"]);
});

test("removeSkillSelection removes only the named selection", () => {
  expect(removeSkillSelection(["pkg:a", "pkg:b", "pkg:c"], "pkg:b")).toEqual(["pkg:a", "pkg:c"]);
});

test("removeSkillSelection canonicalizes before filtering, like add does", () => {
  // The file's contract is that adding and removing behave identically on a
  // non-canonical list: a padded entry that add would have collapsed must not
  // survive the removal of its canonical name.
  expect(removeSkillSelection([" pkg:probe ", "pkg:other"], "pkg:probe")).toEqual(["pkg:other"]);
  expect(removeSkillSelection(["pkg:probe", "", "pkg:probe"], " pkg:probe ")).toEqual([]);
  expect(removeSkillSelection([], "pkg:probe")).toEqual([]);
});

test("removeSkillSelection leaves the selections unchanged when the name is absent", () => {
  expect(removeSkillSelection(["pkg:a"], "pkg:missing")).toEqual(["pkg:a"]);
  expect(removeSkillSelection([], "pkg:missing")).toEqual([]);
});
