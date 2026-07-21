import { test, expect } from "vitest";
import { itemRendererFor, registerItemRenderer } from "./types";
import { RawItemView } from "./RawItemView";

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
