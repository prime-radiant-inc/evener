import { test, expect } from "vitest";
import { systemRunFor, shouldGroup } from "./systemGrouping";
import type { ItemModel } from "../../../../protocol/model";

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
  expect(
    shouldGroup({ items: [item("a", "systemMessage"), item("b", "systemMessage")], isFirst: true }),
  ).toBe(false);
});

test("shouldGroup: a run of exactly 3 groups", () => {
  const items = [item("a", "systemMessage"), item("b", "systemMessage"), item("c", "systemMessage")];
  expect(shouldGroup({ items, isFirst: true })).toBe(true);
});

test("shouldGroup: a run longer than 3 groups", () => {
  const items = Array.from({ length: 5 }, (_, i) => item(String(i), "systemMessage"));
  expect(shouldGroup({ items, isFirst: true })).toBe(true);
});
