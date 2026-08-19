import { expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { RawItemView } from "./RawItemView";
import { ignoringTurn, itemRendererFor, registerItemRenderer } from "./types";

// Dummy stand-ins - these tests are about registry MECHANICS (does a
// registration resolve, does it override, does it stay scoped to its own
// type), not about rendering, so no render() is needed here. No parameter
// declared: a zero-arg function is structurally assignable to
// ComponentType<ItemRenderProps> (it simply ignores whatever props it's
// given).
function DummyA() {
  return null;
}
function DummyB() {
  return null;
}

test("an unregistered type falls back to RawItemView", () => {
  expect(itemRendererFor("types-test-unregistered")).toBe(RawItemView);
});

test("registerItemRenderer makes a type resolve to the registered component", () => {
  registerItemRenderer("types-test-a", DummyA);
  expect(itemRendererFor("types-test-a")).toBe(DummyA);
});

test("registering a second component for the same type overrides the first", () => {
  registerItemRenderer("types-test-b", DummyA);
  registerItemRenderer("types-test-b", DummyB);
  expect(itemRendererFor("types-test-b")).toBe(DummyB);
});

test("registering one type does not affect lookup of a different, unregistered type", () => {
  registerItemRenderer("types-test-c", DummyA);
  expect(itemRendererFor("types-test-d-never-registered")).toBe(RawItemView);
});

// ignoringTurn: the memo comparator wave-4 T5c wraps most registered item
// renderers with (see each renderer's own registerItemRenderer call site).
// Streaming deltas rebuild the enclosing TurnModel on every delta while an
// unchanged item keeps its exact reference (reducer.ts's immutable-update
// discipline) - a renderer that reads nothing off `turn` should not
// re-render just because a SIBLING item changed, which is exactly what this
// comparator is for.
function turnModel(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "inProgress", items: [], ...overrides };
}

function itemModel(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "x", text: "", ...overrides };
}

test("ignoringTurn: same item reference and same live, a different turn object -> treated as equal (no re-render)", () => {
  const item = itemModel();
  const prev = { item, turn: turnModel({ id: "turn_a" }), live: false };
  const next = { item, turn: turnModel({ id: "turn_b" }), live: false };
  expect(ignoringTurn(prev, next)).toBe(true);
});

test("ignoringTurn: a different item reference -> treated as different (re-renders)", () => {
  const turn = turnModel();
  const prev = { item: itemModel({ text: "a" }), turn, live: false };
  const next = { item: itemModel({ text: "b" }), turn, live: false };
  expect(ignoringTurn(prev, next)).toBe(false);
});

test("ignoringTurn: a different live value -> treated as different (re-renders)", () => {
  const item = itemModel();
  const turn = turnModel();
  expect(ignoringTurn({ item, turn, live: false }, { item, turn, live: true })).toBe(false);
});

test("ignoringTurn re-renders when opensExchange or agentLabel changes", () => {
  const base = { item: {} as never, turn: {} as never, live: false };
  expect(ignoringTurn(base, { ...base, opensExchange: true })).toBe(false);
  expect(ignoringTurn(base, { ...base, agentLabel: "k3" })).toBe(false);
  expect(ignoringTurn({ ...base, opensExchange: true }, { ...base, opensExchange: true })).toBe(true);
});
