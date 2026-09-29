// The focused-activity-scope reader: which session the bar and sidebar
// describe, through pane focus changes of every pane type.
// Pane-registration scaffolding mirrors sessionCycle.test.ts (fixture
// descriptors over the shared paneRegistry singleton).

import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
import { focusedActivityScopeRef, focusedSessionRef, resetFocusedActivityScopeForTests } from "./focusedSession";
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
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("transcript")));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("settings", { singleton: true })));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

afterEach(() => {
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
});

describe("focusedSessionRef", () => {
  test("reads the focused session pane's ref", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    expect(focusedSessionRef()).toBe("local:a");
  });

  test("is null when a non-session pane is focused", () => {
    workspaceStore.getState().openPane("settings", {});
    expect(focusedSessionRef()).toBeNull();
  });
});

describe("focusedActivityScopeRef", () => {
  test("follows a session pane", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    expect(focusedActivityScopeRef()).toBe("local:a");
  });

  test("follows a drilled subagent's transcript pane to the child", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("transcript", { ref: "local:b", parentRef: "local:a" }, { slot: "secondary" });
    expect(focusedActivityScopeRef()).toBe("local:b");
  });

  test("a job-log transcript keeps its parent session's scope", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("transcript", { ref: "job:j1", parentRef: "local:a" }, { slot: "secondary" });
    expect(focusedActivityScopeRef()).toBe("local:a");
  });

  test("focusing settings keeps the last session's scope instead of blanking", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("settings", {});
    expect(focusedActivityScopeRef()).toBe("local:a");
  });

  test("is null until any session has been focused", () => {
    expect(focusedActivityScopeRef()).toBeNull();
  });
});
