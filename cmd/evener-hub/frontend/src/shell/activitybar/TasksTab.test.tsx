// The Tasks tab of the activity sidebar: the navigation summary plus the Open
// affordance into the real tasks pane, and the honest empty state.

import type { NavigationManifest } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { navigationStore } from "../../stores/navigation/store";
import { resetFocusedActivityScopeForTests } from "../focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "../paneRegistry";
import { summaryOf } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

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
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("sessionTasks")));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

function manifest(): NavigationManifest {
  return {
    generation_id: "g1",
    revision: 1,
    sources: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  } as NavigationManifest;
}

function resource<T>(key: ResourceKey, data: T): ResourceState {
  return {
    key,
    data,
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "e",
    loading: false,
    stale: false,
    error: null,
    generationID: "g1",
  } as ResourceState;
}

function install(ref: string, tasks?: { total: number; done: number; current?: string }) {
  const root = summaryOf({ ref, title: "A", state: "active", tasks });
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [root] })],
      [
        keyID({ kind: "location", ref }),
        resource({ kind: "location", ref }, { ref, top_level_ref: ref, top_level: true, session: root }),
      ],
    ]),
    expanded: new Map(),
    attention: { changed: [], summary: manifest().attentionSummary },
  });
  workspaceStore.getState().openPane("session", { ref });
  activitySidebarStore.getState().openWith("tasks");
}

function renderSidebar() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
}

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
  resetActivitySidebarStoreForTests();
});

describe("TasksTab", () => {
  test("renders the summary line with the current task", () => {
    install("local:a", { total: 5, done: 2, current: "doing the thing" });
    renderSidebar();
    expect(screen.getByText(/2 of 5 done/)).toBeTruthy();
    expect(screen.getByText(/now: doing the thing/)).toBeTruthy();
  });

  test("Open tasks opens the tasks pane beside the session", () => {
    install("local:a", { total: 5, done: 2 });
    renderSidebar();
    fireEvent.click(screen.getByRole("button", { name: "Open tasks" }));
    const pane = workspaceStore.getState().panes.find((p) => p.type === "sessionTasks");
    expect(pane?.params).toEqual({ ref: "local:a" });
    expect(pane?.slot).toBe("secondary");
  });

  test("no task list reads as an honest empty state with no Open affordance", () => {
    install("local:b");
    renderSidebar();
    expect(screen.getByText("No task list for this session.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Open tasks" })).toBeNull();
  });
});
