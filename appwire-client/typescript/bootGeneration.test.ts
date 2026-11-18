// @vitest-environment node
import { expect, test } from "vitest";
import {
  type BootGenerationAction,
  compareBootGeneration,
  confirmedBootReplacement,
  DAEMONLESS_BOOT_GENERATION,
} from "./bootGeneration";

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

// confirmedBootReplacement answers the ONE question a hold retirement may act
// on: did evidence CONFIRM that one source replaced another? compareBootGeneration
// answers what to do with history, where an ambiguous token is harmless — it
// invalidates and the next read replaces. A hold is different: an unchanged hold
// emits no update of its own, so a retirement decided on a misread token cannot
// be undone later.
const confirmCases: Array<[held: string, incoming: string, want: boolean]> = [
  // A comparable pair decides normally.
  ["3", "3", false],
  ["3", "4", true],
  ["4", "3", false],
  ["3@root1", "4@root1", true],
  ["4@root1", "3@root1", false],

  // A bare counter does not say whose counter it is. An older daemon stamps a
  // descendant's worktree-change resync with "5" while its reads name the same
  // controller "5@root", so the mismatch confirms nothing. This is the case
  // that retired a live hold nothing had replaced.
  ["5@root", "5", false],
  ["5@root", "6", false],
  ["5", "5@root", false],
  ["6", "5@root", false],

  // Two qualified tokens from different roots DO confirm: a daemon serving the
  // thread as its own root is a genuine takeover, not a naming difference.
  ["5@root1", "1@root2", true],
  ["1@root2", "5@root1", true],

  // daemonless names a definite absence of a daemon, so it confirms either way.
  ["5", DAEMONLESS_BOOT_GENERATION, true],
  [DAEMONLESS_BOOT_GENERATION, "1", true],
  ["5@root1", DAEMONLESS_BOOT_GENERATION, true],
  [DAEMONLESS_BOOT_GENERATION, "1@root1", true],
  [DAEMONLESS_BOOT_GENERATION, DAEMONLESS_BOOT_GENERATION, false],

  // Nothing held yet is not a replacement, and a malformed token names nothing.
  ["", "1", false],
  ["", DAEMONLESS_BOOT_GENERATION, false],
  ["3", "x", false],
  ["x", "3", false],
  ["3@", "4", false],
  ["3", "4@", false],
];

test.each(confirmCases)("confirmedBootReplacement(%j, %j) is %s", (held, incoming, want) => {
  expect(confirmedBootReplacement(held, incoming)).toBe(want);
});
