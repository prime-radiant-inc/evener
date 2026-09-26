// @vitest-environment node
import { lazy } from "react";
import { afterEach, expect, test } from "vitest";
import { type PaneDescriptor, type PaneProps, type PaneTypeId, paneFor, registerPaneForTests } from "./paneRegistry";

// A minimal descriptor fixture. `component` must be a LazyExoticComponent
// per the locked PaneDescriptor shape (see the wave-3 plan's Locked
// interfaces) - lazy() around a never-awaited dynamic import is enough for
// registry-mechanics tests, which never render the component.
function fixtureDescriptor<P>(
  id: PaneDescriptor<P>["id"],
  overrides: Partial<PaneDescriptor<P>> = {},
): PaneDescriptor<P> {
  return {
    id,
    title: () => `title for ${id}`,
    component: lazy(() => new Promise<{ default: React.ComponentType<PaneProps<P>> }>(() => {})),
    ...overrides,
  };
}

// paneRegistry.ts is a shared module singleton, not fresh per test - each
// test below registers over a real PaneTypeId (doc/spawn/session), so this
// restores whatever was there before that test ran, keeping one test's
// fixture descriptor from reaching the next test.
let restorePane: (() => void) | undefined;

afterEach(() => {
  restorePane?.();
  restorePane = undefined;
});

test("registerPane makes a descriptor retrievable by paneFor via its id", () => {
  const descriptor = fixtureDescriptor("doc");

  restorePane = registerPaneForTests(descriptor);

  expect(paneFor("doc")).toBe(descriptor);
});

test("paneFor throws a clear error for an id that was never registered", () => {
  // Every real PaneTypeId ("session"/"transcript"/"doc"/"spawn"/"settings"/
  // "welcome") gets registered by its own production module at import time,
  // and other tests here register over them. An id outside the closed union
  // (cast past the type check, simulating a corrupted/impossible id) is the
  // only one guaranteed to stay unregistered whatever this file imports and
  // whichever tests ran first.
  expect(() => paneFor("not-a-real-pane-type" as PaneTypeId)).toThrow(/not-a-real-pane-type/);
});

test("paneFor preserves singleton: true as registered", () => {
  const descriptor = fixtureDescriptor("spawn", { singleton: true });

  restorePane = registerPaneForTests(descriptor);

  expect(paneFor("spawn").singleton).toBe(true);
});

test("paneFor preserves an omitted singleton as undefined", () => {
  const descriptor = fixtureDescriptor("session");

  restorePane = registerPaneForTests(descriptor);

  expect(paneFor("session").singleton).toBeUndefined();
});
