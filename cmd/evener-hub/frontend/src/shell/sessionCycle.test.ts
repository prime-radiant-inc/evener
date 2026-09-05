// @vitest-environment node

// Unit tests for sessionCycle.ts (webui-keybindings-p3 Task 1): the
// session.next/session.previous actions cycle the workspace's SESSION panes
// in workspace (workspaceStore.panes) order, wrapping at both ends, skipping
// every non-session pane, and no-op with fewer than two session panes open.
// Pane-registration scaffolding mirrors workspace.test.ts's own (fixture
// descriptors over the shared paneRegistry singleton).
import { lazy } from "react";
import { afterAll, beforeAll, beforeEach, expect, test } from "vitest";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
import { cycleSessionPane } from "./sessionCycle";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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

const restorePaneFixtures: Array<() => void> = [];

beforeAll(() => {
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("session")));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("settings", { singleton: true })));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("doc")));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

function openSessions(...refs: string[]): string[] {
  return refs.map((ref) => workspaceStore.getState().openPane("session", { ref }));
}

test("next cycles through the session panes in workspace order", () => {
  // The tuple casts: noUncheckedIndexedAccess types destructured elements string|undefined.
  const [a, b, c] = openSessions("local:a", "local:b", "local:c") as [string, string, string];
  workspaceStore.getState().focusPane(a);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(c);
});

test("next wraps from the last session pane back to the first", () => {
  const [a, , c] = openSessions("local:a", "local:b", "local:c") as [string, string, string];
  workspaceStore.getState().focusPane(c);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
});

test("previous cycles backward and wraps from the first to the last", () => {
  // The tuple casts: noUncheckedIndexedAccess types destructured elements string|undefined.
  const [a, b, c] = openSessions("local:a", "local:b", "local:c") as [string, string, string];
  workspaceStore.getState().focusPane(a);
  cycleSessionPane("previous");
  expect(workspaceStore.getState().focusedPaneId).toBe(c);
  cycleSessionPane("previous");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);
});

test("non-session panes in the workspace are skipped", () => {
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().openPane("settings", {});
  workspaceStore.getState().openPane("doc", { session: "local:a", path: "README.md" });
  const b = workspaceStore.getState().openPane("session", { ref: "local:b" });
  workspaceStore.getState().focusPane(a);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
});

test("no-op when no session pane is open", () => {
  const settings = workspaceStore.getState().openPane("settings", {});
  workspaceStore.getState().focusPane(settings);
  cycleSessionPane("next");
  cycleSessionPane("previous");
  expect(workspaceStore.getState().focusedPaneId).toBe(settings);
});

test("no-op when only one session pane is open", () => {
  const [only] = openSessions("local:only") as [string];
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
  cycleSessionPane("previous");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
});

test("with focus on a non-session pane, next focuses the first session and previous the last", () => {
  const [a, , c] = openSessions("local:a", "local:b", "local:c") as [string, string, string];
  const settings = workspaceStore.getState().openPane("settings", {});
  workspaceStore.getState().focusPane(settings);
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  workspaceStore.getState().focusPane(settings);
  cycleSessionPane("previous");
  expect(workspaceStore.getState().focusedPaneId).toBe(c);
});

test("no focused pane at all still lands on a session pane rather than throwing", () => {
  const [a] = openSessions("local:a", "local:b") as [string];
  workspaceStore.setState({ focusedPaneId: null });
  cycleSessionPane("next");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
});
