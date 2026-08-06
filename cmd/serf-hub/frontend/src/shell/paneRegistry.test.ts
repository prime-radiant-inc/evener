// @vitest-environment node
import { lazy } from "react";
import { afterEach, expect, test } from "vitest";
import { type PaneDescriptor, type PaneProps, paneFor, registerPaneForTests } from "./paneRegistry";

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
// restores whatever was there before that test ran, keeping the leak from
// reaching whichever file runs next in the same worker.
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
  // "transcript" is never registered anywhere in this file.
  expect(() => paneFor("transcript")).toThrow(/transcript/);
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
