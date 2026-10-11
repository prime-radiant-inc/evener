// @vitest-environment node
import { expect, test } from "vitest";
import { type BootGenerationAction, compareBootGeneration, DAEMONLESS_BOOT_GENERATION } from "./bootGeneration";

// The table mirrors appwire/boot_generation_test.go's TestCompareBootGeneration
// row for row: the client and the daemon must agree on every token pair.
const cases: Array<[held: string, incoming: string, want: BootGenerationAction]> = [
  ["3", "3", "apply"],
  [DAEMONLESS_BOOT_GENERATION, DAEMONLESS_BOOT_GENERATION, "apply"],
  ["3", "2", "ignore"],
  ["10", "9", "ignore"],
  ["3", "4", "replace"],
  // Numeric, not lexical: 10 is higher than 9.
  ["9", "10", "replace"],
  ["3", DAEMONLESS_BOOT_GENERATION, "replace"],
  [DAEMONLESS_BOOT_GENERATION, "1", "replace"],
  // Nothing held yet: whatever arrives replaces.
  ["", "1", "replace"],
  ["", DAEMONLESS_BOOT_GENERATION, "replace"],
  // A descendant's token, qualified by its root: same owner compares numerically.
  ["3@root1", "3@root1", "apply"],
  ["3@root1", "2@root1", "ignore"],
  ["3@root1", "4@root1", "replace"],
  ["9@root1", "10@root1", "replace"],
  // Different owners never compare.
  ["3@root1", "2@root2", "replace"],
  ["3@root1", "4@root2", "replace"],
  // Qualified against unqualified, either way, replaces.
  ["3@root1", "2", "replace"],
  ["3", "2@root1", "replace"],
  ["3", "4@root1", "replace"],
  // daemonless against a qualified token, either way, replaces.
  ["3@root1", DAEMONLESS_BOOT_GENERATION, "replace"],
  [DAEMONLESS_BOOT_GENERATION, "1@root1", "replace"],
  // A malformed token is some other token: it replaces.
  ["3@", "2@", "replace"],
  ["3", "x", "replace"],
];

test.each(cases)("compareBootGeneration(%j, %j) is %s", (held, incoming, want) => {
  expect(compareBootGeneration(held, incoming)).toBe(want);
});

test("counters beyond Number.MAX_SAFE_INTEGER still compare exactly", () => {
  expect(compareBootGeneration("9007199254740993", "9007199254740992")).toBe("ignore");
  expect(compareBootGeneration("9007199254740992", "9007199254740993")).toBe("replace");
});
