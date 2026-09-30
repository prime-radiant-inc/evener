// ActivitySidebar: the triage surface of the zoom system. Opened from a
// status-bar chip (or the session chrome's Activity button), scoped to the
// leaf you're reading, with the four kind tabs. These tests pin the
// breadcrumb, the tab counts, the Agents tab's current/inactive split with
// its fold and pagination, the drill opening a transcript pane split right,
// the closed state rendering nothing, and the reduced-motion contract.

import type { NavigationManifest } from "@evener/appwire-client";
import { createKeybindingsRegistry } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { parseKeybinding } from "tinykeys";
import { afterAll, afterEach, beforeAll, describe, expect, test, vi } from "vitest";
import { createKeybindingDispatcher } from "../../keybindings/dispatcher";

// Counts deriveScope calls through the REAL implementation (the wrap keeps
// every other test's behavior identical) so the closed-sidebar test can
// prove no derivation happens while nothing is showing.
const deriveScopeCalls = vi.hoisted(() => ({ count: 0 }));
vi.mock("../statusbar/statusScope", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../statusbar/statusScope")>();
  return {
    ...actual,
    deriveScope: (...args: Parameters<typeof actual.deriveScope>) => {
      deriveScopeCalls.count += 1;
      return actual.deriveScope(...args);
    },
  };
});

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
  const { ROOT_A, ROOT_D, SUBAGENTS_A } = sampleTree();
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  const subagentsKey: ResourceKey = { kind: "subagents", ref: "local:a", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v2",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [2] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [ROOT_A, ROOT_D] })],
      [keyID(subagentsKey), resource(subagentsKey, SUBAGENTS_A)],
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

  test("a closed sidebar derives no scope, on mount or on navigation updates", () => {
    // The sidebar is mounted for the whole desktop session; a derivation per
    // polling update while closed duplicates the StatusBar's own walk for a
    // surface nothing shows.
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    const before = deriveScopeCalls.count;
    renderSidebar();
    act(() => {
      navigationStore.setState({ resources: new Map(navigationStore.getState().resources) });
    });
    expect(deriveScopeCalls.count).toBe(before);
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

  test("agents tab lists current children and folds the loaded inactive behind their count", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("B")).toBeTruthy();
    // C folds; the fold counts the loaded inactive rows.
    expect(screen.getByRole("button", { name: /Inactive subagents \(1\)/ })).toBeTruthy();
    expect(screen.queryByText("C")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Inactive subagents \(1\)/ }));
    expect(screen.getByText("C")).toBeTruthy();
    // The fold control carries its spacing class (the CSS rule and the
    // requireClass map entry exist for exactly this wrapper).
    const foldButton = screen.getByRole("button", { name: /Inactive subagents \(1\)/ });
    expect(foldButton.parentElement?.className).toContain("foldButton");
  });

  test("the wire's remainder is a real fetch: load more pages the subagents resource", () => {
    installTree();
    const load = vi.spyOn(navigationStore.getState(), "loadSubagents").mockResolvedValue(undefined as never);
    try {
      workspaceStore.getState().openPane("session", { ref: "local:a" });
      activitySidebarStore.getState().openWith("agents");
      renderSidebar();
      // 201 direct children past page 0, and the control names the next slice.
      const control = screen.getByRole("button", { name: /Load 50 more · 201 not shown/ });
      fireEvent.click(control);
      // Offset is the loaded row count: pages are contiguous 50-row slices.
      expect(load).toHaveBeenCalledWith("local:a", 2);
    } finally {
      load.mockRestore();
    }
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

  test("the fold and its paging reset when the scope's leaf changes", () => {
    // Two sessions, each with an inactive child behind a fold, served through
    // their subagents resources.
    const { ROOT_A, CHILD_B, CHILD_C, ROOT_D } = sampleTree();
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = (ref: string) => ({ kind: "location", ref }) as const;
    const subagentsKeyFor = (ref: string) => ({ kind: "subagents", ref, offset: 0, limit: 50 }) as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [ROOT_A, ROOT_D] })],
        [
          keyID(locationKey("local:a")),
          resource(locationKey("local:a"), {
            ref: "local:a",
            top_level_ref: "local:a",
            top_level: true,
            session: ROOT_A,
          }),
        ],
        [
          keyID(locationKey("local:d")),
          resource(locationKey("local:d"), {
            ref: "local:d",
            top_level_ref: "local:d",
            top_level: true,
            session: ROOT_D,
          }),
        ],
        [
          keyID(subagentsKeyFor("local:a")),
          resource(subagentsKeyFor("local:a"), { sessions: [CHILD_B, CHILD_C], remaining: 0, truncated: false }),
        ],
        [
          keyID(subagentsKeyFor("local:d")),
          resource(subagentsKeyFor("local:d"), { sessions: [CHILD_C], remaining: 0, truncated: false }),
        ],
      ]),
    });
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    // Open the fold on A.
    fireEvent.click(screen.getByRole("button", { name: /Inactive subagents/ }));
    expect(screen.getByText("C")).toBeTruthy();
    // Re-scope to D: the fold must be closed again, not carried over.
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "local:d" });
    });
    expect(screen.queryByText("C")).toBeNull();
    expect(screen.getByRole("button", { name: /Inactive subagents \(1\)/ })).toBeTruthy();
  });

  test("agents tab with no loaded rows but more on the wire offers the fetch, never a contradiction", () => {
    // The wire sent an empty page with 5 children behind it. The tab must not
    // claim "No subagents" while 5 exist: the load-more control, no fold.
    const { ROOT_D } = sampleTree();
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = { kind: "location", ref: "local:sparse" } as const;
    const sparseKey = { kind: "subagents", ref: "local:sparse", offset: 0, limit: 50 } as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [ROOT_D] })],
        [
          keyID(locationKey),
          resource(locationKey, {
            ref: "local:sparse",
            top_level_ref: "local:sparse",
            top_level: true,
            session: { ...ROOT_D, ref: "local:sparse", title: "S" },
          }),
        ],
        [keyID(sparseKey), resource(sparseKey, { sessions: [], remaining: 5, truncated: false })],
      ]),
    });
    workspaceStore.getState().openPane("session", { ref: "local:sparse" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.queryByText("No subagents at this level.")).toBeNull();
    expect(screen.getByRole("button", { name: /Load 5 more · 5 not shown/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Inactive subagents/ })).toBeNull();
  });

  test("agents tab with truly no subagents says so", () => {
    // Truly empty means a loaded page with no rows and no remainder.
    const { ROOT_D } = sampleTree();
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = { kind: "location", ref: "local:d" } as const;
    const emptyKey = { kind: "subagents", ref: "local:d", offset: 0, limit: 50 } as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [ROOT_D] })],
        [
          keyID(locationKey),
          resource(locationKey, { ref: "local:d", top_level_ref: "local:d", top_level: true, session: ROOT_D }),
        ],
        [keyID(emptyKey), resource(emptyKey, { sessions: [], remaining: 0, truncated: false })],
      ]),
    });
    workspaceStore.getState().openPane("session", { ref: "local:d" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("No subagents at this level.")).toBeTruthy();
  });

  test("agents tab reads the loading state while no page has landed", () => {
    // No subagents resource for the scope: null is a loading state, never
    // "No subagents" (the surfaces ensure the fetch).
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:d" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("Loading subagents…")).toBeTruthy();
    expect(screen.queryByText("No subagents at this level.")).toBeNull();
  });

  test("fork originals in the page's rows never list as agents", () => {
    // The page's rows carry fork originals (kind "fork") beside subagents.
    // The Agents tab lists subagents only, or a fork reads as an agent.
    const { ROOT_D } = sampleTree();
    const fork = { ...ROOT_D, ref: "local:fork", title: "FORK SNAP", state: "active", kind: "fork" };
    const agent = { ...ROOT_D, ref: "local:agent", title: "AGENT ROW", state: "active", kind: "subagent" };
    const leaf = { ...ROOT_D, ref: "local:leaf", title: "L" };
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = { kind: "location", ref: "local:leaf" } as const;
    const leafAgentsKey = { kind: "subagents", ref: "local:leaf", offset: 0, limit: 50 } as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [leaf] })],
        [keyID(leafAgentsKey), resource(leafAgentsKey, { sessions: [fork, agent], remaining: 0, truncated: false })],
        [
          keyID(locationKey),
          resource(locationKey, { ref: "local:leaf", top_level_ref: "local:leaf", top_level: true, session: leaf }),
        ],
      ]),
    });
    workspaceStore.getState().openPane("session", { ref: "local:leaf" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("AGENT ROW")).toBeTruthy();
    expect(screen.queryByText("FORK SNAP")).toBeNull();
    // The fork is not in the fold's true total either: only the subagent
    // exists, and it is current, so no fold renders at all.
    expect(screen.queryByRole("button", { name: /Inactive subagents/ })).toBeNull();
  });

  test("the open transition is read once per mount, not re-read on every render", () => {
    // getComputedStyle forces a style pass; calling it per render taxes every
    // scope change and tab switch for a token that changes with the theme, if
    // ever. Resolve it once when the aside mounts.
    installTree();
    const spy = vi.spyOn(window, "getComputedStyle");
    try {
      workspaceStore.getState().openPane("session", { ref: "local:a" });
      activitySidebarStore.getState().openWith("agents");
      renderSidebar();
      const readsAfterMount = spy.mock.calls.length;
      act(() => {
        activitySidebarStore.getState().setTab("jobs");
      });
      expect(spy.mock.calls.length).toBe(readsAfterMount);
    } finally {
      spy.mockRestore();
    }
  });

  test("Escape closes the open sidebar", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByTestId("activity-sidebar")).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(activitySidebarStore.getState().open).toBe(false);
  });

  test("an Escape the keybinding dispatcher claims (settings scope) dismisses that, not the sidebar", () => {
    // The app's dispatcher attaches to window at boot (installKeybindings),
    // before any sidebar can open. A scope-bound Escape - Settings' close
    // chord - claims the key there and preventDefaults. The sidebar's
    // listener must run AFTER that claim: one Esc, one dismissal.
    const registry = createKeybindingsRegistry(parseKeybinding);
    const dispatcher = createKeybindingDispatcher({ registry });
    const detach = dispatcher.attach(window);
    try {
      const state = registry.getState();
      const closeSettings = vi.fn();
      state.registerAction("settings.close", closeSettings);
      state.registerBinding({ id: "esc-settings", actionId: "settings.close", chord: "Escape", scope: "settings" });
      state.pushScope("settings");
      installTree();
      workspaceStore.getState().openPane("session", { ref: "local:a" });
      activitySidebarStore.getState().openWith("agents");
      renderSidebar();
      fireEvent.keyDown(document, { key: "Escape" });
      expect(closeSettings).toHaveBeenCalledTimes(1);
      expect(activitySidebarStore.getState().open).toBe(true);
    } finally {
      detach();
      dispatcher.dispose();
    }
  });

  test("Escape with a handled default does not close (a composer Esc keeps its meaning)", () => {
    installTree();
    // A handler closer to the focus (here: registered first, like the
    // composer's own Esc handler inside the document) preventDefaults the
    // event; the sidebar must stand.
    const claim = (event: KeyboardEvent) => {
      if (event.key === "Escape") event.preventDefault();
    };
    document.addEventListener("keydown", claim);
    try {
      workspaceStore.getState().openPane("session", { ref: "local:a" });
      activitySidebarStore.getState().openWith("agents");
      renderSidebar();
      fireEvent.keyDown(document, { key: "Escape" });
      expect(activitySidebarStore.getState().open).toBe(true);
    } finally {
      document.removeEventListener("keydown", claim);
    }
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
