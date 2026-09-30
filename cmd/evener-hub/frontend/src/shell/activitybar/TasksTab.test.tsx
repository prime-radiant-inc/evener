// The Tasks tab of the activity sidebar: the task list itself, unfolded in
// the sidebar - the same TasksPanelBody the tasks pane renders, fed by the
// shared useThreadModel subscription. No pane affordance anywhere.

import type { NavigationManifest } from "@evener/appwire-client";
import { hydrateThread } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { connectionStore } from "../../stores/connection";
import { navigationStore } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
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

function modelFor(ref: string, tasks: { total: number; done: number } | null) {
  const model = hydrateThread(
    {
      thread: {
        id: `thread_${ref}`,
        sessionId: ref,
        preview: "",
        ephemeral: false,
        modelProvider: "anthropic",
        createdAt: 1000,
        updatedAt: 1000,
        status: { type: "running" },
        cwd: "/tmp/project",
        cliVersion: "1.0.0",
        source: "evener",
        evener: {
          ref,
          capabilities: {
            send: false,
            steer: false,
            interrupt: false,
            compact: false,
            clear: false,
            forkFromTurn: false,
            shutdown: false,
            changeModel: false,
            changeVisionModel: false,
            queue: false,
            goal: false,
            sharedNotes: false,
            rename: false,
          },
          queue: { revision: 0 },
          humanNote: "",
        },
      },
    },
    ref,
    0,
  );
  return { ...model, tasks };
}

function renderSidebar() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("TasksTab", () => {
  test("no hydrated model yet reads as a loading state, never an empty list", () => {
    install("local:a", { total: 5, done: 2 });
    renderSidebar();
    expect(screen.getByText("Loading tasks…")).toBeTruthy();
  });

  test("mounting the tab subscribes to the session's thread (the body's data source)", () => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    const ensure = vi.spyOn(threadsStore.getState(), "ensureThread").mockResolvedValue(undefined as never);
    try {
      install("local:a", { total: 5, done: 2 });
      renderSidebar();
      expect(ensure).toHaveBeenCalledWith("local:a");
    } finally {
      ensure.mockRestore();
    }
  });

  test("with the model hydrated, the task list renders in the tab and no pane opens", async () => {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/tasks/list", () => ({
      data: [
        { id: 1, type: "implement", description: "Wire up the status row", prompt: "", status: "done" },
        { id: 2, type: "implement", description: "Gate green", prompt: "", status: "open" },
      ],
    }));
    install("local:a", { total: 2, done: 1, current: "Gate green" });
    threadsStore.setState({ threads: new Map([["local:a", modelFor("local:a", { total: 2, done: 1 })]]) });
    renderSidebar();
    // The navigation summary line rides on top.
    expect(screen.getByText(/1 of 2 done/)).toBeTruthy();
    // The body's rows render inline - the list unfolds here, never in a
    // pane: the open task visible, the done one in its collapsed group.
    await waitFor(() => expect(screen.getByText("Gate green")).toBeTruthy());
    expect(screen.getByTestId("task-settled-group").textContent).toContain("Done · settled");
    expect(screen.queryByRole("button", { name: "Open tasks" })).toBeNull();
    expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionTasks")).toBe(false);
  });
});
