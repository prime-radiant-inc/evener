// @vitest-environment node
import { expect, test } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import { shouldGroup, systemRunFor } from "./systemGrouping";

function item(id: string, type: string): ItemModel {
  return { id, turnId: "turn_1", type, text: `text-${id}` };
}

test("an item not present in the list resolves to no run", () => {
  const items = [item("a", "systemMessage")];
  expect(systemRunFor(items, "missing")).toBeUndefined();
});

test("a non-systemMessage item resolves to no run", () => {
  const items = [item("a", "userMessage")];
  expect(systemRunFor(items, "a")).toBeUndefined();
});

test("a lone systemMessage item is its own run of one, and is first", () => {
  const items = [item("a", "userMessage"), item("b", "systemMessage"), item("c", "userMessage")];
  const run = systemRunFor(items, "b");
  expect(run?.items.map((i) => i.id)).toEqual(["b"]);
  expect(run?.isFirst).toBe(true);
});

test("consecutive systemMessage items form one run regardless of which member is queried", () => {
  const items = [item("a", "systemMessage"), item("b", "systemMessage"), item("c", "systemMessage")];
  for (const id of ["a", "b", "c"]) {
    const run = systemRunFor(items, id);
    expect(run?.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
  }
});

test("isFirst is true only for the run's first member", () => {
  const items = [item("a", "systemMessage"), item("b", "systemMessage"), item("c", "systemMessage")];
  expect(systemRunFor(items, "a")?.isFirst).toBe(true);
  expect(systemRunFor(items, "b")?.isFirst).toBe(false);
  expect(systemRunFor(items, "c")?.isFirst).toBe(false);
});

test("a non-lifecycle entry between two systemMessage items breaks the run in two", () => {
  const items = [
    item("a", "systemMessage"),
    item("b", "systemMessage"),
    item("prose", "agentMessage"),
    item("c", "systemMessage"),
  ];
  expect(systemRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a", "b"]);
  expect(systemRunFor(items, "b")?.items.map((i) => i.id)).toEqual(["a", "b"]);
  expect(systemRunFor(items, "c")?.items.map((i) => i.id)).toEqual(["c"]);
  expect(systemRunFor(items, "c")?.isFirst).toBe(true);
});

test("a run at the very start or end of the items array is bounded correctly (no out-of-range read)", () => {
  const items = [item("a", "systemMessage"), item("b", "userMessage")];
  expect(systemRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a"]);

  const items2 = [item("a", "userMessage"), item("b", "systemMessage")];
  expect(systemRunFor(items2, "b")?.items.map((i) => i.id)).toEqual(["b"]);
});

// --- shouldGroup -------------------------------------------------------------
// Parity: "3+ adjacent lifecycle events coalesce; fewer than 3 don't"
// (contracts-transcript-scroll-liveness.md #12 / test-system-churn.js).

test("shouldGroup: runs of 1 or 2 do not group", () => {
  expect(shouldGroup({ items: [item("a", "systemMessage")], isFirst: true })).toBe(false);
  expect(shouldGroup({ items: [item("a", "systemMessage"), item("b", "systemMessage")], isFirst: true })).toBe(false);
});

test("shouldGroup: a run of exactly 3 groups", () => {
  const items = [item("a", "systemMessage"), item("b", "systemMessage"), item("c", "systemMessage")];
  expect(shouldGroup({ items, isFirst: true })).toBe(true);
});

test("shouldGroup: a run longer than 3 groups", () => {
  const items = Array.from({ length: 5 }, (_, i) => item(String(i), "systemMessage"));
  expect(shouldGroup({ items, isFirst: true })).toBe(true);
});

// --- a turn failure never joins a run (kata 0wb6) ----------------------------
// A persisted turn failure arrives as a systemMessage item carrying the typed
// "error" eventKind. It is the one system item a reader is hunting for, so it
// must never be foldable into a collapsed "N system events · <the FIRST item's
// line>" disclosure - a summary that can describe something else entirely.

function failure(id: string): ItemModel {
  return { id, turnId: "turn_1", type: "systemMessage", text: `text-${id}`, eventKind: "error" };
}

test("a turn failure is its own run of one, and is first, so it always renders itself", () => {
  const run = systemRunFor([failure("boom")], "boom");
  expect(run?.items.map((i) => i.id)).toEqual(["boom"]);
  expect(run?.isFirst).toBe(true);
  expect(shouldGroup(run!)).toBe(false);
});

test("a turn failure surrounded by lifecycle notices still stands alone", () => {
  const items = [item("a", "systemMessage"), failure("boom"), item("b", "systemMessage")];
  expect(systemRunFor(items, "boom")?.items.map((i) => i.id)).toEqual(["boom"]);
  expect(systemRunFor(items, "boom")?.isFirst).toBe(true);
});

test("a turn failure breaks the run around it, so no group can summarise it away", () => {
  const items = [
    item("a", "systemMessage"),
    item("b", "systemMessage"),
    failure("boom"),
    item("c", "systemMessage"),
    item("d", "systemMessage"),
  ];
  expect(systemRunFor(items, "a")?.items.map((i) => i.id)).toEqual(["a", "b"]);
  expect(systemRunFor(items, "c")?.items.map((i) => i.id)).toEqual(["c", "d"]);
  expect(systemRunFor(items, "c")?.isFirst).toBe(true);
});

// The efme rule: whatever leaves a run must not still be COUNTED by it, or the
// group announces more events than it shows. Membership and rendering are
// derived from one predicate here, so a run of two notices either side of a
// failure stays two - never three.
test("a run's count excludes the failure that broke it", () => {
  const items = [item("a", "systemMessage"), item("b", "systemMessage"), failure("boom")];
  const run = systemRunFor(items, "a");
  expect(run?.items).toHaveLength(2);
  expect(shouldGroup(run!)).toBe(false);
});
