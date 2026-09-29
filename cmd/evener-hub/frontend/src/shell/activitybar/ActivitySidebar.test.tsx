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
    // The fold control carries its spacing class (the CSS rule and the
    // requireClass map entry exist for exactly this wrapper).
    const foldButton = screen.getByRole("button", { name: /Inactive subagents \(202\)/ });
    expect(foldButton.parentElement?.className).toContain("foldButton");
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
    // Two sessions, each with an inactive child behind a fold.
    const { ROOT_A, ROOT_D } = sampleTree();
    const rootB = { ...ROOT_D, ref: "local:d", title: "D", children: ROOT_A.children };
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = (ref: string) => ({ kind: "location", ref }) as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [ROOT_A, rootB] })],
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
            session: rootB,
          }),
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
    expect(screen.getByRole("button", { name: /Inactive subagents/ })).toBeTruthy();
  });

  test("agents tab with no loaded subagents but more on the wire shows the remainder, never a contradiction", () => {
    // The wire omitted every subagent row but says 5 exist. The tab must not
    // claim "No subagents" while folding 5 behind a click: show the passive
    // remainder directly (WatchesTab's pattern), with no fold hiding it.
    const { ROOT_D } = sampleTree();
    const sparse = { ...ROOT_D, ref: "local:sparse", title: "S", children: [], more_subagents: 5 };
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = { kind: "location", ref: "local:sparse" } as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [sparse] })],
        [
          keyID(locationKey),
          resource(locationKey, {
            ref: "local:sparse",
            top_level_ref: "local:sparse",
            top_level: true,
            session: sparse,
          }),
        ],
      ]),
    });
    workspaceStore.getState().openPane("session", { ref: "local:sparse" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.queryByText("No subagents at this level.")).toBeNull();
    expect(screen.getByText("+5 more")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Inactive subagents/ })).toBeNull();
  });

  test("agents tab with truly no subagents says so", () => {
    installTree();
    workspaceStore.getState().openPane("session", { ref: "local:d" });
    activitySidebarStore.getState().openWith("agents");
    renderSidebar();
    expect(screen.getByText("No subagents at this level.")).toBeTruthy();
  });

  test("fork originals in the wire's children never list as agents", () => {
    // The hub's tree carries fork originals (kind "fork") in children beside
    // subagents; the rail renders those as nested session rows. The Agents
    // tab lists subagents only, or a fork reads as an agent.
    const { ROOT_D } = sampleTree();
    const fork = { ...ROOT_D, ref: "local:fork", title: "FORK SNAP", state: "active", kind: "fork" };
    const agent = { ...ROOT_D, ref: "local:agent", title: "AGENT ROW", state: "active", kind: "subagent" };
    const leaf = { ...ROOT_D, ref: "local:leaf", title: "L", children: [fork, agent] };
    const liveKey = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
    const locationKey = { kind: "location", ref: "local:leaf" } as const;
    navigationStore.setState({
      resources: new Map([
        [keyID(liveKey), resource(liveKey, { sessions: [leaf] })],
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
