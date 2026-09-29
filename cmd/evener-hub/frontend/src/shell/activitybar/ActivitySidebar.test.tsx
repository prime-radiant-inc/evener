// ActivitySidebar: the triage surface of the zoom system. Opened from a
// status-bar chip (or the session chrome's Activity button), scoped to the
// leaf you're reading, with the four kind tabs. These tests pin the
// breadcrumb, the tab counts, the Agents tab's current/inactive split with
// its fold and pagination, the drill opening a transcript pane split right,
// the closed state rendering nothing, and the reduced-motion contract.

import type { NavigationManifest } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { navigationStore } from "../../stores/navigation/store";
import { resetFocusedActivityScopeForTests } from "../focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "../paneRegistry";
import { sampleTree } from "../statusbar/scopeTestUtils";
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

function installTree() {
  const { ROOT_A, ROOT_D } = sampleTree();
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [ROOT_A, ROOT_D] })],
      [
        keyID({ kind: "location", ref: "local:a" }),
        resource(
          { kind: "location", ref: "local:a" },
          { ref: "local:a", top_level_ref: "local:a", top_level: true, session: ROOT_A },
        ),
      ],
    ]),
    expanded: new Map(),
    attention: { changed: [], summary: manifest().attentionSummary },
  });
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

describe("ActivitySidebar", () => {
  test("renders nothing while closed", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    const { container } = renderSidebar();
    expect(container.firstChild).toBeNull();
  });

  test("open shows the breadcrumb and the four tabs with counts", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("A")).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Agents 1/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Jobs 0/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Watches 0/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Tasks 0\/0/ })).toBeTruthy();
  });

  test("agents tab lists current children and folds the inactive behind their true total", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("B")).toBeTruthy();
    // C folds; the fold's count includes the 201 the wire never carried.
    expect(screen.getByRole("button", { name: /Inactive subagents \(202\)/ })).toBeTruthy();
    expect(screen.queryByText("C")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Inactive subagents \(202\)/ }));
    expect(screen.getByText("C")).toBeTruthy();
    // The unloaded remainder is a passive note, never a fake control.
    expect(screen.getByText(/\+201 more/)).toBeTruthy();
  });

  test("clicking an agent row drills: the transcript pane opens in the secondary slot", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    fireEvent.click(screen.getByRole("button", { name: /^B/ }));
    const panes = workspaceStore.getState().panes;
    const transcript = panes.find((p) => p.type === "transcript");
    expect(transcript?.params).toEqual({ ref: "local:b", parentRef: "local:a" });
    expect(transcript?.slot).toBe("secondary");
  });

  test("the close button returns to bar-only", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    fireEvent.click(screen.getByRole("button", { name: /Close/ }));
    expect(activitySidebarStore.getState().open).toBe(false);
  });

  // Animation frames don't run in jsdom, so the entrance's look is verified
  // in the browser; what pins here is the reduced-motion contract end to end:
  // with reduce on, the aside mounts at final geometry with no transform and
  // no error, through MotionProvider's reducedMotion="user".
  test("prefers-reduced-motion mounts the sidebar at final geometry", () => {
    const reduce = (query: string) => ({
      matches: query.includes("reduce"),
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
      onchange: null,
      dispatchEvent: () => false,
    });
    const original = window.matchMedia;
    window.matchMedia = reduce as unknown as typeof window.matchMedia;
    try {
      installTree();
      workspaceStore.getState().openPane("session", { ref: "local:a" });
      activitySidebarStore.getState().openWith("agents");
      const { container } = renderSidebar();
      const aside = container.querySelector("aside");
      expect(aside).not.toBeNull();
      expect(aside?.style.transform ?? "").not.toContain("translateX");
      expect(screen.getByText("A")).toBeTruthy();
    } finally {
      window.matchMedia = original;
    }
  });
});
