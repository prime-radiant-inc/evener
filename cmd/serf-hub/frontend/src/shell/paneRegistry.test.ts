import { lazy } from "react";
import { test, expect } from "vitest";
import { paneFor, registerPane, type PaneDescriptor, type PaneProps } from "./paneRegistry";

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

test("registerPane makes a descriptor retrievable by paneFor via its id", () => {
  const descriptor = fixtureDescriptor("doc");

  registerPane(descriptor);

  expect(paneFor("doc")).toBe(descriptor);
});

test("paneFor throws a clear error for an id that was never registered", () => {
  // "transcript" is never registered anywhere in this file.
  expect(() => paneFor("transcript")).toThrow(/transcript/);
});

test("paneFor preserves singleton: true as registered", () => {
  const descriptor = fixtureDescriptor("spawn", { singleton: true });

  registerPane(descriptor);

  expect(paneFor("spawn").singleton).toBe(true);
});

test("paneFor preserves an omitted singleton as undefined", () => {
  const descriptor = fixtureDescriptor("session");

  registerPane(descriptor);

  expect(paneFor("session").singleton).toBeUndefined();
});
