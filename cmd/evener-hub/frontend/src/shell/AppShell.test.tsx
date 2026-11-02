import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { NavigationReadParams, NavigationReadResponse, NavigationSessionLocation } from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";

import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../motion";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { cascadeClient, cascadeContext } from "../panes/zoom/cascadeTestUtils";
import type { SessionZoomParams } from "../panes/zoom/intent";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { activityDetailsThread, answerActivityRead } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { resetThreadsStoreForTests } from "../stores/threads";
import { AppShell } from "./AppShell";
import { resetActivitySidebarStoreForTests } from "./activitybar/activitySidebarStore";
import { ClientProvider } from "./clientContext";
import { DockHost } from "./DockHost";
import { resetDockChunkForTests } from "./DockRegion";
import { paletteStore } from "./palette/paletteController";
import { getDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// Matches DockHost.tsx's own LAYOUT_STORAGE_KEY exactly (not exported - a
// deliberately internal implementation detail; duplicated here the same
// way DockHost.test.tsx's own LAYOUT_KEY is).
const LAYOUT_KEY = "evener.workspace.layout.v2";

const ALL_FEATURES_OFF = {
  threadList: false,
  threadTurnsList: false,
  turnStart: false,
  turnSteer: false,
  threadClear: false,
  threadShutdown: false,
  forkFromTurn: false,
  tasks: false,
  transcriptList: false,
  modelList: false,
  directoryComplete: false,
  auth: false,
};

const TREE_SESSION = {
  row_id: "project:proj1:local:s1",
  ref: "local:s1",
  host_id: "local",
  session_id: "s1",
  title: "Session one",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

let stoppedTreeSession = false;

const EMPTY_NAV_RESPONSE = {
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function navigationRead(params: NavigationReadParams): NavigationReadResponse {
  const treeSession = stoppedTreeSession ? { ...TREE_SESSION, state: "ended", live: false } : TREE_SESSION;
  switch (params.resource) {
    case "manifest":
      return wireSnapshot(params, EMPTY_NAV_RESPONSE, '"test"');
    case "section":
      return wireSnapshot(
        params,
        {
          sessions: params.section === "live" ? [treeSession] : [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "pin_catalog":
      return wireSnapshot(params, { pin_sections: [], remaining: 0 }, '"test"');
    case "pin_section":
      return wireSnapshot(
        params,
        {
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "catalog":
      return wireSnapshot(
        params,
        {
          projects:
            params.catalog === "projects"
              ? [{ key: "proj1", name: "Project one", session_count: 1, working_dir: "" }]
              : [],
          remaining: 0,
        },
        '"test"',
      );
    case "project":
      return wireSnapshot(
        params,
        {
          key: "proj1",
          current: { sessions: [treeSession], remaining: 0 },
          recent: { sessions: [], remaining: 0 },
          archived: { sessions: [], remaining: 0 },
          truncated: false,
        },
        '"test"',
      );
    case "project_page":
      return wireSnapshot(
        params,
        {
          key: params.projectKey,
          tier: params.tier,
          offset: params.offset,
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
    case "location":
      return wireSnapshot(
        params,
        {
          ref: params.ref,
          top_level_ref: params.ref,
          top_level: true,
          session: { ...treeSession, ref: params.ref, session_id: params.ref },
        },
        '"test"',
      );
    case "subagents":
      // Session activity surfaces can demand the scope's subagents page;
      // answer with an empty one (tests that need rows install them into the
      // store directly).
      return wireSnapshot(
        params,
        {
          offset: params.offset,
          sessions: [],
          remaining: 0,
          truncated: false,
        },
        '"test"',
      );
  }
  throw new Error(`unsupported navigation resource: ${params.resource}`);
}

// A FakeClient whose connect() advertises a v2 navigation capability with a
// generation matching EMPTY_NAV_RESPONSE. Tests that render <AppShell/> and
// depend on the navigation store being in mode "v3" (rather than "error")
// must use this instead of a bare `new FakeClient("ready")`, whose default
// InitializeResponse has no navigation capability.
function navClient(initialState: ConnectionState = "ready"): FakeClient {
  const client = new FakeClient(initialState);
  client.on("evener/navigation/read", navigationRead);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: APPWIRE_PROTOCOL_VERSION,
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

function installLocation(location: NavigationSessionLocation): void {
  const key = { kind: "location", ref: location.ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: location,
    loadedRevision: location.revision,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: location.generation_id,
  });
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: location.generation_id,
    resources,
  });
}

function installLocationForRoute(ref: string, ownerRef?: string): void {
  const owner = ownerRef ?? (ref === "local:child" ? "local:owner" : ref === "local:sub1" ? "local:s1" : ref);
  installLocation({
    generation_id: "generation_test",
    revision: 1,
    ref,
    top_level_ref: owner,
    top_level: owner === ref,
    tier: "current",
    session: {
      ref,
      host_id: "local",
      session_id: ref,
      title: ref,
      project: "test-project",
      state: "idle",
      kind: owner === ref ? "session" : "subagent",
      live: false,
      children: [],
    },
  });
}

const appShellCss = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "AppShell.module.css"), "utf8").replace(
  /\/\*[\s\S]*?\*\//g,
  "",
);

test("keeps the desktop shell full-bleed contract in AppShell.module.css", () => {
  expect(appShellCss).not.toContain("padding: var(--space-4);");
  // No gap at any width: the rail sits flush against the workspace, its own
  // border-right hairline the only divider (2026-08 sidebar UX rework). A
  // reintroduced gap: on any .content rule is exactly the regression to catch.
  expect(appShellCss).not.toMatch(/\.content[^{]*\{[^}]*\bgap:/);
  expect(appShellCss).toContain("padding: 0;");
});

// Renders a route to completion so both halves of its lazy-loading cost are
// already paid by the time a test measures it. The module cache is only the
// first half: React.lazy keeps a payload of its own that stays uninitialized
// until React first RENDERS the component, so a warm module cache still
// leaves the first render suspending. The welcome route crosses two nested
// boundaries (AppShell's lazy DockHost, then PaneHost's lazy Welcome); each
// other pane crosses one more of its own. Rendering inside an awaited act
// lets each reveal commit as soon as its already-imported chunk resolves;
// outside act, react-dom holds every reveal for FALLBACK_THROTTLE_MS (300ms,
// react-dom 19.2) on a real timer. Mirrors App.test.tsx's own warmRoute.
//
// The landmark wait gets WARM_ROUTE_TRIPWIRE_MS rather than findBy's 1000ms
// default. That default is an assertion window - it exists to hold a test to a
// responsiveness bar - and a warm-up has no such bar to hold, so the deadline
// here is a tripwire for a hung render.
const WARM_ROUTE_TRIPWIRE_MS = 10_000;

async function warmRoute(
  path: string,
  findLandmark: (options: { timeout: number }) => Promise<unknown>,
): Promise<void> {
  window.history.pushState({}, "", path);
  if (path.startsWith("/s/")) {
    const ref = decodeURIComponent(path.slice("/s/".length));
    installLocation({
      generation_id: "generation_test",
      revision: 1,
      ref,
      top_level_ref: ref,
      top_level: true,
      session: {
        ref,
        host_id: "local",
        session_id: ref,
        title: ref,
        project: "",
        state: "idle",
        kind: "session",
        live: false,
        children: [],
      },
    });
  }
  await act(async () => {
    render(<AppShell client={new FakeClient("ready")} />);
  });
  await findLandmark({ timeout: WARM_ROUTE_TRIPWIRE_MS });
  // Unmounting also clears DockHost's pending debounced layout save (its own
  // effect cleanup), so no warm render leaks a write into a later test.
  cleanup();
  resetWorkspaceStoreForTests();
  // The /settings/hub warm route mounts the real HubSection, whose
  // mount effect calls settingsOverviewStore.getState().fetch() for real
  // against this warm-up's own FakeClient (no handler scripted for
  // "evener/settings/overview", so it settles into a scripted-looking
  // "no handler" error) - settingsOverviewStore is a module singleton, so
  // that leftover error/inflight bookkeeping must not survive into this
  // file's own tests.
  resetSettingsOverviewStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
}

// Await the welcome/session panes' lazy-loaded modules ONCE up front so
// React.lazy resolves from a warm module cache - mirrors App.test.tsx's own
// beforeAll pattern for the same reason: the slow part of lazy-loading is
// the transform/import work, an awaitable completion, not something to race
// with a widened findBy deadline.
beforeAll(async () => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  await import("../panes/settings/Settings");
  await import("../panes/spawn/Spawn");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path) - pre-warmed for the same
  // reason as the two panes above.
  await import("./DockHost");

  // Then render each pane once, for the React.lazy half of the cost - see
  // warmRoute above. Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", (wait) => screen.findByText("No session open", undefined, wait));
  await warmRoute("/new", (wait) => screen.findByRole("button", { name: "Start" }, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
  await warmRoute("/settings/hub", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(() => {
  stoppedTreeSession = false;
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  // notifications/index.ts keeps module state (its init guard, the
  // "reconnect" detector's sawReady, the attention baseline) for every test
  // in this file, and a test that connects a client straight to "ready" arms
  // that detector. Reset and re-init before each test's own fresh connect so
  // every test starts from the state a fresh module evaluation leaves:
  // engine started, seeded from the idle connection and reset navigation
  // store above, with nothing carried over from the previous test.
  resetNotificationsForTests();
  initNotifications();
  navigationStore.setState({ mode: "v3" });
  // afterEach restores Vitest globals; recreate deterministic storage before
  // clearing it so DockHost cannot restore the prior test's layout.
  installLocalStorage(new MemoryStorage());
  localStorage.clear();
  // The settings pane's last-visited-section memory (prefs.ts's
  // lastSettingsSection) is a module-singleton field backed by this same
  // storage: rehydrate against the cleared storage, or one test's settings
  // visit picks the next test's bare-/settings landing section.
  resetPrefsStoreForTests();
  resetSettingsHostForTests();
  // The credential statuses store is a module singleton too: once a test's
  // Providers & credentials section has read it, it re-reads on every later
  // connect, so the next test's client would see an evener/auth/list it never
  // caused.
  resetAuthStatusesStoreForTests();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // The command palette store is a module singleton; reset it so one test's
  // open palette never leaks into the next.
  paletteStore.setState({ open: false, query: "" });
  // Several tests below navigate to /settings/* and mount the real
  // HubSection, whose mount effect touches settingsOverviewStore for
  // real - same module-singleton reasoning as warmRoute's own reset above.
  resetSettingsOverviewStoreForTests();
  vi.unstubAllGlobals();
});

// Literal route/restore oracle, not parseZoomParams/deriveCascadePath or a
// comparison solely against whatever saveLayout happened to serialize.
const ORDERING_CASCADE: SessionZoomParams = {
  ref: "local:step-6",
  source: { type: "session", params: { ref: "local:root" } },
  edges: [
    { ownerRef: "local:root", childRef: "local:step-1", delegateId: "edge-local:step-1" },
    { ownerRef: "local:step-1", childRef: "local:step-2", delegateId: "edge-local:step-2" },
    { ownerRef: "local:step-2", childRef: "local:step-3", delegateId: "edge-local:step-3" },
    { ownerRef: "local:step-3", childRef: "local:step-4", delegateId: "edge-local:step-4" },
    { ownerRef: "local:step-4", childRef: "local:step-5", delegateId: "edge-local:step-5" },
    { ownerRef: "local:step-5", childRef: "local:step-6", delegateId: "edge-local:step-6" },
  ],
};

const ORDERING_SAVED_PANES = [
  { id: "pane_session_1", type: "sessionZoom", params: ORDERING_CASCADE, slot: "main" },
  { id: "pane_session_2", type: "session", params: { ref: "local:neighbor" }, slot: "secondary" },
];

// Seats are per-tab runtime ownership, not saved layout geometry. Compare the
// saved/restored pane shape without them, then test seats separately.
function panesWithoutSeats<T extends { composerSeatId?: string }>(panes: readonly T[]) {
  return panes.map(({ composerSeatId: _composerSeatId, ...pane }) => pane);
}

function orderingClient() {
  const ancestry = ["local:root", "local:step-1", "local:step-2", "local:step-3", "local:step-4", "local:step-5"];
  const client = cascadeClient((ref) => {
    const index = ref === "local:step-6" ? 6 : ancestry.indexOf(ref);
    return cascadeContext(ref, index > 0 ? ancestry.slice(0, index) : []);
  });
  client.on("evener/navigation/read", navigationRead);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: APPWIRE_PROTOCOL_VERSION,
    sourceId: "fake",
    features: ALL_FEATURES_OFF,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

// Prepare legacy in-place intent using real retype, pane registration,
// Dockview, and DockHost's synchronous final-save flush. No fabricated layout.
async function saveOrderingCascade(): Promise<void> {
  const client = orderingClient();
  connectionStore.getState().connect(client);
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:root" });
  const view = await act(async () =>
    render(
      <ClientProvider client={client}>
        <MotionProvider>
          <DockHost />
        </MotionProvider>
      </ClientProvider>,
    ),
  );
  await screen.findByRole("heading", { name: "local:root" });
  await act(async () => {
    workspaceStore.getState().openPane("session", { ref: "local:neighbor" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(sourceId);
  });
  await act(async () => {
    const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
    if (!source) throw new Error("Missing legacy cascade source pane");
    expect(workspaceStore.getState().retypePane(source, "sessionZoom", ORDERING_CASCADE)).toBe(true);
  });
  await screen.findByRole("button", { name: "Return to previous view" });
  expect(panesWithoutSeats(workspaceStore.getState().panes)).toEqual(ORDERING_SAVED_PANES);
  expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_1");
  view.unmount();
  const raw = localStorage.getItem(LAYOUT_KEY);
  if (raw === null) throw new Error("Missing real six-edge saved layout");
  expect(JSON.parse(raw).panels.pane_session_1.params).toEqual({
    paneType: "sessionZoom",
    paneParams: ORDERING_CASCADE,
  });
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
}

test.each([
  { order: "completed-placement-before-restoration", routeRef: "local:root" },
  { order: "restoration-before-location", routeRef: "local:root" },
  { order: "cached-location-with-warm-host", routeRef: "local:root" },
  { order: "completed-placement-before-restoration", routeRef: "local:different-root" },
  { order: "restoration-before-location", routeRef: "local:different-root" },
  { order: "cached-location-with-warm-host", routeRef: "local:different-root" },
] as const)("startup $order respects route $routeRef against a saved six-edge cascade", async ({ order, routeRef }) => {
  await saveOrderingCascade();
  const client = orderingClient();
  const reply = deferred<void>();
  const requested = deferred<void>();
  const restored = deferred<void>();
  const witnesses: { event: string; panes?: unknown; focusedPaneId?: string | null }[] = [];
  const unsubscribeWorkspace = workspaceStore.subscribe((state, previous) => {
    if (state.panes === previous.panes) return;
    witnesses.push({ event: "workspace", panes: state.panes, focusedPaneId: state.focusedPaneId });
    if (state.mainPane()?.type === "sessionZoom") {
      witnesses.push({ event: "restored" });
      restored.resolve();
    }
  });
  const unsubscribeNavigation = navigationStore.subscribe((state) => {
    const location = state.resources.get(keyID({ kind: "location", ref: routeRef }));
    if (location?.data && !location.loading && !location.stale && !witnesses.some((w) => w.event === "location-ready"))
      witnesses.push({ event: "location-ready" });
  });
  class StartupStorage extends MemoryStorage {
    override getItem(key: string): string | null {
      if (key === LAYOUT_KEY) {
        witnesses.push({ event: "layout-read", panes: workspaceStore.getState().panes });
      }
      return super.getItem(key);
    }
  }
  // Copy the exact saved bytes into the observed external storage root.
  const saved = localStorage.getItem(LAYOUT_KEY);
  if (saved === null) throw new Error("Missing saved startup fixture");
  const storage = new StartupStorage();
  storage.setItem(LAYOUT_KEY, saved);
  installLocalStorage(storage);
  client.on("evener/navigation/read", async (params) => {
    if (params.resource === "location" && params.ref === routeRef) {
      requested.resolve();
      await reply.promise;
    }
    return navigationRead(params);
  });
  function PlacementCommitWitness() {
    // Child passive effects precede this parent effect. With the real fresh
    // lazy payload still suspended, AppShell has completed its route effect,
    // while DockHost has not yet mounted or read storage. This observes only.
    useEffect(() => {
      if (order !== "completed-placement-before-restoration") return;
      const location = navigationStore.getState().resources.get(keyID({ kind: "location", ref: routeRef }));
      expect(location?.data).toMatchObject({ ref: routeRef, top_level_ref: routeRef, top_level: true });
      expect(location?.loading).toBe(false);
      expect(location?.stale).toBe(false);
      expect(panesWithoutSeats(workspaceStore.getState().panes)).toEqual([
        { id: "pane_session_1", type: "session", params: { ref: routeRef }, slot: "main" },
      ]);
      expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_1");
      expect(getDockviewApi()).toBeNull();
      expect(witnesses.some((w) => w.event === "layout-read" || w.event === "restored")).toBe(false);
      witnesses.push({ event: "placement-commit" });
    }, []);
    return <AppShell client={client} />;
  }
  try {
    const initialized = await client.connect();
    initNavigation(client, initialized);
    window.history.pushState({}, "", `/s/${encodeURIComponent(routeRef)}`);
    if (order !== "restoration-before-location") {
      reply.resolve();
      // A real cached server location, not installLocation or a workspace seed.
      await navigationStore.getState().lookupLocation(routeRef);
    }
    if (order === "completed-placement-before-restoration") resetDockChunkForTests();
    await act(async () => render(<PlacementCommitWitness />));
    await restored.promise;
    if (order === "restoration-before-location") {
      await requested.promise;
      expect(panesWithoutSeats(workspaceStore.getState().panes)).toEqual(ORDERING_SAVED_PANES);
      expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_1");
      expect(getDockviewApi()?.activePanel?.id).toBe("pane_session_1");
      await act(async () => {
        reply.resolve();
        // Joins the actual in-flight server read and its store publication.
        await navigationStore.getState().lookupLocation(routeRef);
      });
    }
    const events = witnesses.map((witness) => witness.event);
    expect(events.indexOf("location-ready")).toBeGreaterThanOrEqual(0);
    expect(events.indexOf("layout-read")).toBeGreaterThanOrEqual(0);
    expect(events.indexOf("restored")).toBeGreaterThan(events.indexOf("layout-read"));
    if (order !== "restoration-before-location") {
      expect(events.indexOf("location-ready")).toBeLessThan(events.indexOf("layout-read"));
      const layoutReadPanes = witnesses.find((w) => w.event === "layout-read")?.panes;
      expect(panesWithoutSeats(Array.isArray(layoutReadPanes) ? layoutReadPanes : [])).toEqual([
        { id: "pane_session_1", type: "session", params: { ref: routeRef }, slot: "main" },
      ]);
      if (order === "completed-placement-before-restoration") {
        expect(events.indexOf("placement-commit")).toBeGreaterThan(events.indexOf("location-ready"));
        expect(events.indexOf("placement-commit")).toBeLessThan(events.indexOf("layout-read"));
      } else {
        expect(events).not.toContain("placement-commit");
      }
    } else {
      expect(events.indexOf("restored")).toBeLessThan(events.indexOf("location-ready"));
      expect(witnesses.find((w) => w.event === "layout-read")?.panes).toEqual([]);
    }
    if (routeRef === "local:different-root") {
      expect(panesWithoutSeats(workspaceStore.getState().panes)).toEqual([
        { id: "pane_session_3", type: "session", params: { ref: "local:different-root" }, slot: "main" },
      ]);
      expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_3");
      expect(getDockviewApi()?.activePanel?.id).toBe("pane_session_3");
      expect(screen.queryByRole("button", { name: "Return to previous view" })).toBeNull();
      expect(await screen.findByRole("heading", { name: "local:different-root" })).toBeTruthy();
      expect(window.location.pathname).toBe("/s/local%3Adifferent-root");
    } else {
      expect(
        panesWithoutSeats(workspaceStore.getState().panes),
        `Startup witnesses: ${JSON.stringify(witnesses)}`,
      ).toEqual(ORDERING_SAVED_PANES);
      expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_1");
      expect(getDockviewApi()?.activePanel?.id).toBe("pane_session_1");
      expect(await screen.findByRole("button", { name: "Return to previous view" })).toBeTruthy();
      expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual([
        "local:step-5",
        "local:step-6",
      ]);
      expect(screen.getAllByTestId("cascade-spine").map((spine) => spine.getAttribute("data-scope-ref"))).toEqual([
        "local:root",
        "local:step-1",
        "local:step-2",
        "local:step-3",
        "local:step-4",
      ]);
      expect(screen.getByRole("heading", { name: "local:neighbor" })).toBeTruthy();
      expect(window.location.pathname).toBe("/s/local%3Aroot");
    }
  } finally {
    reply.resolve();
    unsubscribeWorkspace();
    unsubscribeNavigation();
    cleanup();
    resetActivitySidebarStoreForTests();
    resetNavigationStoreForTests();
    resetThreadsStoreForTests();
    connectionStore.setState({ client: null, state: "idle" });
  }
});

test("a real session URL wins over an Activity-only saved layout", async () => {
  const client = navClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  client.on("thread/unsubscribe", () => ({}));
  client.on("evener/thread/activity/read", answerActivityRead);
  connectionStore.getState().connect(client);
  const retired = workspaceStore.getState().openPane("session", { ref: "local:saved" });
  const saved = render(
    <ClientProvider client={client}>
      <DockHost />
    </ClientProvider>,
  );
  await waitFor(() => expect(document.querySelector('[data-pane-scaffold="session:local:saved"]')).toBeTruthy());
  saved.unmount();
  const raw = localStorage.getItem(LAYOUT_KEY);
  if (raw === null) throw new Error("Expected a real saved Dockview layout");
  const layout = JSON.parse(raw) as { panels: Record<string, { params?: unknown }> };
  const pane = layout.panels[retired];
  if (!pane) throw new Error(`Missing saved pane ${retired}`);
  pane.params = { paneType: "sessionActivity", paneParams: { ref: "local:saved" } };
  localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout));
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  window.history.pushState({}, "", "/s/local:routed");
  installLocationForRoute("local:routed");
  await act(async () => {
    render(<AppShell client={client} />);
  });
  await waitFor(() =>
    expect(workspaceStore.getState().panes).toEqual([
      expect.objectContaining({ type: "session", params: { ref: "local:routed" }, slot: "main" }),
    ]),
  );
  expect(document.querySelector('[data-pane-scaffold="session:local:routed"]')).toBeTruthy();
  expect(screen.queryByText("No session open")).toBeNull();
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
  const methods = client.calls.map((call) => call.method);
  for (const mutation of [
    "evener/session/delete",
    "evener/project/delete",
    "evener/archive/set",
    "thread/shutdown",
    "evener/thread/forceStop",
  ]) {
    expect(methods).not.toContain(mutation);
  }
});
