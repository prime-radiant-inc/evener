// StatusBar: the glance surface of the zoom system. These tests pin the
// counts and crumbs against the shared fixture tree, the chip-to-sidebar
// escalation, re-scoping on drill (a focused subagent transcript), the hidden
// tasks chip when no task list exists, and the armed-watch count including
// hub-omitted rows.

import type { NavigationManifest } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test, vi } from "vitest";
import { navigationStore } from "../../stores/navigation/store";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { resetFocusedActivityScopeForTests } from "../focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { StatusBar } from "./StatusBar";
import { sampleTree, summaryOf } from "./scopeTestUtils";

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

function installTree(opts?: { omitWatches?: boolean }) {
  const { ROOT_A, CHILD_B, CHILD_C, ROOT_D } = sampleTree();
  const leaf = opts?.omitWatches
    ? summaryOf({ ...CHILD_B, watches: undefined, omitted_watches: 2, omitted_armed_watches: 1 })
    : CHILD_B;
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  const subagentsKey: ResourceKey = { kind: "subagents", ref: "local:a", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [ROOT_A, ROOT_D] })],
      [keyID(subagentsKey), resource(subagentsKey, { sessions: [leaf, CHILD_C], remaining: 201, truncated: false })],
      [
        keyID({ kind: "location", ref: "local:a" }),
        resource(
          { kind: "location", ref: "local:a" },
          { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT_A },
        ),
      ],
      [
        keyID({ kind: "location", ref: "local:b" }),
        resource(
          { kind: "location", ref: "local:b" },
          { ref: "local:b", top_level_ref: "local:a", top_level: false, session: leaf },
        ),
      ],
    ]),
    expanded: new Map(),
    attention: { changed: [], summary: manifest().attentionSummary },
  });
}

function openSession(ref: string) {
  workspaceStore.getState().openPane("session", { ref });
}

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
  resetActivitySidebarStoreForTests();
});

describe("StatusBar", () => {
  test("renders the scope crumb and the counters for the focused session", () => {
    installTree();
    openSession("local:a");
    render(<StatusBar />);
    // The leaf crumb is current text, never a link to itself.
    expect(screen.getByText("A")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Agents, 1 active/ }).textContent).toContain("1");
    expect(screen.getByRole("button", { name: /Jobs/ }).textContent).toContain("0");
    expect(screen.getByRole("button", { name: /Watches/ }).textContent).toContain("0");
    // Root A carries no task list: the tasks chip hides (its own test pins it).
    expect(screen.queryByRole("button", { name: /Tasks/ })).toBeNull();
  });

  test("a chip click opens the sidebar on the matching tab", () => {
    installTree();
    openSession("local:a");
    render(<StatusBar />);
    fireEvent.click(screen.getByRole("button", { name: /Watches/ }));
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarStore.getState().tab).toBe("watches");
  });

  test("mounting fetches the scope's subagents page when none exists", () => {
    installTree();
    const subagentsLoad = vi
      .spyOn(navigationStore.getState(), "loadSubagents")
      .mockRejectedValue(new Error("no client") as never);
    try {
      // local:d has no subagents page in the fixture; local:a does.
      openSession("local:d");
      render(<StatusBar />);
      expect(subagentsLoad).toHaveBeenCalledWith("local:d");
    } finally {
      subagentsLoad.mockRestore();
    }
  });

  test("an existing page is never re-fetched", () => {
    installTree();
    const subagentsLoad = vi
      .spyOn(navigationStore.getState(), "loadSubagents")
      .mockRejectedValue(new Error("no client") as never);
    try {
      // local:a's page is installed in the fixture.
      openSession("local:a");
      render(<StatusBar />);
      expect(subagentsLoad).not.toHaveBeenCalledWith("local:a");
    } finally {
      subagentsLoad.mockRestore();
    }
  });

  test("a drilled scope also fetches the root's page (the crumb walks it)", () => {
    installTree();
    // Remove the root's page: the drill's crumb must fetch it.
    const resources = new Map(navigationStore.getState().resources);
    resources.delete(keyID({ kind: "subagents", ref: "local:a", offset: 0, limit: 50 }));
    navigationStore.setState({ resources });
    const subagentsLoad = vi
      .spyOn(navigationStore.getState(), "loadSubagents")
      .mockRejectedValue(new Error("no client") as never);
    try {
      openSession("local:a");
      workspaceStore.getState().openPane("transcript", { ref: "local:b", parentRef: "local:a" }, { slot: "secondary" });
      render(<StatusBar />);
      expect(subagentsLoad).toHaveBeenCalledWith("local:b");
      expect(subagentsLoad).toHaveBeenCalledWith("local:a");
    } finally {
      subagentsLoad.mockRestore();
    }
  });

  test("drilling re-scopes: a focused subagent transcript shows the path and the child's counts", () => {
    installTree();
    openSession("local:a");
    workspaceStore.getState().openPane("transcript", { ref: "local:b", parentRef: "local:a" }, { slot: "secondary" });
    render(<StatusBar />);
    expect(screen.getByRole("button", { name: "A" })).toBeTruthy();
    expect(screen.getByText("B")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Jobs/ }).textContent).toContain("1");
    expect(screen.getByRole("button", { name: /Watches/ }).textContent).toContain("1");
    expect(screen.getByRole("button", { name: /Tasks/ }).textContent).toContain("2/5");
  });

  test("the tasks chip hides when the scope has no task list", () => {
    installTree();
    openSession("local:a");
    render(<StatusBar />);
    // Root A carries no task list: only three chips render.
    expect(screen.queryByRole("button", { name: /Tasks/ })).toBeNull();
  });

  test("the watches chip counts hub-omitted armed rows", () => {
    installTree({ omitWatches: true });
    openSession("local:a");
    workspaceStore.getState().openPane("transcript", { ref: "local:b", parentRef: "local:a" }, { slot: "secondary" });
    render(<StatusBar />);
    // No retained rows, one armed row omitted by the hub: the chip still says 1.
    expect(screen.getByRole("button", { name: /Watches/ }).textContent).toContain("1");
  });

  test("renders nothing when no session has ever been focused", () => {
    installTree();
    const { container } = render(<StatusBar />);
    expect(container.firstChild).toBeNull();
  });
});
