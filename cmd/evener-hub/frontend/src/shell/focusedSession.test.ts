// The focused-activity-scope reader: which session the bar and sidebar
// describe, through pane focus changes of every pane type.
// Pane-registration scaffolding mirrors sessionCycle.test.ts (fixture
// descriptors over the shared paneRegistry singleton).

import { act, cleanup, renderHook } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import {
  focusedActivityScopeRef,
  focusedSessionRef,
  resetFocusedActivityScopeForTests,
  useFocusedActivityScopeRef,
} from "./focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
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
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("doc")));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("settings", { singleton: true })));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

afterEach(() => {
  cleanup();
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

  test("focusing settings with the session still open in a secondary tab keeps its scope", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("doc", { session: "local:a", path: "README.md" }, { slot: "secondary" });
    workspaceStore.getState().openPane("settings", {}, { slot: "secondary" });
    // The focused secondary tab is settings, but the main pane still shows the
    // session: the scope is what's showing (the rail's selected row agrees).
    expect(focusedActivityScopeRef()).toBe("local:a");
  });

  test("is null when no session is open anywhere", () => {
    workspaceStore.getState().openPane("settings", {});
    expect(focusedActivityScopeRef()).toBeNull();
  });

  test("a doc pane scopes to the session it documents", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("doc", { session: "local:a", path: "README.md" }, { slot: "secondary" });
    expect(focusedActivityScopeRef()).toBe("local:a");
  });

  test("the hook re-renders subscribers on a pure focus switch, no pane-set change needed", () => {
    const { result } = renderHook(() => useFocusedActivityScopeRef());
    let main = "";
    act(() => {
      main = workspaceStore.getState().openPane("session", { ref: "local:a" });
    });
    expect(result.current).toBe("local:a");
    act(() => {
      workspaceStore.getState().openPane("transcript", { ref: "local:b", parentRef: "local:a" }, { slot: "secondary" });
    });
    expect(result.current).toBe("local:b");
    // The sticky-✓ bug the altitude review caught: focusing an EXISTING pane
    // changes nothing but focusedPaneId; readers must still update.
    act(() => workspaceStore.getState().focusPane(main));
    expect(result.current).toBe("local:a");
  });
});
