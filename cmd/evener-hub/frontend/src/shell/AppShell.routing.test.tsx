import type {
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionLocation,
  NavigationSnapshot,
} from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState, WireError } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { flushSync } from "react-dom";

import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { openDocBeside } from "../panes/doc/openDoc";
import { enterAgentCascade, returnFromAgentCascade } from "../panes/zoom/actions";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { credentialsStore } from "../stores/credentials";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { activityDelegate } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { emptyStateReasons } from "../widgets/emptystate/emptyStateTestUtils";
import { AppShell } from "./AppShell";
import { installFailedLocation, installMobileViewport, threadStartResponse } from "./appShellTestUtils";
import { openAttentionSession } from "./attentionHistory";
import { DockHost } from "./DockHost";
import { paletteStore } from "./palette/paletteController";
import { conversationPaneLifetime } from "./paneLifetime";
import { navigate, refParam } from "./routing";
import {
  documentPaneState,
  getDockviewApi,
  type OpenPaneRecord,
  recordConversationOpenParent,
  recordDocumentPaneState,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";

// Matches DockHost.tsx's own LAYOUT_STORAGE_KEY exactly (not exported - a
// deliberately internal implementation detail; duplicated here the same
// way DockHost.test.tsx's own LAYOUT_KEY is).
const LAYOUT_KEY = "evener.workspace.layout.v2";

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

const paneFor = (ref: string) =>
  workspaceStore.getState().panes.find((p) => (p.params as { ref?: string }).ref === ref);

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

// Build the persisted fixture through the real AppShell/DockHost save path.
// The route tests below deliberately inspect the restored workspace state,
// not dockview's serialized representation, so a layout-format change cannot
// make these assertions tautological.
async function saveRealSessionLayout(): Promise<void> {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:session-a",
      path: "README.md",
      kind: "text",
    });
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
}

async function saveRealSessionPanelLayout(): Promise<void> {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  act(() => {
    const panelId = workspaceStore
      .getState()
      .openPane("sessionTasks", { ref: "local:session-a" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/));
  await screen.findByRole("status", { name: "Loading session panel" });

  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
}

// The zoom system promotes a session pane into a cascade by retyping it in
// place (panes/zoom/actions.ts) - same pane id, same slot - so this is the
// exact main a saved layout restores. Edges are the saved cascade path.
function retypeMainToCascade(
  sourceRef: string,
  leafRef: string,
  edges: { ownerRef: string; childRef: string; delegateId: string }[] = [],
): void {
  const main = workspaceStore.getState().mainPane();
  if (!main) throw new Error("no main pane to zoom");
  if (
    !workspaceStore.getState().retypePane(main, "sessionZoom", {
      ref: leafRef,
      source: { type: "session", params: { ref: sourceRef } },
      edges,
    })
  ) {
    throw new Error("cascade promotion failed");
  }
}

// The cascade flavor of the saveReal*Layout helpers above: a real saved
// layout whose main is a cascade zoomed on local:session-a with a
// sessionTasks panel beside it, plus the saved pane ids for the restore
// assertions.
async function saveRealCascadeLayout(): Promise<{ cascadeId?: string; secondaryId?: string }> {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  act(() => {
    retypeMainToCascade("local:session-a", "local:child", [
      { ownerRef: "local:session-a", childRef: "local:child", delegateId: "edge-child" },
    ]);
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:session-a" }, { slot: "secondary" });
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));
  const cascadeId = workspaceStore.getState().mainPane()?.id;
  const secondaryId = workspaceStore.getState().panes.find((pane) => pane.type === "sessionTasks")?.id;
  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
  return { cascadeId, secondaryId };
}

async function saveRealSecondaryCascadeLayout(ref = "local:session-a") {
  window.history.pushState({}, "", `/s/${ref}`);
  installLocationForRoute(ref);
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(getDockviewApi()?.panels).toHaveLength(ref === "local:child" ? 2 : 1));
  const mainId = workspaceStore.getState().mainPane()?.id;
  const source = paneFor(ref);
  if (!mainId || !source || source.type !== "session") throw new Error("Expected real routed conversations");
  let cascadeId = "";
  let secondaryId = "";
  act(() => {
    secondaryId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:neighbor" }, { slot: "secondary" });
    cascadeId = enterAgentCascade(
      activityDelegate({
        ownerRef: ref,
        childRef: ref === "local:child" ? "local:grandchild" : "local:child",
        delegateId: "edge-child",
      }),
      source.id,
    );
    workspaceStore.getState().focusPane(cascadeId);
  });
  await waitFor(() => expect(getDockviewApi()?.panels).toHaveLength(ref === "local:child" ? 4 : 3));
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
  return { mainId, sourceId: source.id, cascadeId, secondaryId };
}

function heldRouteLocation(ref: string) {
  const client = navClient();
  const finishes: (() => void)[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource !== "location" || params.ref !== ref) return navigationRead(params);
    return new Promise<NavigationReadResponse>((resolve) => {
      finishes.push(() => {
        const owner = ref === "local:child" ? "local:owner" : ref;
        const response = wireSnapshot(
          params,
          { session: { ...TREE_SESSION, ref, session_id: ref, kind: owner === ref ? "session" : "subagent" } },
          '"test"',
        );
        (response.data as NavigationSnapshot).metadata = {
          generation_id: "generation_test",
          revision: 1,
          ref,
          top_level_ref: owner,
          top_level: owner === ref,
          tier: "current",
        };
        resolve(response);
      });
    });
  });
  return {
    client,
    release: () => {
      if (finishes.length === 0) throw new Error("No actual location request to release");
      for (const finish of finishes.splice(0)) finish();
    },
  };
}

async function saveLegacyNestedMainLayout(): Promise<void> {
  workspaceStore.getState().openPane("session", { ref: "local:child" });
  const { unmount } = render(<DockHost />);
  await screen.findByText(/loading transcript/i);
  expect(workspaceStore.getState().panes).toEqual([
    expect.objectContaining({ type: "session", params: { ref: "local:child" }, slot: "main" }),
  ]);

  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
}

// --- routing -> workspace glue (this task) ---------------------------

test("deep-linking to /s/{ref} opens that session pane", async () => {
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);

  // The real session pane (wave 4) shows the ref it was opened with while
  // loading - proving the deep link actually threaded through openPane
  // into a real dockview panel, not just that urlToPane() parsed the URL
  // correctly in isolation (routing.test.ts already covers that). No
  // thread/read handler is scripted on this FakeClient, so the pane settles
  // into (and stays in) its loading state - the tab title renders
  // synchronously (addPanel's own title option) but the pane's own content
  // is a lazy-loaded component behind Suspense, so this waits for the pane
  // body's own loading text FIRST (it exists only once Suspense resolves),
  // THEN checks the ref appears in the tab and pane body title. The pane's
  // status bar now leads with repository location, not another session title.
  expect(await screen.findByText(/loading transcript/i)).toBeTruthy();
  expect(screen.getAllByText("local:ref_abc123")).toHaveLength(2);
});

test("a deep-link lookup starts exactly once when navigation mode becomes v2", async () => {
  const ref = "local:mode-transition";
  const client = navClient();
  initNavigation(client, { version: 1, generationId: "generation_test", sequence: 0 });
  navigationStore.setState({ mode: "unknown" });
  const lookupLocation = vi.spyOn(navigationStore.getState(), "lookupLocation");
  const locationCalls = () =>
    client.calls.filter(
      ({ method, params }) =>
        method === "evener/navigation/read" && (params as NavigationReadParams).resource === "location",
    );
  window.history.pushState({}, "", `/s/${encodeURIComponent(ref)}`);

  render(<AppShell client={client} />);
  expect(lookupLocation).not.toHaveBeenCalled();
  expect(locationCalls()).toHaveLength(0);

  act(() => navigationStore.setState({ mode: "v3" }));

  await waitFor(() => expect(locationCalls()).toHaveLength(1));
  expect(lookupLocation).toHaveBeenCalledTimes(1);
  expect(lookupLocation).toHaveBeenCalledWith(ref);
  expect(locationCalls()).toEqual([
    {
      method: "evener/navigation/read",
      params: { resource: "location", ref, representationVersion: 3 },
    },
  ]);
});

test("a direct route with a settled gone v2 location replaces unrelated state with welcome without another lookup", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
  const ref = "local:deleted-direct";
  const key = { kind: "location", ref } as const;
  const lookupLocation = vi.fn().mockResolvedValue(undefined);
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data: null,
          normalized: {
            key,
            graph: { metadata: {}, entities: new Map(), containers: new Map() },
            version: { generationId: "generation_test", revision: 2, etag: '"gone"' },
            presence: "gone",
          },
          loadedRevision: 2,
          targetRevision: null,
          forceToken: 0,
          etag: '"gone"',
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
    lookupLocation,
  });
  window.history.pushState({}, "", `/s/${encodeURIComponent(ref)}`);

  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("welcome"));
  await act(async () => undefined);

  expect(paneFor(ref)).toBeUndefined();
  expect(paneFor("local:unrelated")).toBeUndefined();
  expect(lookupLocation).not.toHaveBeenCalled();
});

test("a direct route with a stale old-generation gone tombstone waits for the fresh lookup instead of welcome", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
  const ref = "local:reconnected-direct";
  const key = { kind: "location", ref } as const;
  const lookupLocation = vi.fn().mockResolvedValue(undefined);
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_next",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data: null,
          normalized: {
            key,
            graph: { metadata: {}, entities: new Map(), containers: new Map() },
            version: { generationId: "generation_old", revision: 2, etag: '"gone"' },
            presence: "gone",
          },
          loadedRevision: null,
          targetRevision: null,
          forceToken: 0,
          etag: null,
          loading: false,
          // Retained across the generation reset while the new-generation
          // request is in flight: not authoritative yet.
          stale: true,
          error: null,
          generationID: "generation_next",
        },
      ],
    ]),
    lookupLocation,
  });
  window.history.pushState({}, "", `/s/${encodeURIComponent(ref)}`);

  render(<AppShell client={new FakeClient("ready")} />);
  // The stale tombstone must neither redirect to welcome nor satisfy the
  // route: the shell holds the pending ref and issues the fresh lookup.
  await waitFor(() => expect(lookupLocation).toHaveBeenCalled());
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("welcome");
  expect(paneFor(ref)).toBeUndefined();
  await act(async () => undefined);
});

test("a nested location opens its explicit owner without loading a project", async () => {
  const child = "local:collapsed-child";
  const client = navClient();
  window.history.pushState({}, "", `/s/${encodeURIComponent(child)}`);
  installLocation({
    generation_id: "generation_test",
    revision: 7,
    ref: child,
    top_level_ref: "local:owner",
    top_level: false,
    project_key: "collapsed-project",
    tier: "recent",
    session: {
      ref: child,
      host_id: "local",
      session_id: "collapsed-child",
      title: "Collapsed child",
      project: "collapsed-project",
      state: "idle",
      kind: "subagent",
      live: false,
      children: [],
    },
  });
  render(<AppShell client={client} />);

  await waitFor(() => expect(paneFor(child)?.slot).toBe("secondary"));
  expect(paneFor("local:owner")?.slot).toBe("main");
  expect([...navigationStore.getState().resources.values()].some((resource) => resource.key.kind === "project")).toBe(
    false,
  );
  expect(
    client.calls.some(
      ({ method, params }) =>
        method === "evener/navigation/read" && (params as NavigationReadParams).resource === "project",
    ),
  ).toBe(false);
});

test("retained unavailable location data does not retry or lose its owner", async () => {
  const child = "local:retained-child";
  const client = navClient();
  navigationStore.setState({ mode: "v3" });
  window.history.pushState({}, "", `/s/${encodeURIComponent(child)}`);
  installLocation({
    generation_id: "generation_test",
    revision: 3,
    ref: child,
    top_level_ref: "local:retained-owner",
    top_level: false,
    tier: "recent",
    session: {
      ref: child,
      host_id: "local",
      session_id: child,
      title: child,
      project: "p",
      state: "idle",
      kind: "subagent",
      live: false,
      children: [],
    },
  });
  render(<AppShell client={client} />);
  await waitFor(() => expect(paneFor(child)?.slot).toBe("secondary"));
  act(() => {
    const key = keyID({ kind: "location", ref: child });
    const resource = navigationStore.getState().resources.get(key);
    if (!resource) throw new Error("location resource missing");
    const resources = new Map(navigationStore.getState().resources);
    resources.set(key, { ...resource, stale: true, error: new Error("temporary navigation failure") });
    navigationStore.setState({ resources });
  });
  await waitFor(() => expect(paneFor("local:retained-owner")?.slot).toBe("main"));
  expect(paneFor(child)?.slot).toBe("secondary");
  await Promise.resolve();
  expect(
    client.calls.filter(
      ({ method, params }) =>
        method === "evener/navigation/read" && (params as NavigationReadParams).resource === "location",
    ).length,
  ).toBe(0);
});

test("a failed first location read keeps the funnel-placed child, its owner, and their neighbors", async () => {
  // The phone journey behind the funnel: the child was opened from its
  // parent (a session pane beside the owner, the opened-from context
  // recorded), the URL publishes /s/<child>, and the child's first location
  // read fails with no retained data. The failed read must not collapse the
  // workspace to a lone child pane: the recorded owner and every neighbor
  // survive, and a later successful read settles the same nested shape
  // without discarding work.
  const child = "local:child";
  const owner = "local:owner";
  window.history.pushState({}, "", `/s/${encodeURIComponent(child)}`);
  installFailedLocation(child);
  act(() => {
    workspaceStore.getState().replacePrimary("session", { ref: owner });
    workspaceStore.getState().openPane("doc", { session: owner, path: "README.md" });
    const childId = workspaceStore.getState().openPane("session", { ref: child }, { slot: "secondary" });
    const childPane = workspaceStore.getState().panes.find((pane) => pane.id === childId);
    if (childPane) recordConversationOpenParent(childPane, owner);
  });
  render(<AppShell client={new FakeClient("ready")} />);

  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: owner });
  expect(paneFor(child)?.slot).toBe("secondary");
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "doc")).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(child)?.id);

  // Recovery: the read succeeds later. The placement settles into the
  // nested shape a successful read produces, still without discarding work.
  act(() => installLocationForRoute(child, owner));
  await waitFor(() => expect(paneFor(child)?.slot).toBe("secondary"));
  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: owner });
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "doc")).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(child)?.id);
});

test("a failed first location read keeps a nested chain's workspace until recovery confirms ownership", async () => {
  // The grandchild journey: root in main, child and grandchild beside it
  // (each opened from its parent, contexts recorded), the URL publishing
  // /s/<grandchild>, and the grandchild's first location read failing. The
  // recorded parent is itself nested, so promoting it through placement
  // would discard the root and every neighbor; the failed read must keep
  // the workspace until a successful read confirms the owner.
  const grandchild = "local:grandchild";
  const child = "local:child";
  const owner = "local:owner";
  window.history.pushState({}, "", `/s/${encodeURIComponent(grandchild)}`);
  installFailedLocation(grandchild);
  act(() => {
    workspaceStore.getState().replacePrimary("session", { ref: owner });
    workspaceStore.getState().openPane("doc", { session: owner, path: "README.md" });
    const childId = workspaceStore.getState().openPane("session", { ref: child }, { slot: "secondary" });
    const childPane = workspaceStore.getState().panes.find((pane) => pane.id === childId);
    if (childPane) recordConversationOpenParent(childPane, owner);
    const grandchildId = workspaceStore.getState().openPane("session", { ref: grandchild }, { slot: "secondary" });
    const grandchildPane = workspaceStore.getState().panes.find((pane) => pane.id === grandchildId);
    if (grandchildPane) recordConversationOpenParent(grandchildPane, child);
  });
  render(<AppShell client={new FakeClient("ready")} />);

  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: owner });
  expect(paneFor(child)?.slot).toBe("secondary");
  expect(paneFor(grandchild)?.slot).toBe("secondary");
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "doc")).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(grandchild)?.id);

  // Recovery: the read confirms the top-level owner. The placement settles
  // into the nested shape a successful read produces, keeping the chain's
  // surviving panes.
  act(() => installLocationForRoute(grandchild, owner));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: owner }));
  expect(paneFor(child)?.slot).toBe("secondary");
  expect(paneFor(grandchild)?.slot).toBe("secondary");
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "doc")).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(grandchild)?.id);
});

// kata 9r5y: the DockHost first-mount race regression test. React fires
// child effects before parent effects within a commit, so DockHost's
// handleReady (called from DockviewReact's own mount effect, a descendant
// of AppShell) runs BEFORE any plain useEffect in AppShell. handleReady's
// boot sequence is "restore the saved layout, then ensure the main slot
// isn't empty (fallback to welcome)". If AppShell opened its initial
// route's pane in a useEffect instead of during render, handleReady would
// restore the saved layout FIRST, and only then would AppShell's effect
// open the routed pane - landing a welcome-route pane ALONGSIDE the
// restored layout as a spurious extra secondary tab, instead of in place.
//
// The render-phase openRouteAsPane call exists to beat that race: it opens
// the route's pane during render, before ANY effect fires, so handleReady
// captures it as a routed pane and re-applies it through replacePrimary
// (which the "welcome" route is excluded from re-applying - kata eve5 -
// because welcome is never itself part of a saved layout, so a re-open
// always resolves to "genuinely new" and would steal focus into a fresh
// secondary tab). This test pins the race end-to-end through the real
// AppShell + DockHost: a saved real layout, reloaded at the welcome route,
// restores exactly that layout with NO spurious welcome tab.
//
// It fails red against the naive "move the open into useEffect" fix:
// handleReady restores the saved session+doc, then the effect opens
// welcome into the secondary slot beside them.
test("kata 9r5y: a reload at the welcome route over a saved layout restores the layout with no spurious welcome tab", async () => {
  // Phase 1: save a REAL layout - a session in main and a doc in secondary -
  // through the real AppShell/DockHost save path.
  await saveRealSessionLayout();
  // localStorage now holds the saved layout (saveRealSessionLayout asserted
  // it), and resetWorkspaceStoreForTests has reset the in-memory workspace.

  // Phase 2: fresh mount at the welcome route ("/"). Deliberately NOT calling
  // localStorage.clear() - the whole point is the saved layout is restored,
  // and the welcome route's pane must NOT land alongside it as a spurious
  // extra tab.
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);

  // The restored session takes main; welcome is never opened beside it.
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:session-a" }));

  // Exactly the saved layout: one session (main) + one doc (secondary). No
  // welcome pane at all - the race would add a welcome pane in secondary.
  expect(workspaceStore.getState().panes).toHaveLength(2);
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "welcome")).toBe(false);
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "session")).toBe(true);
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "doc")).toBe(true);
  expect(screen.queryByText("No session open")).toBeNull();
});

test("Back from an attention target restores the saved conversation the welcome route left in place", async () => {
  await saveRealSessionLayout();
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:session-a" }));
  const origin = workspaceStore.getState().mainPane();
  if (!origin) throw new Error("missing restored reader");
  const lifetime = conversationPaneLifetime(origin);
  // The saved layout keeps its own focus; this leg is about the session
  // reader, so the reader click that starts it focuses main first.
  act(() => workspaceStore.getState().focusPane(origin.id));

  act(() => openAttentionSession("local:destination"));
  const destination = () =>
    workspaceStore.getState().panes.find((pane) => (pane.params as { ref?: string }).ref === "local:destination");
  await waitFor(() => expect(destination()).toBeTruthy());
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination()?.id));

  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });

  // The entry this reader left is "/", not a session URL: Back has to restore
  // its exact reader before the welcome route decides what to place.
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(conversationPaneLifetime(origin)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
});

test("opening /s/{ref} replaces unrelated main session instead of opening a secondary", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:existing" });

  window.history.pushState({}, "", "/s/local:new_session");

  installLocationForRoute("local:new_session");
  render(<AppShell client={new FakeClient("ready")} />);

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:new_session" });
  });
  const sessionPanes = workspaceStore.getState().panes.filter((pane) => pane.type === "session");
  expect(sessionPanes).toHaveLength(1);
  expect(sessionPanes[0]!.slot).toBe("main");
  expect(sessionPanes[0]!.params).toMatchObject({ ref: "local:new_session" });
});

test("an unknown path renders NotFound instead of the workspace", async () => {
  window.history.pushState({}, "", "/not/a/real/route");
  render(<AppShell client={new FakeClient("ready")} />);

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["not-found"]));
  expect(screen.queryByTestId("workspace-host")).toBeNull();
});

test("clicking Go home from NotFound returns to the welcome pane", async () => {
  window.history.pushState({}, "", "/not/a/real/route");
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("Page not found");

  await user.click(screen.getByRole("button", { name: "Go home" }));

  expect(window.location.pathname).toBe("/");
  expect(await screen.findByText("No session open")).toBeTruthy();
});

// kata 9r5y: the warning itself, pinned. NotFound -> "Go home" is the
// shortest path that reaches the render-phase openRouteAsPane call with
// DockHost already mounted and subscribed to workspaceStore: the 404 render
// leaves dockHostHasMountedRef false (route === null, DockHost never
// mounted), so the "/" render still takes the render-phase branch - and by
// then DockHost IS mounted from the same commit. When AppShell subscribes to
// workspaceStore through useSyncExternalStore, that mutation runs the store
// subscriber, which schedules the re-render through React's ordinary
// scheduleUpdateOnFiber path - the one React flags as an update arriving
// from outside the render pass, logging "Cannot update a component
// (`AppShell`) while rendering a different component (`AppShell`)" (both
// names are AppShell here; the check is about how the update was scheduled,
// not about which component it targets). A useReducer dispatch on the
// currently-rendering component takes React's supported
// adjusting-state-during-render path instead and never reaches that check.
//
// The assertion is on a console.error spy, not on test output: vitest's
// piped reporter swallows console output, so a warning here is invisible to
// `make test-web` unless a test captures it. Everything else about the flow
// (route resolves, welcome pane opens) still passes with the warning
// present - only this spy makes it fail.
test("NotFound -> Go home emits no render-phase update warning", async () => {
  const seen: string[] = [];
  const spy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
    seen.push(args.map(String).join(" "));
  });
  try {
    window.history.pushState({}, "", "/not/a/real/route");
    const user = userEvent.setup();
    render(<AppShell client={new FakeClient("ready")} />);
    await screen.findByText("Page not found");

    await user.click(screen.getByRole("button", { name: "Go home" }));

    expect(await screen.findByText("No session open")).toBeTruthy();
  } finally {
    spy.mockRestore();
  }
  expect(seen.filter((m) => m.includes("Cannot update a component"))).toEqual([]);
});

test("navigating from one session deep link to another, post-mount, opens the new one", async () => {
  window.history.pushState({}, "", "/s/local:ref_first");
  installLocationForRoute("local:ref_first");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findAllByText("local:ref_first"); // tab + pane body (no thread name known), both settled

  act(() => {
    window.history.pushState({}, "", "/s/local:ref_second");
    installLocationForRoute("local:ref_second");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await screen.findAllByText("local:ref_second");
  // Main-pane semantics replace the existing main session on deep-link navigation.
  // The second deep link takes focus and closes the previous top-level session.
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["local:ref_second"]);
});

test("browser Back and Forward route notifications replace the primary through AppShell", async () => {
  window.history.pushState({}, "", "/s/local:history_session");
  installLocationForRoute("local:history_session");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findAllByText("local:history_session");

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:history_session",
      path: "README.md",
      kind: "text",
    });
    window.history.pushState({}, "", "/settings");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByRole("navigation", { name: "Settings sections" });
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");

  act(() => {
    window.history.pushState({}, "", "/s/local:history_session");
    installLocationForRoute("local:history_session");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findAllByText("local:history_session");
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:history_session" });

  act(() => {
    window.history.pushState({}, "", "/settings");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByRole("navigation", { name: "Settings sections" });
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
});

// Rail activation must update the canonical URL as well as the workspace. A
// direct store-only activation leaves the shell on /settings, so the next
// Settings click is the same-path navigation the app no longer handles.
test("rail activation updates the URL and a later Settings activation returns Settings to main", async () => {
  const user = userEvent.setup();
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={navClient()} />);
  await screen.findByRole("navigation", { name: "Settings sections" });
  await screen.findByText("Session one");

  const sessionRow = screen.getByText("Session one").closest('[role="treeitem"]');
  expect(sessionRow).not.toBeNull();
  await user.click(screen.getByText("Session one"));

  expect(window.location.pathname).toBe("/s/local%3As1");
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:s1" });
  });

  await user.click(screen.getByTestId("rail-settings"));

  expect(window.location.pathname).toBe("/settings");
  await screen.findByRole("navigation", { name: "Settings sections" });
  const settingsPanes = workspaceStore.getState().panes.filter((pane) => pane.type === "settings");
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
  expect(settingsPanes).toHaveLength(1);
  expect(settingsPanes[0]?.slot).toBe("main");
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "settings" && pane.slot === "secondary")).toBe(
    false,
  );
});

// Closing a deleted session's pane is not enough for the pane the ADDRESS BAR
// names (kata 1hdc): the route-application effect below re-runs on every
// workspace change, so a URL still naming the deleted session re-opens a pane
// for it the instant the rail closes it - "Loading transcript…" forever, for a
// session whose files are gone. The rail's delete path leaves that dead route
// for welcome, which is where DockHost's own relaunch already puts the emptied
// main slot. Driven through the REAL rail delete (menu, confirmation, typed
// mutation, refetch) against the real AppShell + DockHost, because the re-open only
// happens with the route effect and the dock host both live.
test("deleting the session the address bar names lands on welcome instead of re-opening its pane", async () => {
  stoppedTreeSession = true;
  const client = navClient();
  client.on("evener/session/delete", () => ({
    deleted: ["s1"],
    skipped: [],
    navigation: { generation_id: "generation_test", targets: [] },
  }));

  const user = userEvent.setup();
  window.history.pushState({}, "", "/s/local:s1");
  installLocationForRoute("local:s1");
  render(<AppShell client={client} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));

  await user.click(screen.getByRole("button", { name: /actions for session one/i }));
  await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
  await screen.findByRole("button", { name: "Delete" });
  await user.click(screen.getByRole("button", { name: "Delete" }));

  // Both halves on the SAME settled frame: the address bar has left the dead
  // route AND no pane is routed at the deleted session.
  await waitFor(() => {
    expect(window.location.pathname).toBe("/");
    expect(paneFor("local:s1")).toBeUndefined();
  });
  // And it stays that way: the re-open would land on the commit after the
  // close, so flush those effects before reading the workspace one last time.
  await act(async () => {});
  expect(paneFor("local:s1")).toBeUndefined();
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).toBe("welcome");
});

// A route replacement that only opens B leaves A's secondary neighbors in the
// shared workspace. The state assertion catches that additive placement even
// if DockHost happens to hide the stale panel during reconciliation.
test("same-tab navigation from one session to another removes the old main and every secondary pane", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
  });

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:session-a",
      path: "README.md",
      kind: "text",
    });
  });
  expect(workspaceStore.getState().panes).toHaveLength(2);

  act(() => {
    window.history.pushState({}, "", "/s/local:session-b");
    installLocationForRoute("local:session-b");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-b" });
  });
  expect(workspaceStore.getState().panes).toEqual([
    expect.objectContaining({ type: "session", params: { ref: "local:session-b" }, slot: "main" }),
  ]);
});

// Reselecting the same primary identity is an in-place update, not a reason
// to rebuild the workspace and clear useful secondary panes. The encoded
// second pathname reaches the same route through the real popstate seam while
// still producing a distinct pathname update for AppShell.
test("reselecting the same session through a second route notification preserves its secondary pane", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
  });

  const mainId = workspaceStore.getState().mainPane()?.id;
  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:session-a",
      path: "README.md",
      kind: "text",
    });
  });
  const secondaryId = workspaceStore.getState().panes.find((pane) => pane.slot === "secondary")?.id;

  act(() => {
    window.history.pushState({}, "", "/s/local%3Asession-a");
    installLocationForRoute("local:session-a");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(mainId));
  expect(workspaceStore.getState().mainPane()?.id).toBe(mainId);
  expect(workspaceStore.getState().panes.find((pane) => pane.id === secondaryId)?.slot).toBe("secondary");
});

test("Welcome at / preserves existing session panes when no provider is configured", async () => {
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={client} />);
  await screen.findByText(/loading transcript/i);
  const mainId = workspaceStore.getState().mainPane()?.id;
  let secondaryId = "";
  act(() => {
    secondaryId = workspaceStore.getState().openPane("session", { ref: "local:session-b" }, { slot: "secondary" });
    window.history.pushState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByText("No session open");
  await act(async () => credentialsStore.getState().fetch());
  expect(window.location.pathname).toBe("/");
  expect(
    workspaceStore
      .getState()
      .panes.filter((pane) => pane.type === "session")
      .map((pane) => pane.id),
  ).toEqual([mainId, secondaryId]);
});

test("navigating from Settings to /new replaces Settings and clears secondary panes", async () => {
  workspaceStore.getState().openPane("settings", { section: "hub" });
  workspaceStore.getState().openPane("doc", {
    session: "local:settings",
    path: "README.md",
    kind: "text",
  });
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  act(() => {
    window.history.pushState({}, "", "/new");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.type).toBe("spawn");
  });
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(workspaceStore.getState().mainPane()?.slot).toBe("main");
});

// A characterization pin for pre-existing glue the 2026-08-16 settings
// mobile-nav design now depends on: settings' two URL levels (/settings and
// /settings/{section}) swap the SINGLETON pane's params in place via
// replacePrimary on every popstate - the pane id never changes, which is
// exactly why StackHost's focus-keyed back-stack can't see the transition
// and the pane publishes paneBack instead (see chromeStore.ts). The pane-
// level halves of this contract are covered red-first in Settings.test.tsx;
// this covers the routing glue between them.
test("popstate between settings URL levels updates the singleton pane's params in place", async () => {
  window.history.pushState({}, "", "/settings/storage");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ section: "storage" });
  });
  const settingsId = workspaceStore.getState().mainPane()?.id;

  act(() => {
    window.history.pushState({}, "", "/settings");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({});
  });
  expect(workspaceStore.getState().mainPane()?.id).toBe(settingsId);
});

test("a saved session layout is replaced by /settings with Settings as the only main pane", async () => {
  await saveRealSessionLayout();

  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  await waitFor(() => {
    expect(workspaceStore.getState().panes).toEqual([
      expect.objectContaining({ type: "settings", params: {}, slot: "main" }),
    ]);
  });
});

test("a saved session layout is replaced by /new with Spawn as the only main pane", async () => {
  await saveRealSessionLayout();

  window.history.pushState({}, "", "/new");
  render(<AppShell client={new FakeClient("ready")} />);
  expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();

  await waitFor(() => {
    expect(workspaceStore.getState().panes).toEqual([
      expect.objectContaining({ type: "spawn", params: {}, slot: "main" }),
    ]);
  });
});

test("repairs a nested session restored as main when the root route's tree arrives", async () => {
  await saveLegacyNestedMainLayout();
  navigationStore.setState({ mode: "v3" });

  window.history.pushState({}, "", "/");
  render(<AppShell client={navClient()} />);
  act(() => installLocationForRoute("local:child"));

  await waitFor(() => expect(navigationStore.getState().resources).toBeDefined());
  await waitFor(() => {
    expect(paneFor("local:owner")?.slot).toBe("main");
    expect(paneFor("local:child")?.slot).toBe("secondary");
    expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
  });

  expect(workspaceStore.getState().panes).not.toEqual(
    expect.arrayContaining([
      expect.objectContaining({ type: "session", params: { ref: "local:child" }, slot: "main" }),
    ]),
  );
});

test("a nested child route replaces unrelated panes, keeps its owner main, and focuses the child", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
  workspaceStore.getState().openPane("doc", {
    session: "local:unrelated",
    path: "README.md",
    kind: "text",
  });

  window.history.pushState({}, "", "/s/local:child");

  installLocationForRoute("local:child");
  render(<AppShell client={navClient()} />);

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
    expect(paneFor("local:child")?.slot).toBe("secondary");
  });

  const panes = workspaceStore.getState().panes;
  expect(panes.find((pane) => (pane.params as { ref?: string }).ref === "local:unrelated")).toBeUndefined();
  expect(panes.find((pane) => (pane.params as { ref?: string }).ref === "local:owner")?.slot).toBe("main");
  expect(panes.find((pane) => (pane.params as { ref?: string }).ref === "local:child")?.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(
    panes.find((pane) => (pane.params as { ref?: string }).ref === "local:child")?.id,
  );
});

test("a focused same-ref session panel does not steal a settled top-level route", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" }));

  act(() => {
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:session-a" }, { slot: "secondary" });
  });

  await waitFor(() =>
    expect(workspaceStore.getState().focusedPaneId).toBe(
      workspaceStore.getState().panes.find((pane) => pane.type === "sessionTasks")?.id,
    ),
  );
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionTasks")).toBe(true);
});

test("a focused aside-ref session panel does not invalidate a top-level route", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" }));

  act(() => {
    workspaceStore.getState().openPane("session", { ref: "local:sub1" }, { slot: "secondary" });
    workspaceStore.getState().openPane("sessionDetails", { ref: "local:sub1" }, { slot: "secondary" });
  });

  await waitFor(() =>
    expect(workspaceStore.getState().focusedPaneId).toBe(
      workspaceStore.getState().panes.find((pane) => pane.type === "sessionDetails")?.id,
    ),
  );
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
});

test("a focused session panel does not invalidate a settled nested route", async () => {
  window.history.pushState({}, "", "/s/local:child");
  installLocationForRoute("local:child");
  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(paneFor("local:child")?.slot).toBe("secondary"));

  act(() => {
    workspaceStore.getState().openPane("sessionDetails", { ref: "local:child" }, { slot: "secondary" });
  });

  await waitFor(() =>
    expect(workspaceStore.getState().focusedPaneId).toBe(
      workspaceStore.getState().panes.find((pane) => pane.type === "sessionDetails")?.id,
    ),
  );
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
});

test("a deferred deep link beats a restored active session panel", async () => {
  await saveRealSessionPanelLayout();
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v3" });

  window.history.pushState({}, "", "/s/local:child");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(paneFor("local:child")).toBeUndefined();
  act(() => installLocationForRoute("local:child"));
  await waitFor(() => expect(paneFor("local:child")?.slot).toBe("secondary"));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id));
});

test("a fresh navigation to a held child focuses it when the location read is terminal", async () => {
  window.history.pushState({}, "", "/s/local:child");
  installLocationForRoute("local:child");
  render(<AppShell client={navClient()} />);

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
    expect(paneFor("local:child")?.slot).toBe("secondary");
  });
  const childId = paneFor("local:child")?.id;
  const ownerId = workspaceStore.getState().mainPane()?.id;

  // The location read now fails outright, and the user moves to another
  // conversation. The terminal arm must not treat the held child's route as
  // applied while a different conversation is the visible one.
  act(() => installFailedLocation("local:child"));
  act(() => workspaceStore.getState().openPane("session", { ref: "local:other" }, { slot: "secondary" }));
  const otherId = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string }).ref === "local:other")?.id;
  act(() => workspaceStore.getState().focusPane(otherId ?? ""));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(otherId));

  // A fresh navigation (Back/Forward) onto the child's route names the child
  // in the URL, so the visible conversation must become the child's again -
  // by focusing the held pane, never by restructuring the workspace.
  act(() => {
    window.history.pushState({}, "", "/s/local%3Achild");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(childId));
  const panes = workspaceStore.getState().panes;
  expect(panes.find((pane) => pane.id === ownerId)?.params).toEqual({ ref: "local:owner" });
  expect(panes.some((pane) => (pane.params as { ref?: string }).ref === "local:other")).toBe(true);
  expect(panes.filter((pane) => pane.type === "session")).toHaveLength(3);
});

test("a deferred deep link preserves a restored cascade main and its neighbors", async () => {
  const { cascadeId, secondaryId } = await saveRealCascadeLayout();

  // Phase 2: reload the same deep link with the location read still in
  // flight - the boot shape a loaded machine produces. The restored cascade
  // must survive the route's late placement instead of being replaced by a
  // plain pane of the same session (which discarded every neighboring pane
  // with it).
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v3" });
  window.history.pushState({}, "", "/s/local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("sessionZoom"));
  act(() => installLocationForRoute("local:session-a"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.id).toBe(cascadeId));
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([cascadeId, secondaryId]);
  // The placement must not steal focus either: the saved layout's focus (the
  // sessionTasks pane, last focused before the save) is part of the work the
  // deferred route placement preserves.
  expect(workspaceStore.getState().focusedPaneId).toBe(secondaryId);
});

test("deferred location preserves a restored secondary inspector and its real center", async () => {
  const { mainId, cascadeId, secondaryId } = await saveRealSecondaryCascadeLayout();
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v3" });
  const held = heldRouteLocation("local:session-a");
  window.history.pushState({}, "", "/s/local:session-a");
  await act(async () => {
    render(<AppShell client={held.client} />);
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: "local:session-a" }))?.loading).toBe(
    true,
  );
  await act(async () => held.release());
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.id).toBe(mainId));
  expect(workspaceStore.getState().mainPane()?.type).toBe("session");
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, secondaryId, cascadeId]);
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  act(() => returnFromAgentCascade(cascadeId));
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([mainId, secondaryId]);
  expect(workspaceStore.getState().focusedPaneId).toBe(mainId);
});

test("a fresh child pathname waits for real location then focuses its existing conversation over inspection", async () => {
  const { mainId, cascadeId } = await saveRealSecondaryCascadeLayout("local:owner");
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v3" });
  installLocationForRoute("local:owner");
  const held = heldRouteLocation("local:child");
  window.history.pushState({}, "", "/s/local:owner");
  await act(async () => render(<AppShell client={held.client} />));
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  // The cascade column's "Open conversation" link is gone - the column embeds
  // the conversation directly - so the explicit child conversation pane this
  // journey needs is seeded through the workspace seam the link once drove.
  await act(async () => {
    workspaceStore.getState().openPane("session", { ref: "local:child" });
  });
  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && refParam(pane.params) === "local:child");
  if (!child) throw new Error("Missing explicit ordinary child conversation");
  expect(workspaceStore.getState().focusedPaneId).toBe(child.id);
  await act(async () => workspaceStore.getState().focusPane(cascadeId));
  await act(async () => navigate("/s/local:child"));
  expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: "local:child" }))?.loading).toBe(true);
  await act(async () => held.release());
  expect(workspaceStore.getState().mainPane()).toMatchObject({
    id: mainId,
    type: "session",
    params: { ref: "local:owner" },
  });
  expect(workspaceStore.getState().panes).toContain(child);
  expect(
    workspaceStore
      .getState()
      .panes.filter((pane) => pane.type === "session" && refParam(pane.params) === "local:child"),
  ).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(child.id);
  expect(window.location.pathname).toBe("/s/local:child");
});

test.each([
  { ref: "local:session-a", deferred: false },
  { ref: "local:child", deferred: false },
  { ref: "local:child", deferred: true },
])("secondary inspector boot keeps actual $ref conversations with deferred=$deferred", async ({ ref, deferred }) => {
  const { mainId, sourceId, cascadeId, secondaryId } = await saveRealSecondaryCascadeLayout(ref);
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v3" });
  if (!deferred) installLocationForRoute(ref);
  const held = deferred ? heldRouteLocation(ref) : null;
  window.history.pushState({}, "", `/s/${ref}`);
  await act(async () => {
    render(<AppShell client={held?.client ?? navClient()} />);
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  if (held) await act(async () => held.release());
  expect(workspaceStore.getState().mainPane()).toMatchObject({
    id: mainId,
    type: "session",
    params: { ref: ref === "local:child" ? "local:owner" : ref },
  });
  expect(paneFor(ref)?.id).toBe(sourceId);
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual(
    ref === "local:child" ? [mainId, sourceId, secondaryId, cascadeId] : [mainId, secondaryId, cascadeId],
  );
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  const source = paneFor(ref);
  if (!source) throw new Error("Missing restored source");
  const lifetime = conversationPaneLifetime(source);
  act(() => returnFromAgentCascade(cascadeId));
  expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
  expect(paneFor(ref)).toBe(source);
  expect(conversationPaneLifetime(source)).toBe(lifetime);
  expect(workspaceStore.getState().panes.some((pane) => pane.id === secondaryId)).toBe(true);
  expect(workspaceStore.getState().panes.some((pane) => pane.id === cascadeId)).toBe(false);
});

test.each(["transcript", "fallback"] as const)(
  "a settled route preserves focus for %s inspection and its explicit child conversation",
  async (originKind) => {
    window.history.pushState({}, "", "/s/local:session-a");
    installLocationForRoute("local:session-a");
    render(<AppShell client={new FakeClient("ready")} />);
    await screen.findByText(/loading transcript/i);
    const main = workspaceStore.getState().mainPane();
    if (main?.type !== "session") throw new Error("Expected real route session");
    let inspectorId = "";
    await act(async () => {
      const originId = workspaceStore
        .getState()
        .openPane(
          "transcript",
          { ref: originKind === "transcript" ? "local:outside" : "job:outside" },
          { slot: "secondary" },
        );
      inspectorId = enterAgentCascade(
        activityDelegate({ ownerRef: "local:outside", childRef: "local:inspected", delegateId: "edge-outside" }),
        originId,
      );
    });
    expect(workspaceStore.getState().mainPane()).toBe(main);
    expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
    expect(workspaceStore.getState().panes.find((pane) => pane.id === inspectorId)?.slot).toBe("secondary");
    // The cascade column's "Open conversation" link is gone (the column
    // embeds the conversation), so the explicit child conversation opens
    // through the workspace seam the link once drove.
    await act(async () => {
      workspaceStore.getState().openPane("session", { ref: "local:inspected" });
    });
    const child = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "session" && refParam(pane.params) === "local:inspected");
    if (!child) throw new Error("Expected explicit full child conversation");
    expect(workspaceStore.getState().mainPane()).toBe(main);
    expect(workspaceStore.getState().focusedPaneId).toBe(child.id);
    expect(window.location.pathname).toBe("/s/local:session-a");
    for (const pathname of ["/thread/local:session-a", "/s/local:session-a"]) {
      await act(async () => navigate(pathname));
      await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(main.id));
      expect(workspaceStore.getState().mainPane()).toBe(main);
      expect(window.location.pathname).toBe(pathname);
      await act(async () => workspaceStore.getState().focusPane(inspectorId));
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
    }
  },
);

test("switching between /thread and /s refocuses the routed session despite a focused panel", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" }));

  act(() => {
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:session-a" }, { slot: "secondary" });
  });
  const mainId = workspaceStore.getState().mainPane()?.id;
  act(() => {
    window.history.pushState({}, "", "/thread/local:session-a");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(mainId));

  act(() => {
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:session-a" }, { slot: "secondary" });
    window.history.pushState({}, "", "/s/local:session-a");
    installLocationForRoute("local:session-a");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(mainId));
});

test("a placement guard armed for one pathname does not mark a concurrent pathname placed", async () => {
  window.history.pushState({}, "", "/s/local:session-base");
  installLocationForRoute("local:session-base");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());

  let redirected = false;
  const unsubscribe = workspaceStore.subscribe(() => {
    const main = workspaceStore.getState().mainPane();
    const ref = (main?.params as { ref?: unknown } | undefined)?.ref;
    if (!redirected && ref === "local:session-armed") {
      redirected = true;
      window.history.pushState({}, "", "/s/local:session-next");
      installLocationForRoute("local:session-next");
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  });

  act(() => {
    window.history.pushState({}, "", "/s/local:session-armed");
    installLocationForRoute("local:session-armed");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local:session-next"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-next" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
  unsubscribe();
});

test("a focused non-panel pane is re-focused to the routed top-level session", async () => {
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" }));
  const mainId = workspaceStore.getState().mainPane()?.id;

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:session-a",
      path: "README.md",
      kind: "text",
    });
  });

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(mainId));
});

test("a focused non-panel pane is re-focused to the routed nested session", async () => {
  window.history.pushState({}, "", "/s/local:child");
  installLocationForRoute("local:child");
  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(paneFor("local:child")?.slot).toBe("secondary"));
  const childId = paneFor("local:child")?.id;

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:child",
      path: "README.md",
      kind: "text",
    });
  });

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(childId));
});

const focusFileReference = {
  path: "docs/focus.md",
  cwd: "/tmp/project",
  readTarget: "/tmp/project/docs/focus.md",
  provenance: "relative",
} as const;

async function settledDocumentRoute(ref: string): Promise<void> {
  // Script only the HTTP boundary. AppShell, DockHost, opener and workspace
  // remain real, including the route effect's post-open publications.
  vi.stubGlobal("fetch", async () => new Response("# Focus document", { headers: { "Content-Type": "text/plain" } }));
  window.history.pushState({}, "", `/s/${ref}`);
  installLocationForRoute(ref);
  await act(async () => render(<AppShell client={new FakeClient("ready")} />));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(ref)?.id));
}

test("filename open retains focus after a cold location resolves for an already placed source", async () => {
  const ref = "local:session-a";
  vi.stubGlobal("fetch", async () => new Response("# Focus document", { headers: { "Content-Type": "text/plain" } }));
  window.history.pushState({}, "", `/s/${encodeURIComponent(ref)}`);
  // The browser fixture opens its source before mounting AppShell, then the
  // real location request resolves after the shell has armed its pending route.
  const sourceId = workspaceStore.getState().openPane("session", { ref });
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("source missing");
  await act(async () => render(<AppShell client={navClient()} />));
  await waitFor(() =>
    expect(
      [...navigationStore.getState().resources.values()].find(
        (resource) => resource.key.kind === "location" && resource.key.ref === ref,
      )?.data,
    ).toMatchObject({ ref, top_level: true }),
  );
  expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
  expect(workspaceStore.getState().mainPane()).toBe(source);

  await act(async () => openDocBeside({ session: ref, reference: focusFileReference, sourcePaneId: source.id }));

  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(doc).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
  expect(workspaceStore.getState().mainPane()).toBe(source);
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)).toEqual({ reference: focusFileReference, origin: source, reopen: 0 });
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(ref)}`);
});

test.each(["local:session-a", "local:child"])(
  "filename open retains document focus and exact promoted source on settled %s route",
  async (ref) => {
    await settledDocumentRoute(ref);
    const source = paneFor(ref);
    if (!source) throw new Error("routed source missing");
    const retainedPanes = [...workspaceStore.getState().panes];

    await act(async () => openDocBeside({ session: ref, reference: focusFileReference, sourcePaneId: source.id }));

    const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    expect(doc).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
    expect(workspaceStore.getState().mainPane()).toBe(source);
    expect(source.slot).toBe("main");
    expect(doc?.slot).toBe("secondary");
    expect(doc?.params).toEqual({ session: ref, path: "docs/focus.md", kind: "file" });
    if (!doc) throw new Error("document missing");
    expect(documentPaneState(doc)).toEqual({ reference: focusFileReference, origin: source, reopen: 0 });
    for (const pane of retainedPanes) expect(workspaceStore.getState().panes).toContain(pane);
    expect(window.location.pathname).toBe(`/s/${ref}`);
  },
);

test("initial filename open survives a synchronous workspace commit before its binding is published", async () => {
  await settledDocumentRoute("local:session-a");
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("source missing");
  // A native filename listener can trigger an external-store consumer commit
  // during openPane, before openDocBeside records the new document's binding.
  // Force that real React commit rather than batching both publications in act.
  const unsubscribe = workspaceStore.subscribe((state) => {
    const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
    if (focused?.type === "doc" && !documentPaneState(focused)) flushSync(() => {});
  });
  onTestFinished(unsubscribe);

  await act(async () =>
    openDocBeside({ session: "local:session-a", reference: focusFileReference, sourcePaneId: source.id }),
  );

  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(doc).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
  expect(workspaceStore.getState().mainPane()).toBe(source);
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)?.origin).toBe(source);
});

test("a cascade column's filename open keeps its document focused beside the cascade", async () => {
  vi.stubGlobal("fetch", async () => new Response("# Focus document", { headers: { "Content-Type": "text/plain" } }));
  window.history.pushState({}, "", "/s/local:session-a");
  installLocationForRoute("local:session-a");
  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:session-a")?.id));
  const source = paneFor("local:session-a");
  if (source?.type !== "session") throw new Error("expected the routed conversation");
  // Drill the cascade as the sidebar's inspect row does: the zoom hosts the
  // child conversation as its readable column.
  let cascadeId = "";
  act(() => {
    cascadeId = enterAgentCascade(
      activityDelegate({
        ownerRef: "local:session-a",
        childRef: "local:child",
        delegateId: "edge-child",
      }),
      source.id,
    );
    workspaceStore.getState().focusPane(cascadeId);
  });
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade) throw new Error("cascade did not open");
  // The column's real filename-open producer, naming the hosting zoom pane.
  await act(async () =>
    openDocBeside({ session: "local:child", reference: focusFileReference, sourcePaneId: cascadeId }),
  );
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(doc).toBeDefined();
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)?.origin).toBe(cascade);
  expect(workspaceStore.getState().focusedPaneId).toBe(doc.id);
  // The cascade keeps its inspection placement and the routed conversation
  // keeps main.
  expect(cascade.slot).toBe("secondary");
  expect(workspaceStore.getState().mainPane()).toBe(source);
  // Settle the route's deferred generic-document recheck: the zoom-hosted
  // document is the route's companion, so focus must stay with it.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(doc.id);
  expect(workspaceStore.getState().mainPane()).toBe(source);
});

test("initial filename open retains focus through a reference-only document binding publication", async () => {
  const ref = "local:session-a";
  await settledDocumentRoute(ref);
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("source missing");
  // DocPane's layout hydration can publish a reference before the opener's
  // exact owner is available. Exercise that canonical workspace publication
  // during the real opener, with a real synchronous AppShell commit.
  const unsubscribe = workspaceStore.subscribe((state) => {
    const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
    if (focused?.type === "doc" && !documentPaneState(focused)) {
      recordDocumentPaneState(focused, { reference: focusFileReference, origin: undefined, reopen: 0 });
      flushSync(() => {});
    }
  });
  onTestFinished(unsubscribe);

  await act(async () => openDocBeside({ session: ref, reference: focusFileReference, sourcePaneId: source.id }));

  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(doc).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
  expect(workspaceStore.getState().mainPane()).toBe(source);
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)).toEqual({ reference: focusFileReference, origin: source, reopen: 0 });
});

test.each([
  { name: "nested session", route: "local:child", session: "local:child" },
  { name: "secondary delegate transcript", route: "local:session-a", session: "local:sub1" },
])("reference-only publication retains the promoted $name before its exact owner arrives", async (fixture) => {
  await settledDocumentRoute(fixture.route);
  let source = paneFor(fixture.session);
  if (fixture.session === "local:sub1") {
    let sourceId = "";
    act(() => {
      sourceId = workspaceStore
        .getState()
        .openPane("transcript", { ref: fixture.session, parentRef: fixture.route }, { slot: "secondary" });
    });
    source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  }
  if (!source) throw new Error("source missing");
  const exactSource = source;
  let intermediateDocument: OpenPaneRecord | undefined;
  const unsubscribe = workspaceStore.subscribe((state) => {
    const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
    if (focused?.type !== "doc" || documentPaneState(focused)) return;
    intermediateDocument = focused;
    recordDocumentPaneState(focused, { reference: focusFileReference, origin: undefined, reopen: 0 });
    flushSync(() => {});
    expect(workspaceStore.getState().focusedPaneId).toBe(focused.id);
    expect(workspaceStore.getState().mainPane()).toBe(exactSource);
    expect(documentPaneState(focused)?.origin).toBeUndefined();
  });
  onTestFinished(unsubscribe);

  await act(async () =>
    openDocBeside({ session: fixture.session, reference: focusFileReference, sourcePaneId: exactSource.id }),
  );

  expect(intermediateDocument).toBeDefined();
  if (!intermediateDocument) throw new Error("intermediate document missing");
  expect(workspaceStore.getState().focusedPaneId).toBe(intermediateDocument.id);
  expect(workspaceStore.getState().mainPane()).toBe(exactSource);
  expect(documentPaneState(intermediateDocument)).toEqual({
    reference: focusFileReference,
    origin: exactSource,
    reopen: 0,
  });
});

test.each(["local:session-a", "local:child"])(
  "a reference-only generic document still yields focus to the settled %s route",
  async (ref) => {
    await settledDocumentRoute(ref);
    const source = paneFor(ref);
    if (!source) throw new Error("source missing");
    let doc: OpenPaneRecord | undefined;
    await act(async () => {
      const id = workspaceStore.getState().openPane("doc", {
        session: ref,
        path: focusFileReference.path,
        kind: "file",
      });
      doc = workspaceStore.getState().panes.find((pane) => pane.id === id);
      if (!doc) throw new Error("document missing");
      recordDocumentPaneState(doc, { reference: focusFileReference, origin: undefined, reopen: 0 });
      flushSync(() => {});
    });

    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(source.id));
    if (!doc) throw new Error("document missing");
    expect(documentPaneState(doc)?.origin).toBeUndefined();
  },
);

test("same-pane filename reopen keeps document selection and increments its retained read generation", async () => {
  await settledDocumentRoute("local:session-a");
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("source missing");
  await act(async () =>
    openDocBeside({ session: "local:session-a", reference: focusFileReference, sourcePaneId: source.id }),
  );
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!doc) throw new Error("document missing");
  act(() => workspaceStore.getState().focusPane(source.id));

  await act(async () =>
    openDocBeside({ session: "local:session-a", reference: focusFileReference, sourcePaneId: source.id }),
  );

  expect(workspaceStore.getState().focusedPaneId).toBe(doc.id);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toEqual([doc]);
  expect(documentPaneState(doc)).toEqual({ reference: focusFileReference, origin: source, reopen: 1 });
  expect(workspaceStore.getState().mainPane()).toBe(source);
});

test.each([
  { name: "secondary delegate session", type: "session", params: { ref: "local:sub1" }, session: "local:sub1" },
  {
    name: "same-ref read-only transcript",
    type: "transcript",
    params: { ref: "local:session-a", parentRef: "local:session-a" },
    session: "local:session-a",
  },
  {
    name: "delegate transcript",
    type: "transcript",
    params: { ref: "local:sub1", parentRef: "local:session-a" },
    session: "local:sub1",
  },
  {
    name: "job transcript",
    type: "transcript",
    params: { ref: "job:job_focus", parentRef: "local:session-a" },
    session: "local:session-a",
  },
] as const)("filename open from $name keeps that exact source main and the document selected", async (fixture) => {
  await settledDocumentRoute("local:session-a");
  const routedSource = workspaceStore.getState().mainPane();
  let sourceId = "";
  act(() => {
    sourceId = workspaceStore.getState().openPane(fixture.type, fixture.params, { slot: "secondary" });
  });
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  if (!source) throw new Error("secondary source missing");

  await act(async () =>
    openDocBeside({ session: fixture.session, reference: focusFileReference, sourcePaneId: sourceId }),
  );

  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(workspaceStore.getState().mainPane()).toBe(source);
  expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
  expect(routedSource?.slot).toBe("secondary");
  expect(workspaceStore.getState().panes).toContain(routedSource);
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)?.origin).toBe(source);
  expect(doc.params).toEqual({ session: fixture.session, path: "docs/focus.md", kind: "file" });
});

test("settled location refresh and reconnect retain filename document focus and owner", async () => {
  await settledDocumentRoute("local:child");
  const source = paneFor("local:child");
  if (!source) throw new Error("child source missing");
  await act(async () =>
    openDocBeside({ session: "local:child", reference: focusFileReference, sourcePaneId: source.id }),
  );
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");

  await act(async () => {
    connectionStore.setState({ state: "reconnecting" });
    installLocationForRoute("local:child");
  });
  await act(async () => {
    connectionStore.setState({ state: "ready" });
    installLocationForRoute("local:child");
  });

  expect(workspaceStore.getState().focusedPaneId).toBe(doc?.id);
  expect(workspaceStore.getState().mainPane()).toBe(source);
  if (!doc) throw new Error("document missing");
  expect(documentPaneState(doc)?.origin).toBe(source);
});

test.each(["missing origin", "replaced origin", "session mismatch", "path mismatch", "unpromoted origin"])(
  "settled route rejects a filename document with %s",
  async (invalidBinding) => {
    await settledDocumentRoute("local:session-a");
    const source = workspaceStore.getState().mainPane();
    if (!source) throw new Error("source missing");
    await act(async () => {
      openDocBeside({ session: "local:session-a", reference: focusFileReference, sourcePaneId: source.id });
      const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
      if (!doc) throw new Error("document missing");
      if (invalidBinding === "missing origin") workspaceStore.getState().closePane(source.id);
      if (invalidBinding === "replaced origin") {
        workspaceStore.setState({
          panes: workspaceStore.getState().panes.map((pane) => (pane === source ? { ...pane } : pane)),
        });
      }
      if (invalidBinding === "session mismatch" || invalidBinding === "path mismatch") {
        doc.params = {
          session: invalidBinding === "session mismatch" ? "local:other" : "local:session-a",
          path: invalidBinding === "path mismatch" ? "docs/other.md" : "docs/focus.md",
          kind: "file",
        };
        workspaceStore.setState({ panes: [...workspaceStore.getState().panes] });
      }
      if (invalidBinding === "unpromoted origin") {
        const otherId = workspaceStore.getState().openPane("session", { ref: "local:other" });
        workspaceStore.getState().promotePane(otherId);
        workspaceStore.getState().focusPane(doc.id);
      }
    });

    await waitFor(() => {
      expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
      expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
    });
  },
);

test.each(["/thread/local:child", "/s/local:next", "/settings/hub"])(
  "explicit popstate navigation to %s overrides filename source promotion",
  async (pathname) => {
    await settledDocumentRoute("local:child");
    const source = paneFor("local:child");
    if (!source) throw new Error("source missing");
    await act(async () => {
      openDocBeside({ session: "local:child", reference: focusFileReference, sourcePaneId: source.id });
      window.history.pushState({}, "", pathname);
      if (pathname === "/s/local:next") installLocationForRoute("local:next");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });

    await waitFor(() => {
      if (pathname === "/thread/local:child") {
        expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
        expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
      } else {
        expect(workspaceStore.getState().mainPane()?.type).toBe(
          pathname.startsWith("/settings") ? "settings" : "session",
        );
        expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
      }
    });
  },
);

test("cold route placement overrides a previously bound filename document", async () => {
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:child" });
  openDocBeside({ session: "local:child", reference: focusFileReference, sourcePaneId: sourceId });
  await settledDocumentRoute("local:child");

  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
});

test("reset with reused pane IDs does not resurrect a filename document's runtime owner", async () => {
  await settledDocumentRoute("local:session-a");
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("source missing");
  await act(async () => {
    openDocBeside({ session: "local:session-a", reference: focusFileReference, sourcePaneId: source.id });
    resetWorkspaceStoreForTests();
    workspaceStore.getState().openPane("session", { ref: "local:session-a" });
    workspaceStore.getState().openPane("doc", { session: "local:session-a", path: "docs/focus.md", kind: "file" });
  });

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id));
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!doc) throw new Error("restored document missing");
  expect(documentPaneState(doc)?.origin).toBeUndefined();
});

test("successful Spawn navigation replaces Spawn with the created session and clears old secondary panes", async () => {
  const fake = navClient();
  fake.on("thread/start", () => threadStartResponse("local:created"));
  window.history.pushState({}, "", "/new");
  render(<AppShell client={fake} />);
  expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("spawn"));

  act(() => {
    workspaceStore.getState().openPane("doc", {
      session: "local:spawn",
      path: "README.md",
      kind: "text",
    });
  });
  await userEvent.setup().click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Acreated"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:created" });
  });
  expect(workspaceStore.getState().panes).toEqual([
    expect.objectContaining({ type: "session", params: { ref: "local:created" }, slot: "main" }),
  ]);
  expect(workspaceStore.getState().panes.some((pane) => pane.type === "spawn")).toBe(false);
});

test("navigating from a 404 straight to a session deep link opens only that pane, no spurious welcome tab", async () => {
  // DockHost never mounts while urlToPane() returns null (NotFound renders
  // in its place - see AppShell's return), so this is the first time
  // DockHost mounts at all in this test, and it happens on a LATER render
  // than AppShell's own initial one - a different case from every deep-
  // link test above, which all mount DockHost on the very first render.
  window.history.pushState({}, "", "/not/a/real/route");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("Page not found");

  act(() => {
    window.history.pushState({}, "", "/s/local:ref_from_404");
    installLocationForRoute("local:ref_from_404");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await screen.findByText(/loading transcript/i);
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["local:ref_from_404"]);
});

test("a saved welcome layout is replaced by a fresh routed primary, which lands focused", async () => {
  // Phase 1: generate a REAL saved layout at the default route (the
  // welcome pane). The welcome pane's own addPanel() schedules the debounced
  // save (addPanel fires onDidLayoutChange - see DockHost.test.tsx's own
  // probe-verified comment on that), and unmount FLUSHES that pending save
  // rather than dropping it, so the write has landed by the time unmount()
  // returns - no waiting out the 400ms debounce on the real clock, which is
  // what used to make this the slowest test in the file.
  window.history.pushState({}, "", "/");
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  unmount();
  // Asserting the saved layout actually CONTAINS the welcome pane, not
  // merely that the key exists: phase 2's whole subject is a saved welcome
  // layout being replaced by a routed primary, so an empty or degenerate
  // layout here would leave that assertion passing for the wrong reason.
  const savedPanels = (
    JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? "null") as {
      panels?: Record<string, { params?: { paneType?: string } }>;
    } | null
  )?.panels;
  expect(Object.values(savedPanels ?? {}).map((p) => p.params?.paneType)).toEqual(["welcome"]);
  resetWorkspaceStoreForTests(); // simulates a fresh page load: in-memory workspace state resets

  // Phase 2: fresh mount at a NEW deep link. Deliberately NOT calling
  // localStorage.clear() here, unlike every other test in this file (see
  // beforeEach above, which blinds the rest of the suite to this path) -
  // the whole point is proving the stale saved welcome layout from phase 1
  // is replaced by this freshly-routed primary rather than leaving the
  // welcome pane beside it.
  window.history.pushState({}, "", "/s/local:ref_new_session");
  installLocationForRoute("local:ref_new_session");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByText(/loading transcript/i)).toBeTruthy();
  // This is a replacement: the saved layout is a lone WELCOME pane in the
  // main slot, and welcome is that slot's empty state, so the routed session
  // DISPLACES it rather than opening beside it. The routed primary remains
  // focused after the replacement.
  expect(workspaceStore.getState().panes.map((p) => p.type)).toEqual(["session"]);
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:ref_new_session" });
  expect(screen.queryByText("No session open")).toBeNull();
});

test("deep-linking to a nested /s/{ref} opens the top-level owner in main and nested in secondary after tree arrival", async () => {
  navigationStore.setState({ mode: "v3" });
  const client = navClient();
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "location" && params.ref === "local:sub1") {
      throw new WireError("location unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return navigationRead(params);
  });

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={client} />);

  act(() =>
    navigationStore.setState({
      resources: new Map(
        [...navigationStore.getState().resources].filter(([, resource]) => resource.key.kind !== "location"),
      ),
      mode: "v3",
    }),
  );
  await waitFor(() => expect(paneFor("local:sub1")?.slot).toBe("main"));
  expect(paneFor("local:s1")).toBeUndefined();

  act(() => installLocationForRoute("local:sub1"));
  expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: "local:sub1" }))?.data).toMatchObject({
    top_level: false,
  });
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" });
  });
  await waitFor(() => {
    expect(paneFor("local:sub1")?.slot).toBe("secondary");
  });

  const mainId = workspaceStore.getState().mainPane()?.id;
  const childId = paneFor("local:sub1")?.id;
  let workspaceUpdates = 0;
  const unsubscribe = workspaceStore.subscribe(() => {
    workspaceUpdates += 1;
  });
  act(() => {
    window.history.pushState({}, "", "/s/local%3Asub1");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Asub1"));
  unsubscribe();

  expect(workspaceUpdates).toBe(0);
  expect(workspaceStore.getState().mainPane()?.id).toBe(mainId);
  expect(paneFor("local:sub1")?.id).toBe(childId);
  expect(workspaceStore.getState().focusedPaneId).toBe(childId);
});

test("nested deep-link remains closed for a missing location until a later location arrives", async () => {
  navigationStore.setState({ mode: "v3" });
  const client = navClient();
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "location" && params.ref === "local:sub1") {
      throw new WireError("location unavailable", -32014, { evenerErrorInfo: "actionUnavailable" });
    }
    return navigationRead(params);
  });

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={client} />);

  act(() =>
    navigationStore.setState({
      resources: new Map(
        [...navigationStore.getState().resources].filter(([, resource]) => resource.key.kind !== "location"),
      ),
      mode: "v3",
    }),
  );
  await waitFor(() => expect(paneFor("local:sub1")?.slot).toBe("main"));
  act(() => installLocationForRoute("local:sub1"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" });
  });
  expect(paneFor("local:sub1")?.slot).toBe("secondary");
});

test("deep-linking to /settings replaces any existing main pane", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:main_session" });
  workspaceStore.getState().openPane("settings", { section: "stale_credentials" }, { slot: "secondary" });

  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
  const workspace = workspaceStore.getState();
  expect(workspace.panes.filter((pane) => pane.type === "session")).toHaveLength(0);
  expect(workspace.panes.filter((pane) => pane.type === "settings")).toHaveLength(1);
  expect(workspace.panes.find((pane) => pane.slot === "secondary" && pane.type === "settings")).toBeUndefined();
});

// --- single-pane mode (/thread/{ref} share link, wave 8 T1) -----------

test("deep-linking to /thread/{ref} opens the session pane chrome-stripped (rail hidden, marker set)", async () => {
  window.history.pushState({}, "", "/thread/local:ref_shared");
  render(<AppShell client={new FakeClient("ready")} />);
  // Single-pane mode never mounts RailHost, so this route has no rail-owned
  // navigation read. The notification engine's baseline AppWire read is
  // independent of the rail and already covers this path in production.
  await act(async () => {
    await Promise.resolve();
  });

  // /thread/{ref} routes to the SESSION pane (its loading text proves the
  // pane mounted), not the never-URL'd transcript pane.
  expect(await screen.findByText(/loading transcript/i)).toBeTruthy();
  // The shell root carries the single-pane marker T6 keys its layout off.
  expect(document.querySelector("[data-single-pane]")).not.toBeNull();
  // The rail chrome (its search/new/settings entry points, floor §2.3) is
  // stripped - RailHost isn't rendered at all.
  expect(screen.queryByTestId("rail-search")).toBeNull();
});

test("a normal /s/{ref} route keeps the rail and sets no single-pane marker", async () => {
  window.history.pushState({}, "", "/s/local:ref_normal");
  installLocationForRoute("local:ref_normal");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByText(/loading transcript/i)).toBeTruthy();
  expect(document.querySelector("[data-single-pane]")).toBeNull();
  // Desktop rail renders (default auto mode, jsdom's wide no-matchMedia
  // viewport) - the contrast that proves the /thread case actually suppressed it.
  expect(await screen.findByTestId("rail-search")).toBeTruthy();
});

test("the session status bar belongs to its pane with no shell-wide duplicate", async () => {
  window.history.pushState({}, "", "/s/local:ref_normal");
  installLocationForRoute("local:ref_normal");
  render(<AppShell client={new FakeClient("ready")} />);

  const bars = await screen.findAllByTestId("statusbar");
  expect(bars).toHaveLength(1);
  const bar = bars[0]!;
  expect(bar.closest("[data-testid='pane-edge-footer']")).not.toBeNull();
  const column = bar.closest("[data-testid='workspace-column']");
  expect(column).not.toBeNull();
  expect(column?.querySelector("[data-testid='workspace-host']")).not.toBeNull();
  expect(column?.contains(await screen.findByTestId("rail-search"))).toBe(false);
});

// --- settings routing (this task) -------------------------------------

test("navigating to /settings/{section} shows that section, replacing main settings and removing secondary settings", async () => {
  workspaceStore.setState({
    panes: [
      {
        id: "settings-main",
        type: "settings",
        params: { section: "hub" },
        slot: "main",
      },
      {
        id: "settings-secondary",
        type: "settings",
        params: { section: "credentials" },
        slot: "secondary",
      },
    ],
    focusedPaneId: "settings-secondary",
  });

  window.history.pushState({}, "", "/settings/theme");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Appearance" }).getAttribute("aria-current")).toBe("page");
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Appearance"]);
  const mainSettingsPane = workspaceStore.getState().mainPane();
  expect(mainSettingsPane).not.toBeNull();
  expect(mainSettingsPane!.id).toBe("settings-main");
  const settingsPanes = workspaceStore.getState().panes.filter((pane) => pane.type === "settings");
  expect(settingsPanes).toHaveLength(1);
  expect(settingsPanes[0]!.slot).toBe("main");
  expect(settingsPanes[0]!.params).toMatchObject({ section: "theme" });
  expect(mainSettingsPane!.params).toMatchObject({ section: "theme" });
  expect(mainSettingsPane!.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe("settings-main");
});

test.each(["general", "display", "plugins", "unknown-section"])(
  "mobile Back to retired settings %s replaces history and preserves Forward",
  async (section) => {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
    const search = section === "plugins" ? "?host=beta" : "";
    window.history.pushState({ retained: section }, "", `/settings/${section}${search}`);
    window.history.pushState({}, "", `/settings/hub${search}`);
    render(<AppShell client={new FakeClient("ready")} />);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ section: "hub" }));
    const push = vi.spyOn(window.history, "pushState");
    const replace = vi.spyOn(window.history, "replaceState");

    try {
      act(() => window.history.back());
      await waitFor(() => expect(window.location.pathname).toBe("/settings/theme"));
      expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ section: "theme" });
      expect(window.location.search).toBe(search);
      expect(window.history.state).toEqual({ retained: section });
      expect(push).not.toHaveBeenCalled();
      expect(replace).toHaveBeenCalledTimes(1);

      act(() => window.history.forward());
      await waitFor(() => expect(window.location.pathname).toBe("/settings/hub"));
      await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ section: "hub" }));
      expect(push).not.toHaveBeenCalled();
    } finally {
      push.mockRestore();
      replace.mockRestore();
    }
  },
);

test("deep-linking to bare /settings opens the settings pane on its default (Appearance) section", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Appearance" }).getAttribute("aria-current")).toBe("page");
});

test("deep-linking to /credentials resolves to the settings pane, pre-focused on credentials", async () => {
  window.history.pushState({}, "", "/credentials");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Providers & credentials" }).getAttribute("aria-current")).toBe("page");
  // paneToURL's own canonical form, not the /credentials alias - see
  // routing.test.ts's "paneToURL formats the credentials section" test.
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Providers & credentials"]);
});

test("navigating from one settings section to another, post-mount, updates the SAME pane (singleton) rather than opening a second tab", async () => {
  const user = userEvent.setup();
  window.history.pushState({}, "", "/settings/hub");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });
  const initialPaneId = workspaceStore.getState().mainPane()?.id;

  await user.click(screen.getByRole("button", { name: "Storage" }));

  expect(screen.getByRole("button", { name: "Storage" }).getAttribute("aria-current")).toBe("page");
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Storage"]);
  expect(workspaceStore.getState().mainPane()?.id).toBe(initialPaneId);
});

// --- kata 11ee: spawn singleton refocus must still apply a fresh ?dir= ----

test("kata 11ee: navigating to /new?dir= a second time, with the spawn pane already open, still prefills the new dir", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  render(<AppShell client={new FakeClient("ready")} />);
  // The working directory is a PathField: its closed trigger holds the path as
  // text (plus a chevron and a screen-reader hint), so the value is matched
  // inside that text rather than read off an input's .value.
  await waitFor(() =>
    expect(screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" }).textContent).toContain(
      "/home/me/app",
    ),
  );

  // A second /new?dir= navigation (e.g. RailRow's own spawnInProject, for a
  // DIFFERENT project) while the spawn pane is already open and focused -
  // openPane's singleton dedup refocuses the same pane rather than opening
  // (or remounting) a second one, exactly the shape this kata's own
  // openRouteAsPane -> workspaceStore.openPane("spawn", {}) path produces.
  act(() => {
    window.history.pushState({}, "", "/new?dir=%2Fhome%2Fother");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  // Still exactly one spawn tab (singleton, not a duplicate) - and it now
  // shows the SECOND navigation's dir, not the first one silently retained.
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["New session"]);
  await waitFor(() =>
    expect(screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" }).textContent).toContain(
      "/home/other",
    ),
  );
});
