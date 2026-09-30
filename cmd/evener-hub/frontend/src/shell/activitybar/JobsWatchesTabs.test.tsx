// The Jobs and Watches tabs of the activity sidebar: ordering, status
// wording, glyph tones, drill/open behavior, and the omitted-watches grammar.

import type { NavigationManifest } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { navigationStore } from "../../stores/navigation/store";
import { resetFocusedActivityScopeForTests } from "../focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "../paneRegistry";
import { summaryOf, watchOf } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import styles from "./activitybar.module.css";
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
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("transcript")));
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

// Root A with a full activity load: one running job, two completed (one
// failed), two watches (one armed, one fired), and hub-omitted watch rows.
const ROOT = summaryOf({
  ref: "local:a",
  title: "A",
  state: "active",
  running_jobs: [{ job_id: "j1", job_type: "shell", status: "running", command: "go test ./..." }],
  completed_jobs: [
    { job_id: "j0", job_type: "shell", status: "command_exited_nonzero", command: "make lint" },
    { job_id: "j_1", job_type: "shell", status: "completed", command: "npm run build" },
  ],
  watches: [
    watchOf({ id: "w1", note: "test heartbeat", cadence: [{ kind: "every", seconds: 300 }] }),
    watchOf({
      id: "w2",
      note: "build landed",
      cadence: [{ kind: "after", seconds: 600 }],
      active: false,
      deliveries: 1,
    }),
  ],
  omitted_watches: 2,
  omitted_armed_watches: 1,
  tasks: { total: 5, done: 2, current: "doing the thing" },
});

function installRoot() {
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [ROOT] })],
      [
        keyID({ kind: "location", ref: "local:a" }),
        resource(
          { kind: "location", ref: "local:a" },
          { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT },
        ),
      ],
    ]),
    expanded: new Map(),
    attention: { changed: [], summary: manifest().attentionSummary },
  });
  workspaceStore.getState().openPane("session", { ref: "local:a" });
}

function renderSidebar(tab: "jobs" | "watches") {
  activitySidebarStore.getState().openWith(tab);
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

describe("JobsTab", () => {
  test("lists running jobs before completed, with status wording", () => {
    installRoot();
    renderSidebar("jobs");
    const names = screen.getAllByText(/go test|make lint|npm run build/);
    expect(names.map((n) => n.textContent)).toEqual(["go test ./...", "make lint", "npm run build"]);
    expect(screen.getByText("Command failed")).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("completed")).toBeTruthy();
  });

  test("a failed job's glyph carries the danger tone", () => {
    installRoot();
    const { container } = renderSidebar("jobs");
    expect(container.querySelector(`.${styles.glyphDanger}`)).not.toBeNull();
  });

  test("clicking a job opens its transcript pane beside the session", () => {
    installRoot();
    renderSidebar("jobs");
    fireEvent.click(screen.getByText("make lint"));
    const transcript = workspaceStore.getState().panes.find((p) => p.type === "transcript");
    expect(transcript?.params).toEqual({ ref: "job:j0", parentRef: "local:a" });
    expect(transcript?.slot).toBe("secondary");
  });
});

describe("WatchesTab", () => {
  test("renders the shared cadence wording and the omitted-rows grammar", () => {
    installRoot();
    renderSidebar("watches");
    expect(screen.getByText("test heartbeat")).toBeTruthy();
    expect(screen.getByText(/every 5m/)).toBeTruthy();
    expect(screen.getByText("build landed")).toBeTruthy();
    // The hub omitted 2 rows (1 armed). The footer's armed total is the TRUE
    // total - the retained armed row plus the omitted armed one - in the
    // rail's own grammar.
    expect(screen.getByText("2 watches · 2 armed total · +2 more")).toBeTruthy();
  });

  test("the empty state reads honestly when nothing watches", () => {
    const bare = summaryOf({ ref: "local:bare", title: "Bare", state: "active" });
    const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
    navigationStore.setState({
      mode: "v2",
      capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
      clientGenerationID: "g1",
      manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [bare] })],
        [
          keyID({ kind: "location", ref: "local:bare" }),
          resource(
            { kind: "location", ref: "local:bare" },
            { ref: "local:bare", top_level_ref: "local:bare", top_level: true, session: bare },
          ),
        ],
      ]),
      expanded: new Map(),
      attention: { changed: [], summary: manifest().attentionSummary },
    });
    workspaceStore.getState().openPane("session", { ref: "local:bare" });
    renderSidebar("watches");
    expect(screen.getByText("No watches at this level.")).toBeTruthy();
  });
});
