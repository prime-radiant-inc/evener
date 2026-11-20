// @vitest-environment node
import { expect, test } from "vitest";
import {
  adoptableBootGeneration,
  type BootGenerationAction,
  compareBootGeneration,
  DAEMONLESS_BOOT_GENERATION,
  liveBootGeneration,
  retiresBootGeneration,
  UNKNOWN_OWNER_BOOT_GENERATION,
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
  // unknown-owner is no counter and names no one: it applies only to itself,
  // and replaces or is replaced by everything else.
  [UNKNOWN_OWNER_BOOT_GENERATION, UNKNOWN_OWNER_BOOT_GENERATION, "apply"],
  ["3", UNKNOWN_OWNER_BOOT_GENERATION, "replace"],
  [UNKNOWN_OWNER_BOOT_GENERATION, "3", "replace"],
  [UNKNOWN_OWNER_BOOT_GENERATION, "2", "replace"],
  [DAEMONLESS_BOOT_GENERATION, UNKNOWN_OWNER_BOOT_GENERATION, "replace"],
  [UNKNOWN_OWNER_BOOT_GENERATION, "3@root1", "replace"],
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

// An older peer omits the field entirely, so the comparator has to be total
// over the wire's optional token: an omitted token is the nothing-held the
// daemon spells "".
test("an omitted token compares as nothing held", () => {
  expect(compareBootGeneration(undefined, "1")).toBe("replace");
  expect(compareBootGeneration("1", undefined)).toBe("replace");
  expect(compareBootGeneration(undefined, DAEMONLESS_BOOT_GENERATION)).toBe("replace");
  expect(compareBootGeneration(undefined, undefined)).toBe("apply");
  expect(compareBootGeneration("", undefined)).toBe("apply");
});

// adoptableBootGeneration is the one gate between a wire token and the store's
// adopted source: only a token that names a generation gets through.
test("only a token that names a generation is adoptable", () => {
  expect(adoptableBootGeneration("4")).toBe("4");
  expect(adoptableBootGeneration("4@root1")).toBe("4@root1");
  expect(adoptableBootGeneration(DAEMONLESS_BOOT_GENERATION)).toBe(DAEMONLESS_BOOT_GENERATION);
  expect(adoptableBootGeneration(UNKNOWN_OWNER_BOOT_GENERATION)).toBeUndefined();
  expect(adoptableBootGeneration(undefined)).toBeUndefined();
  expect(adoptableBootGeneration("")).toBeUndefined();
});

// Retirement is the comparator's "ignore" alone: a sample the adopted
// generation provably moved past. A sample from a different owner (a
// replace), or one whose publisher named nothing, proves nothing and is kept.
test("only a provably older sample is retired", () => {
  expect(retiresBootGeneration("4", "3")).toBe(true);
  expect(retiresBootGeneration("10", "9")).toBe(true);
  expect(retiresBootGeneration("4@root1", "3@root1")).toBe(true);
  expect(retiresBootGeneration("4", "4")).toBe(false);
  expect(retiresBootGeneration("4", "5")).toBe(false);
  expect(retiresBootGeneration("4", "3@root1")).toBe(false);
  expect(retiresBootGeneration("4@root1", "3@root2")).toBe(false);
  expect(retiresBootGeneration("4", DAEMONLESS_BOOT_GENERATION)).toBe(false);
  expect(retiresBootGeneration(DAEMONLESS_BOOT_GENERATION, "4")).toBe(false);
  expect(retiresBootGeneration("4", UNKNOWN_OWNER_BOOT_GENERATION)).toBe(false);
  expect(retiresBootGeneration("4", undefined)).toBe(false);
  expect(retiresBootGeneration("4", "")).toBe(false);
  // Nothing adopted retires nothing: a caller holding no source can ask
  // without guarding the question.
  expect(retiresBootGeneration(undefined, "3")).toBe(false);
});

// A *success* carrying daemonless is saved-history provenance, not proof the
// daemon exited (docs/product/history-content.md: "A successful saved fallback
// carries daemonless ... a client must not retire a standing hold on it"). Only
// a failed read attests absence, and that caller keeps adoptableBootGeneration.
test("a successful read adopts a live generation but never daemonless", () => {
  expect(liveBootGeneration("4")).toBe("4");
  expect(liveBootGeneration("4@root1")).toBe("4@root1");
  expect(liveBootGeneration(DAEMONLESS_BOOT_GENERATION)).toBeUndefined();
  expect(liveBootGeneration(UNKNOWN_OWNER_BOOT_GENERATION)).toBeUndefined();
  expect(liveBootGeneration(undefined)).toBeUndefined();
  expect(liveBootGeneration("")).toBeUndefined();
});
