import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  InitializeResponse,
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionLocation,
  NavigationSessionSummary,
  NavigationSnapshot,
  ThreadStartResponse,
} from "@evener/appwire-client";
import {
  APPWIRE_PROTOCOL_VERSION,
  AppwireClient,
  acquireThreadSubscription,
  type ConnectionState,
  WireError,
} from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { StrictMode, useEffect } from "react";
import { flushSync } from "react-dom";

// Session-title queries in this file target navigation, so keep them scoped to
// the rail rather than matching the same session in workspace content.
const rail = () => within(screen.getByTestId("rail"));

import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { MotionProvider } from "../motion";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { openDocBeside } from "../panes/doc/openDoc";
import * as composerFocus from "../panes/session/composer/composerFocus";
import { OpenTranscriptButton } from "../panes/session/transcript/openTranscript";
import { retainedTranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { installJobLogRangeGeometry } from "../panes/transcript/JobLogTestUtils";
import { enterAgentCascade, returnFromAgentCascade } from "../panes/zoom/actions";
import { cascadeClient, cascadeContext } from "../panes/zoom/cascadeTestUtils";
import type { SessionZoomParams } from "../panes/zoom/intent";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import * as backgroundTranscriptsModule from "../stores/backgroundTranscripts";
import { connectionStore } from "../stores/connection";
import { credentialsStore } from "../stores/credentials";
import { selectNeedsYouRows } from "../stores/navigation/selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import {
  activityContext,
  activityDelegate,
  activityDetailsThread,
  answerActivityRead,
} from "../stores/sessionActivityTestUtils";
import { SessionEntryCache } from "../stores/sessionEntryCache";
import { resetSettingsHostForTests, settingsHostStore } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { holdNextWriteTransaction } from "../stores/testing/stalledIndexedDB";
import * as threadsModule from "../stores/threads";
import { resetThreadsStoreForTests, setProductionEntryCacheForTests, threadsStore } from "../stores/threads";
import { readModuleCss } from "../styles/cssBlock";
import { emptyStateReasons } from "../widgets/emptystate/emptyStateTestUtils";
import { AppShell } from "./AppShell";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitybar/activitySidebarStore";
import { currentAttentionEntryToken, openAttentionSession } from "./attentionHistory";
import { ClientProvider } from "./clientContext";
import { DockHost } from "./DockHost";
import { resetDockChunkForTests } from "./DockRegion";
import { focusedSessionPaneId, focusedSessionRef } from "./focusedSession";
import { resolveFocusedComposer } from "./holdhints/HoldHints";
import { NeedsYouProvider, useNeedsYou } from "./NeedsYouProvider";
import * as needsYouActionsModule from "./needsYouActions";
import { paletteStore } from "./palette/paletteController";
import { conversationPaneLifetime } from "./paneLifetime";
import * as routingModule from "./routing";
import { navigate, refParam, routeHistoryEntryId } from "./routing";
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

const THREAD_CAPABILITIES = {
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
};

function threadStartResponse(ref: string): ThreadStartResponse {
  return {
    thread: {
      id: ref.slice(ref.indexOf(":") + 1),
      sessionId: `sess_${ref}`,
      preview: "test",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "local",
      evener: { ref, capabilities: THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    turn: { id: "turn_1", itemsView: "full", status: "idle" },
  };
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

// A first location read that failed outright: no retained data, so the
// route's placement must decide from the workspace it already holds.
function installFailedLocation(ref: string): void {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: null,
    loadedRevision: null,
    targetRevision: null,
    forceToken: 0,
    etag: null,
    loading: false,
    stale: false,
    error: new Error("location read failed"),
    generationID: "generation_test",
  });
  navigationStore.setState({ mode: "v3", resources });
}

function installNeedsYouRows(refs = ["local:ny1", "local:ny2"]): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = refs.map((ref, index) => ({
    ...TREE_SESSION,
    ref,
    session_id: ref,
    title: `Needs you ${index + 1}`,
    state: "awaiting",
  }));
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(key));
  navigationStore.setState({ mode: "v3", resources, visibleResourceKeys });
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
  expect(workspaceStore.getState().panes).toEqual(ORDERING_SAVED_PANES);
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
      expect(workspaceStore.getState().panes).toEqual([
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
      expect(workspaceStore.getState().panes).toEqual(ORDERING_SAVED_PANES);
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
      expect(witnesses.find((w) => w.event === "layout-read")?.panes).toEqual([
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
      expect(workspaceStore.getState().panes).toEqual([
        { id: "pane_session_3", type: "session", params: { ref: "local:different-root" }, slot: "main" },
      ]);
      expect(workspaceStore.getState().focusedPaneId).toBe("pane_session_3");
      expect(getDockviewApi()?.activePanel?.id).toBe("pane_session_3");
      expect(screen.queryByRole("button", { name: "Return to previous view" })).toBeNull();
      expect(await screen.findByRole("heading", { name: "local:different-root" })).toBeTruthy();
      expect(window.location.pathname).toBe("/s/local%3Adifferent-root");
    } else {
      expect(workspaceStore.getState().panes, `Startup witnesses: ${JSON.stringify(witnesses)}`).toEqual(
        ORDERING_SAVED_PANES,
      );
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

test("mounts and renders the welcome pane", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await waitFor(() => expect(emptyStateReasons(document.body)).toContain("no-session"));
});

test("wires the injected client into connectionStore (connects on mount)", async () => {
  const fake = new FakeClient("ready");
  const connect = vi.spyOn(fake, "connect");
  try {
    render(<AppShell client={fake} />);
    expect(connectionStore.getState().client).toBe(fake);
    expect(connectionStore.getState().state).toBe("ready");
    expect(connect).toHaveBeenCalled();
    const connectionResult = connect.mock.results[0];
    if (connectionResult?.type !== "return") throw new Error("AppShell did not start the handshake");
    await act(async () => {
      await connectionResult.value;
    });
  } finally {
    connect.mockRestore();
  }
});

test("rerender keeps the injected client's navigation wiring and unmount leaves the client open", async () => {
  const client = navClient();
  const view = render(<AppShell client={client} />);
  await waitFor(() => expect(navigationStore.getState().mode).toBe("v3"));
  const manifest = navigationStore.getState().manifest;
  expect(manifest?.error).toBeNull();
  const manifestReads = () =>
    client.calls.filter(
      (call) =>
        call.method === "evener/navigation/read" && (call.params as NavigationReadParams).resource === "manifest",
    );
  expect(manifestReads()).toHaveLength(1);

  view.rerender(<AppShell client={client} />);
  expect(connectionStore.getState().client).toBe(client);
  expect(navigationStore.getState().manifest).toBe(manifest);
  expect(manifestReads()).toHaveLength(1);

  view.unmount();
  expect(navigationStore.getState().mode).toBe("unknown");
  expect(client.state).toBe("ready");
});

test("shows no banner while the injected client is ready", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(screen.queryByText(/reconnecting/i)).toBeNull();
  expect(screen.queryByText(/connection closed/i)).toBeNull();
});

test("a banner retry swaps the client useClient consumers call", async () => {
  const user = userEvent.setup();
  const stale = new FakeClient("closed");
  const fresh = new FakeClient("ready");
  fresh.on("evener/search", () => ({ live: [], past: [] }));
  render(<AppShell client={stale} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
  await screen.findByText("No session open");

  // The banner is visible for the closed client; retry wires and adopts fresh.
  await user.click(await screen.findByRole("button", { name: "Retry" }));
  await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));

  // A useClient consumer (the palette search) must reach the fresh client,
  // not the closed original.
  await user.keyboard("{Meta>}k{/Meta}");
  await user.type(await screen.findByRole("combobox"), "hello");
  await waitFor(() => expect(fresh.calls.some((call) => call.method === "evener/search")).toBe(true));
  expect(stale.calls.some((call) => call.method === "evener/search")).toBe(false);
});

// --- background Live transcript ownership (Task 7) ---------------------

// The AppShell-owned background roster owner follows the complete Live roster
// through the real navigation and thread stores. These tests observe its
// actual wire demand: one additive thread membership read (subscribe:true) per
// discovered Live ref, released on the owner's lifetime end.
function currentThreadRead(params: { ref?: string; requestGeneration?: number }) {
  return { ...currentRead(params.ref ?? "local:s1"), requestGeneration: params.requestGeneration };
}
function membershipReads(client: FakeClient): number {
  return client.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}
function unsubscribedRefs(client: FakeClient): string[] {
  return client.calls
    .filter((call) => call.method === "thread/unsubscribe")
    .map((call) => (call.params as { ref?: string }).ref ?? "");
}
function backgroundReadyClient(): FakeClient {
  const client = navClient();
  client.on("thread/read", currentThreadRead);
  client.on("thread/unsubscribe", () => ({}));
  return client;
}

test("two mounted panes and an activity holder survive background ownership release", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  const activity = acquireThreadSubscription(client, "local:s1");
  const ensure = vi.spyOn(threadsStore.getState(), "ensureThread");
  let view: ReturnType<typeof render> | undefined;
  try {
    window.history.pushState({}, "", "/s/local:s1");
    installLocationForRoute("local:s1");
    view = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThanOrEqual(1));
    act(() => {
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:s1", parentRef: "local:owner" }, { slot: "secondary" });
    });
    await waitFor(() =>
      expect(ensure.mock.calls.filter(([ref]) => ref === "local:s1").length).toBeGreaterThanOrEqual(2),
    );
    await activity.read({ includeTurns: false, itemsView: "full", itemLimit: 80 });
    await owners.records[0]!.owner.settled();
    const boundary = client.calls.length;
    await act(async () => {
      await owners.records[0]!.owner.dispose();
    });
    expect(unsubscribedRefs(client)).not.toContain("local:s1");
    expect(
      workspaceStore.getState().panes.filter((pane) => (pane.params as { ref?: string }).ref === "local:s1"),
    ).toHaveLength(2);
    expect(
      client.calls
        .slice(boundary)
        .filter((call) => call.method.startsWith("thread/"))
        .every((call) => call.method === "thread/read" || call.method === "thread/unsubscribe"),
    ).toBe(true);
    view.unmount();
    view = undefined;
    await owners.joinDisposals();
    expect(unsubscribedRefs(client)).not.toContain("local:s1");
    activity.release();
    await waitFor(() => expect(unsubscribedRefs(client)).toContain("local:s1"));
  } finally {
    view?.unmount();
    activity.release();
    await owners.joinDisposals();
    ensure.mockRestore();
    owners.restore();
  }
});

// A coherent completed-entry read carrying `literal`; final release re-reads
// and checkpoints it. Distinct successor bytes carry a newer item version.
function currentVersionedThreadRead(literal: string, version: number) {
  return (params: { ref?: string; requestGeneration?: number }) => {
    const base = currentRead(params.ref ?? "local:s1");
    base.snapshot = { incarnation: "inc-1", length: 1 };
    base.thread.status = { type: "idle" };
    delete base.overlay;
    delete base.olderCursor;
    base.thread.turns = [
      {
        id: "turn_1",
        itemsView: "full",
        status: "completed",
        version,
        items: [
          {
            type: "agentMessage",
            id: "turn_1-item-0",
            transcriptKey: "apptranscript-item-v2:turn_1:0:0",
            turnId: "turn_1",
            position: { entry: 0, item: 0 },
            version,
            text: literal,
          },
        ],
      },
    ];
    return { ...base, requestGeneration: params.requestGeneration };
  };
}
function versionedClient(literal: string, version = 1): FakeClient {
  const client = navClient();
  client.on("thread/read", currentVersionedThreadRead(literal, version));
  client.on("thread/unsubscribe", () => ({}));
  return client;
}
function threadsHistory(ref: string): string {
  return JSON.stringify(threadsStore.getState().threads.get(ref)?.history ?? {});
}

function deferredSignal(): { promise: Promise<void>; resolve: () => void } {
  let resolve: (() => void) | undefined;
  const promise = new Promise<void>((settle) => {
    resolve = settle;
  });
  return { promise, resolve: () => resolve?.() };
}

// Retain a primary failure while still reporting a cleanup failure beside it.
function combineErrors(primary: unknown, cleanup: unknown): unknown {
  if (primary === undefined) return cleanup;
  return new AggregateError([primary, cleanup], "test body and cleanup both failed");
}

// Call-through instrumentation of the real background thread lifetime:
// - Records every real acquisition and whether it happened at/after the
//   predecessor's real release entry (the ownership oracle).
// - For the predecessor's lease (the first acquired after arm()), spies the real
//   release with a SYNCHRONOUS pass-through: it marks release entry and returns
//   the real release promise without delaying entry, so the real synchronous
//   retirement runs and the later real cache flush is attributable to it.
function instrumentBackgroundLifetime() {
  const realAcquire = threadsModule.acquireBackgroundThread;
  const acquisitions: Array<{ ref: string; afterReleaseEntry: boolean }> = [];
  let armed = false;
  let predecessorReleaseSpied = false;
  let releaseEntered = false;
  const spy = vi.spyOn(threadsModule, "acquireBackgroundThread").mockImplementation((ref) => {
    const lease = realAcquire(ref);
    acquisitions.push({ ref, afterReleaseEntry: releaseEntered });
    if (armed && !predecessorReleaseSpied) {
      predecessorReleaseSpied = true;
      const realRelease = lease.release.bind(lease);
      vi.spyOn(lease, "release").mockImplementation(() => {
        releaseEntered = true;
        return realRelease();
      });
    }
    return lease;
  });
  return {
    arm() {
      armed = true;
    },
    acquisitions,
    releaseEntered: () => releaseEntered,
    acquisitionsAfterReleaseEntry: (ref: string) =>
      acquisitions.filter((entry) => entry.ref === ref && entry.afterReleaseEntry).length,
    restore() {
      spy.mockRestore();
    },
  };
}

// A one-shot hold over the predecessor owner's real final-release cache write.
// `disarm()` stops any FUTURE hold from being armed; cleanup must call it
// before releasing the current hold and before unmount/dispose can start a
// fresh flush. A hold armed after cleanup began (i.e. after `beginCleanup()`)
// is recorded and signalled, so a failure proof can detect an orphaned hold and
// release it rather than hanging the suite.
function armReleaseWriteHold(cache: SessionEntryCache, lifetime: ReturnType<typeof instrumentBackgroundLifetime>) {
  let held: ReturnType<typeof holdNextWriteTransaction> | undefined;
  let consumed = false;
  let disarmed = false;
  let cleanupBegan = false;
  let signalArmedDuringCleanup: (() => void) | undefined;
  const armedDuringCleanup = new Promise<void>((resolve) => {
    signalArmedDuringCleanup = resolve;
  });
  const writeEntered = deferredSignal();
  const realCommit = cache.commitBatch.bind(cache);
  const spy = vi.spyOn(cache, "commitBatch").mockImplementation(async (batch) => {
    // One-shot: hold only the predecessor's real release flush, never a later
    // successor teardown write. After disarm no new hold may be armed, so
    // teardown cannot leave an unreleased gate behind.
    if (!disarmed && !consumed && lifetime.releaseEntered()) {
      consumed = true;
      held = holdNextWriteTransaction(["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"]);
      writeEntered.resolve();
      if (cleanupBegan) signalArmedDuringCleanup?.();
    }
    return realCommit(batch);
  });
  return {
    writeEntered,
    armedDuringCleanup,
    consumed: () => consumed,
    held: () => held,
    clear() {
      held = undefined;
    },
    beginCleanup() {
      cleanupBegan = true;
    },
    disarm() {
      disarmed = true;
    },
    release() {
      held?.release();
      held = undefined;
    },
    restore() {
      spy.mockRestore();
    },
  };
}

// The shared held-release cleanup both the success scenario and the failure
// proof exercise: disarm future holds FIRST, release the current hold, unmount
// and join the real owner disposal, then restore observers and dependencies.
async function runHeldCleanup(
  hold: ReturnType<typeof armReleaseWriteHold>,
  steps: { unmount?: () => void; join: () => Promise<void>; restore: () => void },
): Promise<void> {
  hold.beginCleanup();
  hold.disarm();
  hold.release();
  try {
    steps.unmount?.();
    await steps.join();
  } finally {
    steps.restore();
  }
}

// Run the shared cleanup, converting the defect (a hold armed during teardown
// that cleanup never releases) into a clear failure: detect the orphan, release
// it so cleanup can finish, then throw, rather than hanging the suite.
async function runHeldCleanupSafely(
  hold: ReturnType<typeof armReleaseWriteHold>,
  steps: { unmount?: () => void; join: () => Promise<void>; restore: () => void },
): Promise<void> {
  const cleanup = runHeldCleanup(hold, steps);
  const outcome = await Promise.race([
    cleanup.then(() => "cleanup" as const),
    hold.armedDuringCleanup.then(() => "armed" as const),
  ]);
  if (outcome === "armed") {
    hold.release();
    await cleanup;
    throw new Error("held cleanup armed an unreleased write hold during teardown");
  }
  await cleanup;
}

// Shared bounded fixture for the AppShell held-release failure proofs: real
// entry storage, a predecessor's accepted baseline, real owner tracking and
// the real final-read checkpoint hold. Reset storage only after the following
// healthy-operation proof, so that proof cannot hide a leaked owner by reset.
async function heldReleaseFailureFixture() {
  resetThreadsStoreForTests();
  onTestFinished(() => resetThreadsStoreForTests());
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-bg-fail-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch();
  const predecessor = versionedClient("predecessor-content");
  const owners = trackBackgroundOwners();
  const lifetime = instrumentBackgroundLifetime();
  const hold = armReleaseWriteHold(cache, lifetime);
  return { cache, predecessor, owners, lifetime, hold };
}

// The shared teardown for the AppShell failure proofs: release and disarm
// through runHeldCleanupSafely, then join real disposals and restore observers.
// `onRestore` observes that boundary before intercepts are torn down. Storage
// stays usable for the following healthy operation and closes at test finish.
function heldReleaseFailureCleanup(
  fixture: Awaited<ReturnType<typeof heldReleaseFailureFixture>>,
  getView: () => { unmount: () => void } | undefined,
  onRestore?: () => void,
) {
  return {
    unmount: () => getView()?.unmount(),
    join: () => fixture.owners.joinDisposals(),
    restore: () => {
      onRestore?.();
      stoppedTreeSession = false;
      fixture.hold.restore();
      fixture.lifetime.restore();
      fixture.owners.restore();
    },
  };
}

// A following healthy AppShell/background-owner operation on the same stores
// remains usable after a failure proof's cleanup.
async function expectHealthyBackgroundUsable() {
  const healthyOwners = trackBackgroundOwners();
  const healthyClient = backgroundReadyClient();
  let healthyView: ReturnType<typeof render> | undefined;
  try {
    healthyView = render(<AppShell client={healthyClient} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(healthyClient)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsModule.productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    healthyView.unmount();
    healthyView = undefined;
    await healthyOwners.joinDisposals();
    for (const record of healthyOwners.records) expect(record.disposeStatus).toBe("fulfilled");
  } finally {
    healthyView?.unmount();
    await healthyOwners.joinDisposals();
    healthyOwners.restore();
  }
}

// Captured at module load, before any spy can wrap the export, so the
// call-through below always reaches the real implementation.
const realStartBackgroundTranscripts = backgroundTranscriptsModule.startBackgroundTranscripts;

// Call-through interception of the real owner factory. Each record marks the
// synchronous retirement boundary (dispose() called) separately from the real
// disposal outcome (pending/fulfilled/rejected); the real promise and any
// rejection are retained rather than collapsed into a boolean, so a retired
// predecessor whose cleanup still runs is not confused with an active successor.
function trackBackgroundOwners() {
  const start = realStartBackgroundTranscripts;
  const records: Array<{
    owner: ReturnType<typeof realStartBackgroundTranscripts>;
    disposed: boolean;
    disposeCalls: number;
    disposeStatus: "pending" | "fulfilled" | "rejected";
    disposeError?: unknown;
    settledStatus: "pending" | "fulfilled" | "rejected";
    settledError?: unknown;
    disposePromise?: Promise<void>;
  }> = [];
  const spy = vi.spyOn(backgroundTranscriptsModule, "startBackgroundTranscripts").mockImplementation((navigation) => {
    const owner = start(navigation);
    const record: (typeof records)[number] = {
      owner,
      disposed: false,
      disposeCalls: 0,
      disposeStatus: "pending",
      settledStatus: "pending",
    };
    records.push(record);
    return {
      dispose() {
        record.disposeCalls += 1;
        record.disposed = true;
        const promise = owner.dispose();
        record.disposePromise = promise;
        promise.then(
          () => {
            record.disposeStatus = "fulfilled";
          },
          (error: unknown) => {
            record.disposeStatus = "rejected";
            record.disposeError = error;
          },
        );
        return promise;
      },
      settled() {
        const promise = owner.settled();
        promise.then(
          () => {
            record.settledStatus = "fulfilled";
          },
          (error: unknown) => {
            record.settledStatus = "rejected";
            record.settledError = error;
          },
        );
        return promise;
      },
    };
  });
  async function joinDisposals(): Promise<void> {
    await Promise.all(
      records.filter((record) => record.disposed && record.disposePromise).map((record) => record.disposePromise),
    );
  }
  return {
    records,
    activeCount: () => records.filter((record) => !record.disposed).length,
    retired: () => records.filter((record) => record.disposed),
    active: () => records.filter((record) => !record.disposed),
    joinDisposals,
    restore: () => spy.mockRestore(),
  };
}

test("starts one background transcript owner for the injected client and retires it on unmount", async () => {
  const client = backgroundReadyClient();
  const { rerender, unmount } = render(<AppShell client={client} />);
  await screen.findByText("No session open");
  // The Live session (local:s1) is discovered and read before any pane opens.
  await waitFor(() => expect(membershipReads(client)).toBe(1));

  // A rerender with the same client must not start a second owner.
  rerender(<AppShell client={client} />);
  await act(async () => {});
  expect(membershipReads(client)).toBe(1);

  unmount();
  await waitFor(() => expect(unsubscribedRefs(client)).toEqual(["local:s1"]));
});

test("exactly one active logical background owner across rerenders and unmount", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    view = render(<AppShell client={client} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(client)).toBe(1));
    // One logical owner, not merely one deduplicated wire demand.
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);

    // A rerender with the same client must not start a second owner.
    view.rerender(<AppShell client={client} />);
    await act(async () => {});
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);

    // Unmount retires the only owner, and its real disposal fulfills.
    view.unmount();
    view = undefined;
    await waitFor(() => expect(owners.activeCount()).toBe(0));
    expect(owners.records[0]?.disposeCalls).toBe(1);
    await owners.joinDisposals();
    expect(owners.records[0]?.disposeStatus).toBe("fulfilled");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      view?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("a replacement client gets its own background owner and the old demand is retired", async () => {
  const user = userEvent.setup();
  const stale = new FakeClient("closed");
  const fresh = backgroundReadyClient();
  render(<AppShell client={stale} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
  await screen.findByText("No session open");
  // The closed original never became ready, so it owns nothing.
  expect(membershipReads(stale)).toBe(0);

  await user.click(await screen.findByRole("button", { name: "Retry" }));
  await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));
  await waitFor(() => expect(membershipReads(fresh)).toBeGreaterThan(0));
  expect(unsubscribedRefs(stale)).toEqual([]);
});

test.each(["desktop", "narrow"])("one background owner per client lifetime in the %s layout", async (layout) => {
  if (layout === "narrow") {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
  }
  const owners = trackBackgroundOwners();
  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    const client = backgroundReadyClient();
    view = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBe(1));
    // One logical owner, not merely one deduplicated wire demand.
    expect(owners.records.length).toBe(1);
    expect(owners.activeCount()).toBe(1);
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      view?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("a client replacement holds the predecessor's real release cleanup and keeps a useful successor", async () => {
  const user = userEvent.setup();
  resetThreadsStoreForTests();
  onTestFinished(() => resetThreadsStoreForTests());
  // A real open cache adapter: the predecessor owner's real final release
  // drains a real cache write we hold inside the release.
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-bg-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch(); // open the real entry storage before arming
  const predecessor = versionedClient("predecessor-content");
  const fresh = versionedClient("successor-content", 2);
  const owners = trackBackgroundOwners();
  const lifetime = instrumentBackgroundLifetime();

  // Arm the completion hold at the real cache-flush entry and only after the
  // real release has entered, so the held write is attributable to the
  // predecessor's final-release flush (not merely the next cache write).
  const hold = armReleaseWriteHold(cache, lifetime);

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} bannerDelayMs={0} bannerCreateClient={() => fresh} />);
    await screen.findByText("No session open");
    // The predecessor owner is active and its resident model has versioned,
    // valid content, so its release drains a real cache write.
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(threadsModule.productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    const predecessorRecord = owners.records[0];
    if (!predecessorRecord) throw new Error("no predecessor owner was tracked");

    // Enter the predecessor owner's real final release while the client is
    // still ready and the model valid: the real release runs its synchronous
    // retirement and its real cache flush, held at the completion boundary.
    let predecessorDisposal!: Promise<void>;
    await act(async () => {
      predecessorDisposal = predecessorRecord.owner.dispose();
    });
    await waitFor(() => expect(hold.held()).toBeDefined());
    const write = hold.held();
    if (!write) throw new Error("predecessor release never entered a real cache flush");
    expect(lifetime.releaseEntered()).toBe(true);
    await write.reached; // transaction committed, its completion delivery held
    expect(predecessorRecord.disposed).toBe(false);

    // Replace the client through the real banner-retry path while the old
    // release cleanup is still held; AppShell's cleanup disposes the same owner
    // again (idempotent) and starts the successor owner.
    await act(async () => {
      predecessor.emitStateChange("closed");
    });
    await user.click(await screen.findByRole("button", { name: "Retry" }));
    await waitFor(() => expect(connectionStore.getState().client).toBe(fresh));

    // A useful successor: a real acquisition of the target ref while held.
    await waitFor(() => expect(lifetime.acquisitionsAfterReleaseEntry("local:s1")).toBeGreaterThanOrEqual(1));
    expect(owners.activeCount()).toBe(1);

    // Distinct successor content from the successor's own real client read: a
    // ready transition refreshes the now-owned ref from the fresh snapshot.
    await act(async () => {
      fresh.emitStateChange("reconnecting");
      fresh.emitReady({
        serverInfo: { name: "fake", version: "1" },
        protocolVersion: "evener-appwire-v6",
        sourceId: "fake",
        features: {} as never,
        navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
      });
    });
    // Queued successor bytes may already exist from its initial read. Capture
    // only after this fresh-ready resync has actually published its own cut.
    const lastReadParams = () =>
      fresh.calls.filter((call) => call.method === "thread/read").at(-1)?.params as
        | { requestGeneration?: number }
        | undefined;
    await waitFor(() => expect(typeof lastReadParams()?.requestGeneration).toBe("number"));
    const refreshGeneration = lastReadParams()?.requestGeneration;
    await waitFor(() =>
      expect(threadsStore.getState().threads.get("local:s1")?.history).toMatchObject({
        issuedGeneration: refreshGeneration,
        appliedGeneration: refreshGeneration,
      }),
    );
    const historyKey = threadsModule.productionEntryTranscriptForTests("local:s1").historyKey;
    if (!historyKey) throw new Error("successor has no resolved entry history identity");
    await waitFor(() =>
      expect(
        JSON.stringify(
          threadsModule.productionEntryTranscriptForTests("local:s1").repository?.retainedInventoryForTesting(),
        ),
      ).toContain("successor-content"),
    );
    // R36: the accepted ordinary background-only contract excludes bodies from
    // the store model (threads.entryProduction.test.ts:653-656). The old
    // release is still held, so accepted successor bytes belong to its real
    // repository's bounded write inventory until that checkpoint can complete.
    expect(threadsHistory("local:s1")).not.toContain("successor-content");
    const contentWhileHeld = threadsHistory("local:s1");
    const freshReadsWhileHeld = membershipReads(fresh);
    const freshUnsubsWhileHeld = unsubscribedRefs(fresh);

    // Release the real write and await the predecessor's real release
    // completion explicitly (its settled() joins the in-flight release work).
    expect(lifetime.acquisitionsAfterReleaseEntry("local:s1")).toBeGreaterThanOrEqual(1);
    await act(async () => {
      write.release();
      hold.clear();
      await predecessorDisposal;
    });

    // The old cleanup neither retired the successor nor removed its content or
    // demand.
    expect(threadsHistory("local:s1")).toBe(contentWhileHeld);
    expect(threadsHistory("local:s1")).not.toContain("successor-content");
    await threadsModule.settleEntryTranscriptForTests("local:s1");
    expect(JSON.stringify(await cache.getLatest(historyKey, 40))).toContain("successor-content");
    expect(membershipReads(fresh)).toBe(freshReadsWhileHeld);
    expect(unsubscribedRefs(fresh)).toEqual(freshUnsubsWhileHeld);
    expect(owners.activeCount()).toBe(1);
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(hold, {
        unmount: () => view?.unmount(),
        join: () => owners.joinDisposals(),
        restore: () => {
          stoppedTreeSession = false;
          hold.restore();
          lifetime.restore();
          owners.restore();
          cache.close();
        },
      });
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("an early failure before the predecessor release write joins real cleanup without an orphan hold", async () => {
  const fixture = await heldReleaseFailureFixture();
  const { predecessor, owners, lifetime, hold } = fixture;

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(threadsModule.productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    // A valid resident model exists and no release write has been held yet, so
    // the injected failure lands BEFORE the first hold is consumed.
    expect(hold.consumed()).toBe(false);
    throw new Error("injected early failure before the first release write");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(
        hold,
        heldReleaseFailureCleanup(fixture, () => view),
      );
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }

  // The injected failure survives cleanup unchanged, and cleanup created no
  // unreleased hold (otherwise runHeldCleanupSafely would have thrown above).
  expect(primaryError).toBeInstanceOf(Error);
  expect((primaryError as Error).message).toContain("injected early failure before the first release write");
  expect(hold.consumed()).toBe(false);
  // The real owner disposal was joined, not abandoned.
  expect(owners.activeCount()).toBe(0);
  for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");

  // A following healthy operation on the same stores remains usable.
  await expectHealthyBackgroundUsable();
});

test("a failure after the predecessor release entry joins the held predecessor before restoration", async () => {
  const fixture = await heldReleaseFailureFixture();
  const { predecessor, owners, lifetime, hold } = fixture;

  let view: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  let disposeFulfilledAtRestore = false;
  try {
    lifetime.arm();
    view = render(<AppShell client={predecessor} />);
    await screen.findByText("No session open");
    await waitFor(() => expect(membershipReads(predecessor)).toBeGreaterThanOrEqual(1));
    await waitFor(() => expect(threadsStore.getState().threads.get("local:s1")?.history?.incarnation).toBeDefined());
    await waitFor(() => expect(threadsModule.productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    expect(owners.activeCount()).toBe(1);
    const predecessorRecord = owners.records[0];
    if (!predecessorRecord) throw new Error("no predecessor owner was tracked");

    // Enter the real predecessor final release while the client is still ready
    // and the model valid: its real cache flush is held at the completion
    // boundary (the real IDB transaction committed, delivery held).
    await act(async () => {
      predecessorRecord.owner.dispose();
    });
    await waitFor(() => expect(hold.held()).toBeDefined());
    const write = hold.held();
    if (!write) throw new Error("predecessor release never entered a real cache flush");
    expect(lifetime.releaseEntered()).toBe(true);
    await write.reached;
    expect(hold.consumed()).toBe(true);
    // Inject the failure AFTER the real hold entry, leaving the held write
    // unreleased so cleanup -- not the body -- must release and join it.
    throw new Error("injected failure after the held release entry");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      await runHeldCleanupSafely(
        hold,
        heldReleaseFailureCleanup(
          fixture,
          () => view,
          () => {
            disposeFulfilledAtRestore = owners.records[0]?.disposeStatus === "fulfilled";
          },
        ),
      );
    } catch (error) {
      cleanupError = error;
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }

  // The injected failure survives cleanup, and the held predecessor was
  // released and joined before observers and the cache adapter were restored.
  expect(primaryError).toBeInstanceOf(Error);
  expect((primaryError as Error).message).toContain("injected failure after the held release entry");
  expect(hold.consumed()).toBe(true);
  expect(disposeFulfilledAtRestore).toBe(true);
  expect(owners.records[0]?.disposed).toBe(true);
  expect(owners.records[0]?.disposeStatus).toBe("fulfilled");

  await expectHealthyBackgroundUsable();
});

test("StrictMode mount and cleanup/remount retire their predecessor and complete disposal", async () => {
  const client = backgroundReadyClient();
  const owners = trackBackgroundOwners();
  let strictView: ReturnType<typeof render> | undefined;
  let againView: ReturnType<typeof render> | undefined;
  let primaryError: unknown;
  try {
    strictView = render(
      <StrictMode>
        <AppShell client={client} />
      </StrictMode>,
    );
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThan(0));
    // StrictMode's double-invoked mount retires a predecessor synchronously and
    // leaves exactly one active replacement; join the retired predecessor's real
    // disposal fulfilment before unmount.
    const retired = owners.retired();
    expect(retired.length).toBeGreaterThanOrEqual(1);
    expect(owners.activeCount()).toBe(1);
    await waitFor(() => expect(retired.every((record) => record.disposeStatus !== "pending")).toBe(true));
    for (const record of retired) expect(record.disposeStatus).toBe("fulfilled");

    strictView.unmount();
    strictView = undefined;
    // Every acquired membership is retired: a leaked owner would leave one open.
    await waitFor(() => expect(unsubscribedRefs(client).length).toBe(membershipReads(client)));
    await owners.joinDisposals();
    for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");

    // A clean remount of the same client: one owner at a time, none leaked.
    againView = render(<AppShell client={client} />);
    await waitFor(() => expect(membershipReads(client)).toBeGreaterThan(unsubscribedRefs(client).length));
    await waitFor(() => expect(owners.activeCount()).toBe(1));
    againView.unmount();
    againView = undefined;
    await waitFor(() => expect(unsubscribedRefs(client).length).toBe(membershipReads(client)));
    await owners.joinDisposals();
    for (const record of owners.records) expect(record.disposeStatus).toBe("fulfilled");
  } catch (error) {
    primaryError = error;
  } finally {
    let cleanupError: unknown;
    try {
      strictView?.unmount();
      againView?.unmount();
      await owners.joinDisposals();
    } catch (error) {
      cleanupError = error;
    } finally {
      owners.restore();
    }
    if (cleanupError !== undefined) primaryError = combineErrors(primaryError, cleanupError);
  }
  if (primaryError !== undefined) throw primaryError;
});

test("banner reflects reconnecting state when injected", async () => {
  const fake = new FakeClient("ready");
  // bannerDelayMs={0} drives the banner synchronously so the test doesn't
  // wait the production 10s reveal delay (see ConnectionBannerProps.delayMs).
  render(<AppShell client={fake} bannerDelayMs={0} />);
  await screen.findByText("No session open");

  act(() => {
    fake.emitStateChange("reconnecting");
  });

  expect(await screen.findByText(/reconnecting to the server/i)).toBeTruthy();
});

test('clicking "New session" navigates to /new and opens the spawn pane', async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  const button = await screen.findByRole("button", { name: "New session" });

  await user.click(button);

  expect(window.location.pathname).toBe("/new");
  // /new now opens the real spawn pane (the old "not available yet" welcome
  // fallback is gone) - its Start button proves the pane mounted.
  // The spawn pane's own submit verb is "Start" - it lives in the prompt
  // card's corner the way Send does in the composer, both surfaces being the
  // same shared card.
  expect(await screen.findByRole("button", { name: "Start" })).toBeTruthy();
});

// --- command palette wiring (this task) -------------------------------

test("Cmd+K opens the command palette from anywhere in the app", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Meta>}k{/Meta}");

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Ctrl+K opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Control>}k{/Control}");

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

// --- Mod+I focuses the focused session pane's composer (UX fix) ----------

test("Mod+I focuses the focused session pane's composer", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).toHaveBeenCalledWith("local:ref_abc123", workspaceStore.getState().focusedPaneId);
  focusSpy.mockRestore();
});

test("Mod+I is a no-op when the focused pane isn't a session", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
});

// SHOULD-FIX: Mod+I/Mod+J used to fire straight through an open modal - see
// AppShell.tsx's own "BLOCKER fix" comment above onKeyDown.

test("Mod+I is a no-op while the command palette is open", async () => {
  const user = userEvent.setup();
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  await user.keyboard("{Meta>}k{/Meta}");
  await screen.findByRole("dialog", { name: "Command palette" });

  await user.keyboard("{Meta>}i{/Meta}");

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
});

test("Mod+I is a no-op while a Dialog/Sheet ([aria-modal=true]) is open", async () => {
  const focusSpy = vi.spyOn(composerFocus, "requestComposerFocus");
  window.history.pushState({}, "", "/s/local:ref_abc123");
  installLocationForRoute("local:ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  const focusTarget = document.createElement("button");
  modal.appendChild(focusTarget);
  document.body.appendChild(modal);
  focusTarget.focus();

  fireEvent.keyDown(focusTarget, { key: "i", metaKey: true });

  expect(focusSpy).not.toHaveBeenCalled();
  focusSpy.mockRestore();
  modal.remove();
});

// --- hold hints: the composer chip agrees with composer.focus on a cascade --

test("the composer hold-hint targets a focused cascade's leaf composer, not the neighbouring parent conversation", () => {
  // A parent conversation pane sits beside a focused cascade. composer.focus
  // (AppShell's registerAction) reads the FOCUSED session through
  // focusedSessionRef/focusedSessionPaneId, which name the cascade's LEAF -
  // so the hold-hint chip must anchor to that leaf's composer. Anchoring only
  // ordinary session panes (the pre-fix resolver) missed the cascade and fell
  // back to the first composer in the document, the parent's.
  const parent = document.createElement("div");
  parent.setAttribute("data-composer", "local:parent");
  const child = document.createElement("div");
  child.setAttribute("data-composer", "local:child");
  document.body.append(parent, child);
  const cascadeParams: SessionZoomParams = {
    ref: "local:child",
    source: { type: "session", params: { ref: "local:parent" } },
    edges: [],
  };
  workspaceStore.setState({
    panes: [
      { id: "pane_parent", type: "session", params: { ref: "local:parent" }, slot: "main" },
      { id: "pane_cascade", type: "sessionZoom", params: cascadeParams, slot: "secondary" },
    ],
    focusedPaneId: "pane_cascade",
  });
  try {
    // The shortcut focuses the cascade's leaf ref ...
    expect(focusedSessionRef()).toBe("local:child");
    // ... and the hint resolves to that same leaf's composer, not the parent's.
    expect(resolveFocusedComposer()).toBe(child);
  } finally {
    parent.remove();
    child.remove();
  }
});

test("the composer hold-hint targets the focused pane's composer when a pane and a cascade show the same conversation", () => {
  // An ordinary pane and a cascade column can both show the SAME conversation.
  // composer.focus addresses the focused session's ref AND pane id
  // (focusedSessionPaneId), so with the cascade focused the chip must anchor to
  // the cascade's composer, not the ordinary pane's - even though both anchors
  // carry the same ref and the ordinary pane's anchor comes first in document
  // order.
  const ordinary = document.createElement("div");
  ordinary.setAttribute("data-composer", "local:same");
  ordinary.setAttribute("data-composer-pane", "pane_ordinary");
  const cascade = document.createElement("div");
  cascade.setAttribute("data-composer", "local:same");
  cascade.setAttribute("data-composer-pane", "pane_cascade");
  document.body.append(ordinary, cascade);
  const cascadeParams: SessionZoomParams = {
    ref: "local:same",
    source: { type: "session", params: { ref: "local:same" } },
    edges: [],
  };
  workspaceStore.setState({
    panes: [
      { id: "pane_ordinary", type: "session", params: { ref: "local:same" }, slot: "main" },
      { id: "pane_cascade", type: "sessionZoom", params: cascadeParams, slot: "secondary" },
    ],
    focusedPaneId: "pane_cascade",
  });
  try {
    // The shortcut addresses the cascade's ref AND pane ...
    expect(focusedSessionRef()).toBe("local:same");
    expect(focusedSessionPaneId()).toBe("pane_cascade");
    // ... and the hint resolves to the cascade's composer, not the ordinary
    // pane's (the pre-fix resolver matched the ref alone and took the first).
    expect(resolveFocusedComposer()).toBe(cascade);
  } finally {
    ordinary.remove();
    cascade.remove();
  }
});

test("the composer hold-hint still falls back to the first composer when no session is focused", () => {
  const first = document.createElement("div");
  first.setAttribute("data-composer", "local:first");
  const second = document.createElement("div");
  second.setAttribute("data-composer", "local:second");
  document.body.append(first, second);
  try {
    // No session is focused (the reset workspace focuses none): the chord
    // no-ops, and the pre-change single-composer behavior (the first anchor)
    // holds.
    expect(focusedSessionRef()).toBeNull();
    expect(resolveFocusedComposer()).toBe(first);
  } finally {
    first.remove();
    second.remove();
  }
});

// --- Mod+J cycles needs-you sessions (UX fix) -----------------------------
// (needs-you rows are installed directly via installNeedsYouRows, not via fetch)

test("Mod+J opens the first needs-you session when nothing is focused", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = navClient();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());

  await user.keyboard("{Meta>}j{/Meta}");

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
});

test("Mod+J still works while an editor has focus", async () => {
  installNeedsYouRows();
  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
  const editor = document.createElement("textarea");
  document.body.appendChild(editor);
  editor.focus();

  fireEvent.keyDown(editor, { key: "j", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
  editor.remove();
});

test("Mod+J cycles from the focused needs-you session to the next one, wrapping", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = navClient();
  window.history.pushState({}, "", "/s/local:ny2");
  installLocationForRoute("local:ny2");
  render(<AppShell client={client} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny2" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);

  await user.keyboard("{Meta>}j{/Meta}");

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
});

test("Mod+J history Back and Forward restore the exact routed readers", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  const origin = workspaceStore.getState().mainPane();
  if (!origin) throw new Error("missing routed origin reader");
  const originLifetime = conversationPaneLifetime(origin);
  const initialHistoryLength = window.history.length;

  await user.keyboard("{Meta>}j{/Meta}");
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
  const destination = workspaceStore.getState().mainPane();
  if (!destination) throw new Error("missing needs-you destination reader");
  const destinationLifetime = conversationPaneLifetime(destination);
  expect(window.history.length).toBe(initialHistoryLength + 1);

  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(conversationPaneLifetime(origin)).toBe(originLifetime);
  expect(originLifetime.alive).toBe(true);

  const forward = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.forward();
    await forward;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination.id));
  expect(workspaceStore.getState().panes).toContain(destination);
  expect(conversationPaneLifetime(destination)).toBe(destinationLifetime);
  expect(destinationLifetime.alive).toBe(true);
});

test("mobile initial mount restores a valid attention entry before StackHost publishes the stale focused URL", async () => {
  const setMobile = installSwitchableViewport();
  setMobile(true);
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  const firstMount = render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  const origin = workspaceStore.getState().mainPane();
  if (!origin) throw new Error("missing initial routed reader");
  const originLifetime = conversationPaneLifetime(origin);
  const originView = retainedTranscriptReadView(originLifetime, "local:origin", "session");
  if (!originView) throw new Error("missing initial retained read view");
  let staleReader = "";
  act(() => {
    staleReader = workspaceStore.getState().openPane("session", { ref: "local:stale" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(origin.id);
    openAttentionSession("local:destination");
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:destination" }));
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
  firstMount.unmount();

  act(() => workspaceStore.getState().focusPane(staleReader));
  const token = currentAttentionEntryToken();
  if (!token) throw new Error("origin history entry lost its valid runtime token before remount");
  const entryURL = window.location.pathname;
  const entryState = window.history.state;
  const historyLength = window.history.length;

  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
  expect(currentAttentionEntryToken()).toBe(token);
  expect(window.location.pathname).toBe(entryURL);
  expect(window.history.length).toBe(historyLength);
  expect(window.history.state).toEqual(entryState);
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(conversationPaneLifetime(origin)).toBe(originLifetime);
  expect(originLifetime.alive).toBe(true);
  expect(retainedTranscriptReadView(originLifetime, "local:origin", "session")).toBe(originView);

  act(() => workspaceStore.getState().focusPane(staleReader));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Astale"));
});

// The entry an attention source leaves need not be a session URL: a saved
// layout's conversation is a valid source while the address bar still says
// "/". The mobile URL sync has to wait for that restore on EVERY route type,
// not only on a session deep link, or StackHost publishes whichever pane
// happens to be focused at mount over the entry it is about to restore.
test("mobile mount at a welcome-route attention entry restores it before StackHost publishes the stale focused URL", async () => {
  const setMobile = installSwitchableViewport();
  setMobile(true);
  // A saved layout's reader lives at "/": the address bar names the workspace
  // default while a session conversation holds focus.
  window.history.replaceState({}, "", "/");
  let origin = "";
  act(() => {
    origin = workspaceStore.getState().openPane("session", { ref: "local:origin" });
    workspaceStore.getState().focusPane(origin);
  });
  const originPane = workspaceStore.getState().panes.find((pane) => pane.id === origin);
  if (!originPane) throw new Error("missing the saved-layout reader");
  const originLifetime = conversationPaneLifetime(originPane);
  act(() => openAttentionSession("local:destination"));
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  expect(window.location.pathname).toBe("/");

  // A different session reader is focused when the phone mounts here.
  let staleReader = "";
  act(() => {
    staleReader = workspaceStore.getState().openPane("session", { ref: "local:stale" }, { slot: "secondary" });
  });
  act(() => workspaceStore.getState().focusPane(staleReader));
  const token = currentAttentionEntryToken();
  if (!token) throw new Error("the welcome-route entry lost its valid runtime token before remount");
  const entryState = window.history.state;
  const historyLength = window.history.length;

  render(<AppShell client={navClient()} />);

  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin));
  expect(window.location.pathname).toBe("/");
  expect(currentAttentionEntryToken()).toBe(token);
  expect(window.history.length).toBe(historyLength);
  expect(window.history.state).toEqual(entryState);
  expect(workspaceStore.getState().panes).toContain(originPane);
  expect(conversationPaneLifetime(originPane)).toBe(originLifetime);
  expect(originLifetime.alive).toBe(true);
});

// routeDeferred is computed during render from a ref the restoration effect
// writes, so a restore that changes nothing observable leaves no render behind
// it. This is the case the review asked for: the phone remounts on an attention
// entry it already holds the exact reader for, and a later focus-only move to
// another existing pane still has to publish that pane's URL.
test("a focus-only move after an already-restored attention entry publishes the new pane's URL", async () => {
  const setMobile = installSwitchableViewport();
  setMobile(true);
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  const firstMount = render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  act(() => openAttentionSession("local:destination"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:destination" }));
  // Still on the attention entry, with an existing secondary built before the
  // remount so focusing it later leaves the pane array alone.
  expect(currentAttentionEntryToken()).not.toBeNull();
  const entryURL = window.location.pathname;
  const entryState = window.history.state;
  const destination = workspaceStore.getState().mainPane();
  if (!destination) throw new Error("missing attention target");
  let child = "";
  act(() => {
    child = workspaceStore.getState().openPane("session", { ref: "local:child" }, { slot: "secondary" });
  });
  act(() => workspaceStore.getState().focusPane(destination.id));
  // Opening the secondary focused it, which published its URL. Remount on the
  // attention entry itself, as the phone does when the tab is recreated.
  window.history.replaceState(entryState, "", entryURL);
  firstMount.unmount();

  // The live store already holds this entry's reader, focused and placed: the
  // restoration is a no-op, so nothing about it forces a render.
  expect(workspaceStore.getState().focusedPaneId).toBe(destination.id);
  render(<AppShell client={navClient()} />);
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination.id));
  expect(window.location.pathname).not.toBe("/s/local%3Achild");

  act(() => {
    workspaceStore.getState().focusPane(child);
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Achild"));
  await act(async () => {});
});

// Closing the attention target is the reader saying no to that conversation.
// The route still names it, so ordinary reconciliation used to reopen it - and
// with another session owning main, that replacement took the whole workspace,
// parked source readers included. The entry has to be retired when its reader
// is closed.
test("closing an attention target retires its route instead of reopening it", async () => {
  window.history.replaceState({}, "", "/s/local%3Asource");
  installLocationForRoute("local:source");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:source" }));
  const source = workspaceStore.getState().mainPane();
  if (!source) throw new Error("missing source reader");
  const sourceLifetime = conversationPaneLifetime(source);

  act(() => openAttentionSession("local:target"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:target" }));
  expect(source.slot).toBe("parked");

  const target = workspaceStore.getState().mainPane();
  if (!target) throw new Error("missing attention target");
  act(() => workspaceStore.getState().closePane(target.id));

  await waitFor(() => expect(workspaceStore.getState().panes.some((pane) => pane.id === target.id)).toBe(false));
  // Nothing may reopen the closed conversation, and the parked source keeps its
  // exact lifetime.
  expect(workspaceStore.getState().panes.some((pane) => (pane.params as { ref?: string }).ref === "local:target")).toBe(
    false,
  );
  expect(workspaceStore.getState().panes).toContain(source);
  expect(conversationPaneLifetime(source)).toBe(sourceLifetime);
  expect(sourceLifetime.alive).toBe(true);
  expect(window.location.pathname).not.toBe("/s/local%3Atarget");
});

test("a secondary departure restores the exact reader without replacing its main or neighbors", async () => {
  window.history.replaceState({}, "", "/s/local%3Aowner");
  installLocationForRoute("local:owner");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:owner" }));
  const main = workspaceStore.getState().mainPane();
  if (!main) throw new Error("missing owner reader");
  let secondaryId = "";
  act(() => {
    secondaryId = workspaceStore.getState().openPane("session", { ref: "local:secondary" }, { slot: "secondary" });
  });
  await waitFor(() =>
    expect(workspaceStore.getState().panes.find((pane) => pane.id === secondaryId)?.type).toBe("session"),
  );
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary) throw new Error("missing secondary reader");
  const lifetime = conversationPaneLifetime(secondary);
  const view = retainedTranscriptReadView(lifetime, "local:secondary", "session");
  if (!view) throw new Error("missing secondary read view");
  act(() => {
    workspaceStore.getState().focusPane(secondaryId);
    openAttentionSession("local:destination");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adestination"));
  const destination = workspaceStore.getState().focusedPaneId;
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(secondaryId));
  expect(workspaceStore.getState().mainPane()).toBe(main);
  expect(workspaceStore.getState().panes).toContain(secondary);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(destination);
  expect(conversationPaneLifetime(secondary)).toBe(lifetime);
  expect(retainedTranscriptReadView(lifetime, "local:secondary", "session")).toBe(view);
});

test("same-URL attention entries restore distinct cascade and session readers on Back and Forward", async () => {
  window.history.replaceState({}, "", "/s/local%3Asame");
  installLocationForRoute("local:same");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:same" }));
  const session = workspaceStore.getState().mainPane();
  if (!session) throw new Error("missing same-URL session reader");
  let sourceId = "";
  let cascadeId = "";
  act(() => {
    sourceId = workspaceStore.getState().openPane("session", { ref: "local:source" }, { slot: "secondary" });
  });
  await waitFor(() =>
    expect(workspaceStore.getState().panes.find((pane) => pane.id === sourceId)?.type).toBe("session"),
  );
  act(() => {
    cascadeId = enterAgentCascade(
      activityDelegate({ ownerRef: "local:source", childRef: "local:same", delegateId: "same-url-edge" }),
      sourceId,
    );
    workspaceStore.getState().focusPane(cascadeId);
  });
  await waitFor(() => {
    expect(workspaceStore.getState().panes.find((pane) => pane.id === cascadeId)?.type).toBe("sessionZoom");
    expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  });
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade) throw new Error("missing same-URL cascade reader");
  expect(cascade.params).toMatchObject({
    ref: "local:same",
    source: { type: "session", params: { ref: "local:source" } },
    edges: [{ ownerRef: "local:source", childRef: "local:same", delegateId: "same-url-edge" }],
  });
  expect(workspaceStore.getState().panes.find((pane) => pane.id === sourceId)).toMatchObject({
    type: "session",
    params: { ref: "local:source" },
  });
  expect(focusedSessionRef()).toBe("local:same");
  const cascadeLifetime = conversationPaneLifetime(cascade);
  const cascadeView = retainedTranscriptReadView(cascadeLifetime, "local:same", "cascade");
  const sessionLifetime = conversationPaneLifetime(session);
  const sessionView = retainedTranscriptReadView(sessionLifetime, "local:same", "session");
  if (!cascadeView || !sessionView) throw new Error("missing same-URL reader views");
  const initialHistoryLength = window.history.length;
  act(() => openAttentionSession("local:same"));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(session.id));
  expect(window.location.pathname).toBe("/s/local%3Asame");
  expect(window.history.length).toBe(initialHistoryLength + 1);

  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId));
  expect(window.location.pathname).toBe("/s/local%3Asame");
  expect(workspaceStore.getState().panes).toContain(cascade);
  expect(conversationPaneLifetime(cascade)).toBe(cascadeLifetime);
  expect(retainedTranscriptReadView(cascadeLifetime, "local:same", "cascade")).toBe(cascadeView);

  const forward = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.forward();
    await forward;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(session.id));
  expect(window.location.pathname).toBe("/s/local%3Asame");
  expect(workspaceStore.getState().panes).toContain(session);
  expect(conversationPaneLifetime(session)).toBe(sessionLifetime);
  expect(retainedTranscriptReadView(sessionLifetime, "local:same", "session")).toBe(sessionView);
});

test.each(["session", "cascade"] as const)(
  "mobile browser and StackHost Back preserve the exact %s reader without phantom steps",
  async (readerKind) => {
    const setMobile = installSwitchableViewport();
    setMobile(true);
    window.history.replaceState({}, "", "/s/local%3Aorigin");
    installLocationForRoute("local:origin");
    render(<AppShell client={navClient()} />);
    await screen.findByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
    const originOwner = workspaceStore.getState().mainPane();
    if (!originOwner) throw new Error("missing origin owner session");
    let originId = workspaceStore.getState().mainPane()?.id;
    if (readerKind === "cascade") {
      act(() => {
        originId = enterAgentCascade(
          activityDelegate({ ownerRef: "local:origin", childRef: "local:leaf", delegateId: "origin-leaf" }),
          originId,
        );
        workspaceStore.getState().focusPane(originId as string);
      });
      await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(originId));
    }
    const origin = workspaceStore.getState().panes.find((pane) => pane.id === originId);
    if (!origin) throw new Error(`missing ${readerKind} origin reader`);
    if (readerKind === "cascade") {
      expect(origin).toMatchObject({
        type: "sessionZoom",
        params: {
          ref: "local:leaf",
          source: { type: "session", params: { ref: "local:origin" } },
          edges: [{ ownerRef: "local:origin", childRef: "local:leaf", delegateId: "origin-leaf" }],
        },
      });
      expect(focusedSessionRef()).toBe("local:leaf");
    }
    const originLifetime = conversationPaneLifetime(origin);
    const readerRef = readerKind === "cascade" ? "local:leaf" : "local:origin";
    const role = readerKind === "cascade" ? "cascade" : "session";
    const originView = retainedTranscriptReadView(originLifetime, readerRef, role);
    if (!originView) throw new Error(`missing ${readerKind} read view`);
    const initialHistoryLength = window.history.length;
    act(() => openAttentionSession("local:destination"));
    await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adestination"));
    const destination = workspaceStore
      .getState()
      .panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
    if (!destination) throw new Error("missing destination reader");
    const destinationLifetime = conversationPaneLifetime(destination);
    expect(window.history.length).toBe(initialHistoryLength + 1);

    const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
    await act(async () => {
      window.history.back();
      await back;
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
    expect(workspaceStore.getState().panes).toContain(origin);
    expect(conversationPaneLifetime(origin)).toBe(originLifetime);
    expect(retainedTranscriptReadView(originLifetime, readerRef, role)).toBe(originView);

    const forward = new Promise<void>((resolve) =>
      window.addEventListener("popstate", () => resolve(), { once: true }),
    );
    await act(async () => {
      window.history.forward();
      await forward;
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination.id));
    expect(conversationPaneLifetime(destination)).toBe(destinationLifetime);

    const lengthBeforeBack = window.history.length;
    await userEvent.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
    expect(window.history.length).toBe(lengthBeforeBack);
    expect(window.location.pathname).toBe("/s/local%3Aorigin");

    const toolbarForward = new Promise<void>((resolve) =>
      window.addEventListener("popstate", () => resolve(), { once: true }),
    );
    await act(async () => {
      window.history.forward();
      await toolbarForward;
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination.id));
    expect(window.history.length).toBe(lengthBeforeBack);

    const returnToOrigin = new Promise<void>((resolve) =>
      window.addEventListener("popstate", () => resolve(), { once: true }),
    );
    await act(async () => {
      window.history.back();
      await returnToOrigin;
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
    const lengthBeforeExhaustedBack = window.history.length;
    await userEvent.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => {
      const focused = workspaceStore
        .getState()
        .panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
      expect(focused?.type).toBe(readerKind === "cascade" ? "session" : "welcome");
      if (readerKind === "cascade") expect(focused).toBe(originOwner);
    });
    expect(workspaceStore.getState().focusedPaneId).not.toBe(destination.id);
    expect(window.location.pathname).toBe(readerKind === "cascade" ? "/s/local%3Aorigin" : "/");
    // Main's StackHost change stopped the exhausted Back from publishing a
    // phantom route: pressing Back with nothing left to restore now leaves the
    // history untouched for both reader kinds. The pane and pathname
    // assertions above still prove the Welcome/origin outcome.
    expect(window.history.length).toBe(lengthBeforeExhaustedBack);
  },
);

test("attention restoration survives desktop→mobile→desktop host remount with the same reader and view", async () => {
  const setMobile = installSwitchableViewport();
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  const origin = workspaceStore.getState().mainPane();
  if (!origin) throw new Error("missing desktop origin reader");
  const lifetime = conversationPaneLifetime(origin);
  const view = retainedTranscriptReadView(lifetime, "local:origin", "session");
  if (!view) throw new Error("missing desktop origin read view");
  act(() => openAttentionSession("local:destination"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adestination"));
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));

  act(() => setMobile(true));
  await screen.findByRole("button", { name: "Back" });
  expect(workspaceStore.getState().focusedPaneId).toBe(origin.id);
  act(() => setMobile(false));
  await waitFor(() => expect(getDockviewApi()).not.toBeNull());
  expect(workspaceStore.getState().focusedPaneId).toBe(origin.id);
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(conversationPaneLifetime(origin)).toBe(lifetime);
  expect(retainedTranscriptReadView(lifetime, "local:origin", "session")).toBe(view);
});

test("restored secondary survives successful, failed, and refreshed location reads without main reconstruction", async () => {
  window.history.replaceState({}, "", "/s/local%3Aowner");
  installLocationForRoute("local:owner");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:owner" }));
  const owner = workspaceStore.getState().mainPane();
  if (!owner) throw new Error("missing location owner");
  let secondaryId = "";
  act(() => {
    secondaryId = workspaceStore.getState().openPane("session", { ref: "local:secondary" }, { slot: "secondary" });
  });
  await waitFor(() =>
    expect(workspaceStore.getState().panes.find((pane) => pane.id === secondaryId)?.type).toBe("session"),
  );
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary) throw new Error("missing location secondary");
  const lifetime = conversationPaneLifetime(secondary);
  const view = retainedTranscriptReadView(lifetime, "local:secondary", "session");
  if (!view) throw new Error("missing location secondary view");
  act(() => {
    workspaceStore.getState().focusPane(secondaryId);
    openAttentionSession("local:destination");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adestination"));
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(secondaryId));
  const assertExact = () => {
    expect(workspaceStore.getState().mainPane()).toBe(owner);
    expect(workspaceStore.getState().focusedPaneId).toBe(secondaryId);
    expect(workspaceStore.getState().panes).toContain(secondary);
    expect(conversationPaneLifetime(secondary)).toBe(lifetime);
    expect(retainedTranscriptReadView(lifetime, "local:secondary", "session")).toBe(view);
  };

  act(() =>
    installLocation({
      generation_id: "generation_test",
      revision: 2,
      ref: "local:owner",
      top_level_ref: "local:other-owner",
      top_level: false,
      session: { ...TREE_SESSION, ref: "local:owner", kind: "subagent" },
    }),
  );
  await waitFor(() =>
    expect(
      navigationStore.getState().resources?.get(keyID({ kind: "location", ref: "local:owner" }))?.loadedRevision,
    ).toBe(2),
  );
  assertExact();

  act(() => installFailedLocation("local:owner"));
  await waitFor(() =>
    expect(
      navigationStore.getState().resources?.get(keyID({ kind: "location", ref: "local:owner" }))?.error,
    ).toBeInstanceOf(Error),
  );
  assertExact();

  const key = { kind: "location", ref: "local:owner" } as const;
  const resources = new Map(navigationStore.getState().resources);
  const prior = resources.get(keyID(key));
  if (!prior) throw new Error("missing refreshed location resource");
  resources.set(keyID(key), {
    ...prior,
    data: { ...(prior.data as NavigationSessionLocation), top_level_ref: "local:stale-owner" },
    stale: true,
    error: null,
  });
  act(() => navigationStore.setState({ resources }));
  await waitFor(() => expect(navigationStore.getState().resources?.get(keyID(key))?.stale).toBe(true));
  assertExact();
  act(() =>
    installLocation({
      generation_id: "generation_test",
      revision: 3,
      ref: "local:owner",
      top_level_ref: "local:owner",
      top_level: true,
      session: { ...TREE_SESSION, ref: "local:owner" },
    }),
  );
  await waitFor(() => expect(navigationStore.getState().resources?.get(keyID(key))?.loadedRevision).toBe(3));
  assertExact();
});

test("generic route placement does not focus a parked same-ref attention reader", async () => {
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  const parkedOrigin = workspaceStore.getState().mainPane();
  if (!parkedOrigin) throw new Error("missing routed origin");
  act(() => openAttentionSession("local:destination"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Adestination"));
  expect(parkedOrigin.slot).toBe("parked");
  act(() => navigate("/s/local%3Aorigin"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  const generic = workspaceStore.getState().mainPane();
  expect(generic).not.toBe(parkedOrigin);
  expect(generic?.id).not.toBe(parkedOrigin.id);
  expect(parkedOrigin.slot).toBe("parked");
});

test("focus-only attention departures restore a changed cascade leaf with exactly two pushes", async () => {
  const user = userEvent.setup();
  installNeedsYouRows(["local:origin", "local:leaf-one", "local:leaf-two", "local:ny2"]);
  window.history.replaceState({}, "", "/s/local%3Aorigin");
  installLocationForRoute("local:origin");
  render(<AppShell client={navClient()} />);
  await screen.findByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:origin" }));
  expect(selectNeedsYouRows(navigationStore.getState()).map((row) => row.ref)).toEqual([
    "local:origin",
    "local:leaf-one",
    "local:leaf-two",
    "local:ny2",
  ]);
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
  expect(focusedSessionRef()).toBe("local:origin");
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
  expect(currentAttentionEntryToken()).toBeNull();
  let sourceId = "";
  act(() => {
    sourceId = workspaceStore.getState().openPane("session", { ref: "local:focus" }, { slot: "secondary" });
  });
  await waitFor(() =>
    expect(workspaceStore.getState().panes.find((pane) => pane.id === sourceId)?.type).toBe("session"),
  );
  const historyLength = window.history.length;

  await user.keyboard("{Meta>}j{/Meta}");
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:leaf-one" }));
  expect(window.history.length).toBe(historyLength + 1);
  expect(window.location.pathname).toBe("/s/local%3Aleaf-one");
  const firstEntryToken = currentAttentionEntryToken();
  if (!firstEntryToken) throw new Error("first keyboard hop did not retain its attention token");
  let cascadeId = "";
  act(() => {
    cascadeId = enterAgentCascade(
      activityDelegate({ ownerRef: "local:focus", childRef: "local:leaf-one", delegateId: "leaf-one" }),
      sourceId,
    );
    workspaceStore.getState().focusPane(cascadeId);
  });
  await waitFor(() => {
    expect(workspaceStore.getState().panes.find((pane) => pane.id === cascadeId)?.type).toBe("sessionZoom");
    expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  });
  act(() => {
    const currentCascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
    if (!currentCascade) throw new Error("missing cascade reader before leaf change");
    if (
      !workspaceStore.getState().retypePane(currentCascade, "sessionZoom", {
        ref: "local:leaf-two",
        source: { type: "session", params: { ref: "local:focus" } },
        edges: [
          { ownerRef: "local:focus", childRef: "local:leaf-one", delegateId: "leaf-one" },
          { ownerRef: "local:leaf-one", childRef: "local:leaf-two", delegateId: "leaf-two" },
        ],
      })
    )
      throw new Error("cascade leaf did not change");
    workspaceStore.getState().focusPane(cascadeId);
  });
  const changedCascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!changedCascade) throw new Error("missing changed cascade reader");
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId);
  expect(focusedSessionRef()).toBe("local:leaf-two");
  expect(window.location.pathname).toBe("/s/local%3Aleaf-one");
  expect(currentAttentionEntryToken()).toBe(firstEntryToken);
  expect(selectNeedsYouRows(navigationStore.getState()).map((row) => row.ref)).toEqual([
    "local:origin",
    "local:leaf-one",
    "local:leaf-two",
    "local:ny2",
  ]);
  const changedLifetime = conversationPaneLifetime(changedCascade);
  const changedView = retainedTranscriptReadView(changedLifetime, "local:leaf-two", "cascade");
  if (!changedView) throw new Error("missing changed cascade view");

  const independentMain = workspaceStore.getState().mainPane();
  const independentSource = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  if (!independentMain || !independentSource) throw new Error("missing independent panes before second hop");
  const independentMainLifetime = conversationPaneLifetime(independentMain);
  const independentSourceLifetime = conversationPaneLifetime(independentSource);
  expect(independentMain).toMatchObject({ type: "session", params: { ref: "local:leaf-one" }, slot: "main" });
  expect(independentSource).toMatchObject({ type: "session", params: { ref: "local:focus" }, slot: "secondary" });
  expect(independentMainLifetime).toMatchObject({
    paneId: independentMain.id,
    sourceRef: "local:leaf-one",
    alive: true,
  });
  expect(independentSourceLifetime).toMatchObject({
    paneId: independentSource.id,
    sourceRef: "local:focus",
    alive: true,
  });

  await user.keyboard("{Meta>}j{/Meta}");
  await waitFor(() => expect(focusedSessionRef()).toBe("local:ny2"));
  expect(window.location.pathname).toBe("/s/local%3Any2");
  expect(window.history.length).toBe(historyLength + 2);
  expect((window.history.state as Record<string, unknown>).__evenerAttention).toBeDefined();
  expect(workspaceStore.getState().panes).toContain(independentMain);
  expect(workspaceStore.getState().panes).toContain(independentSource);
  expect(independentMain).toMatchObject({ type: "session", params: { ref: "local:leaf-one" }, slot: "main" });
  expect(independentSource).toMatchObject({ type: "session", params: { ref: "local:focus" }, slot: "secondary" });
  expect(conversationPaneLifetime(independentMain)).toBe(independentMainLifetime);
  expect(conversationPaneLifetime(independentSource)).toBe(independentSourceLifetime);
  expect(independentMainLifetime.alive).toBe(true);
  expect(independentSourceLifetime.alive).toBe(true);

  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(cascadeId));
  expect(workspaceStore.getState().panes.find((pane) => pane.id === cascadeId)).toEqual(changedCascade);
  expect((changedCascade.params as { ref: string }).ref).toBe("local:leaf-two");
  expect(conversationPaneLifetime(changedCascade)).toBe(changedLifetime);
  expect(retainedTranscriptReadView(changedLifetime, "local:leaf-two", "cascade")).toBe(changedView);
  expect(workspaceStore.getState().panes).toContain(independentMain);
  expect(workspaceStore.getState().panes).toContain(independentSource);
  expect(independentMain).toMatchObject({ type: "session", params: { ref: "local:leaf-one" }, slot: "main" });
  expect(independentSource).toMatchObject({ type: "session", params: { ref: "local:focus" }, slot: "secondary" });
  expect(conversationPaneLifetime(independentMain)).toBe(independentMainLifetime);
  expect(conversationPaneLifetime(independentSource)).toBe(independentSourceLifetime);
  expect(independentMainLifetime.alive).toBe(true);
  expect(independentSourceLifetime.alive).toBe(true);

  const forward = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.forward();
    await forward;
  });
  await waitFor(() => expect(focusedSessionRef()).toBe("local:ny2"));
  expect(workspaceStore.getState().panes).toContain(independentMain);
  expect(workspaceStore.getState().panes).toContain(independentSource);
  expect(independentMain).toMatchObject({ type: "session", params: { ref: "local:leaf-one" }, slot: "main" });
  expect(independentSource).toMatchObject({ type: "session", params: { ref: "local:focus" }, slot: "secondary" });
  expect(conversationPaneLifetime(independentMain)).toBe(independentMainLifetime);
  expect(conversationPaneLifetime(independentSource)).toBe(independentSourceLifetime);
  expect(independentMainLifetime.alive).toBe(true);
  expect(independentSourceLifetime.alive).toBe(true);
});

interface NeedsYouWireRead {
  offset?: number;
  limit?: number;
}

// These keyboard cases exercise AppShell's shipped Mod+J path end to end: the
// keybinding action calls the real NeedsYouController, which reads the real
// navigation store and demands pages through the client's
// "evener/navigation/read" wire boundary. The helpers below script only that
// boundary (FakeClient transport); every controller, store, route, provider
// and opener stays real. Every other navigation resource keeps the file's
// shared navigationRead fixture.
function needsYouClient(
  readNeedsYou: (params: NavigationReadParams) => NavigationReadResponse | Promise<NavigationReadResponse>,
  needsYouCount = 1,
): FakeClient {
  const client = navClient();
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "section" && params.section === "needs_you") return readNeedsYou(params);
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        {
          ...EMPTY_NAV_RESPONSE,
          sections: { ...EMPTY_NAV_RESPONSE.sections, needs_you: { count: needsYouCount } },
        },
        '"test"',
      );
    }
    return navigationRead(params);
  });
  return client;
}

function needsYouPages() {
  return [...navigationStore.getState().resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === "needs_you",
  );
}

// A page read held at the real client boundary. The boot fan-out and the
// keyboard activation share this one in-flight wire request, exactly as they
// do against the hub.
function heldNeedsYouPage(rowRef: string): {
  client: FakeClient;
  reads: NeedsYouWireRead[];
  entered: Promise<void>;
  release: () => void;
} {
  const reads: NeedsYouWireRead[] = [];
  let releasePage!: () => void;
  let enteredPage!: () => void;
  const entered = new Promise<void>((resolve) => (enteredPage = resolve));
  const client = needsYouClient((params) => {
    reads.push({ offset: params.offset, limit: params.limit });
    enteredPage();
    return new Promise<NavigationReadResponse>((resolve) => {
      releasePage = () =>
        resolve(
          wireSnapshot(params, { sessions: [{ ref: rowRef }], remaining: 0 }, '"held-page"', 1, "generation_test"),
        );
    });
  });
  return { client, reads, entered, release: () => releasePage() };
}

test("Mod+J cold-demand requests page zero once and opens its first ref", async () => {
  const row = { ...TREE_SESSION, ref: "local:cold", session_id: "cold", title: "Cold row", state: "awaiting" };
  const reads: NeedsYouWireRead[] = [];
  let releaseManifest!: () => void;
  let releasePage!: () => void;
  let enteredManifest!: () => void;
  const manifestEntered = new Promise<void>((resolve) => (enteredManifest = resolve));
  const client = navClient();
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      enteredManifest();
      return new Promise<NavigationReadResponse>((resolve) => {
        releaseManifest = () =>
          resolve(
            wireSnapshot(
              params,
              { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, needs_you: { count: 1 } } },
              '"test"',
            ),
          );
      });
    }
    if (params.resource === "section" && params.section === "needs_you") {
      reads.push({ offset: params.offset, limit: params.limit });
      return new Promise<NavigationReadResponse>((resolve) => {
        releasePage = () =>
          resolve(wireSnapshot(params, { sessions: [row], remaining: 0 }, '"cold-page"', 1, "generation_test"));
      });
    }
    return navigationRead(params);
  });
  render(<AppShell client={client} />);
  await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
  // The keyboard demand runs before the cold manifest has arrived: unknown
  // membership keeps the demand path, so this activation is the one that
  // asks for page zero.
  await manifestEntered;
  const historyBefore = window.history.length;
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(reads).toEqual([{ offset: 0, limit: 50 }]));
  await act(async () => {
    releaseManifest();
    await Promise.resolve();
  });
  await act(async () => {
    releasePage();
    await Promise.resolve();
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: row.ref }));
  expect(reads).toEqual([{ offset: 0, limit: 50 }]);
  expect(window.location.pathname).toBe("/s/local%3Acold");
  expect(focusedSessionRef()).toBe(row.ref);
  expect(window.history.length).toBe(historyBefore + 1);
  expect(paneFor(row.ref)).toBeTruthy();
});

test("Mod+J loads the next needs-you page after the focused last row", async () => {
  const current = {
    ...TREE_SESSION,
    ref: "local:page-one-last",
    session_id: "page-one-last",
    title: "Page one last",
    state: "awaiting",
  };
  const next = {
    ...TREE_SESSION,
    ref: "local:page-two-first",
    session_id: "page-two-first",
    title: "Page two first",
    state: "awaiting",
  };
  const firstPageRows = [
    ...Array.from({ length: 49 }, (_, index) => ({
      ...TREE_SESSION,
      ref: `local:page-one-${index}`,
      session_id: `page-one-${index}`,
      title: `Page one ${index}`,
      state: "awaiting",
    })),
    current,
  ];
  const reads: NeedsYouWireRead[] = [];
  const client = needsYouClient((params) => {
    reads.push({ offset: params.offset, limit: params.limit });
    return wireSnapshot(
      params,
      params.offset === 0 ? { sessions: firstPageRows, remaining: 1 } : { sessions: [next], remaining: 0 },
      `"page-${params.offset}"`,
      1,
      "generation_test",
    );
  }, firstPageRows.length + 1);
  window.history.pushState({}, "", "/s/local%3Apage-one-last");
  render(<AppShell client={client} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: current.ref }));
  await waitFor(() =>
    expect(needsYouPages().some((page) => page.key.kind === "section" && page.key.offset === 0 && !page.loading)).toBe(
      true,
    ),
  );
  const sourcePane = paneFor(current.ref);
  const historyBefore = window.history.length;

  fireEvent.keyDown(window, { key: "j", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: next.ref }));
  expect(reads).toEqual([
    { offset: 0, limit: 50 },
    { offset: 50, limit: 50 },
  ]);
  expect(window.location.pathname).toBe("/s/local%3Apage-two-first");
  expect(focusedSessionRef()).toBe(next.ref);
  expect(window.history.length).toBe(historyBefore + 1);
  expect(paneFor(current.ref)).toBe(sourcePane);
  expect(paneFor(next.ref)).toBeTruthy();
});

test("late Mod-J page success does not navigate after focus moves to Settings", async () => {
  const held = heldNeedsYouPage("local:late");
  render(<AppShell client={held.client} />);
  await screen.findByText("No session open");
  await held.entered;
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  act(() => workspaceStore.getState().replacePrimary("settings", {}));
  await act(async () => {
    held.release();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
});

test("late Mod-J page success does not navigate after a modal opens", async () => {
  const held = heldNeedsYouPage("local:modal-late");
  render(<AppShell client={held.client} />);
  await screen.findByText("No session open");
  await held.entered;
  const historyBeforeModal = window.history.length;
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  await act(async () => {
    document.body.appendChild(modal);
    await Promise.resolve();
  });
  await act(async () => {
    held.release();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
  expect(window.location.pathname).toBe("/");
  await act(async () => {
    modal.remove();
    await Promise.resolve();
    await Promise.resolve();
  });
  // After the real close has settled, and before any new shortcut, the
  // cancelled hop is still absent: no URL or history change, no session
  // pane, and the completed page is available with its row.
  await waitFor(() =>
    expect(
      needsYouPages().some(
        (page) =>
          page.key.kind === "section" &&
          page.key.offset === 0 &&
          !page.loading &&
          !page.error &&
          !page.stale &&
          ((page.data as { sessions?: Array<{ ref: string }> } | null)?.sessions ?? []).some(
            (row) => row.ref === "local:modal-late",
          ),
      ),
    ).toBe(true),
  );
  expect(window.location.pathname).toBe("/");
  expect(window.history.length).toBe(historyBeforeModal);
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("session");
  expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
  // Closing the modal does not resurrect the cancelled hop, but a later
  // deliberate shortcut sees the now-loaded page and navigates.
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:modal-late" }));
  expect(window.location.pathname).toBe("/s/local%3Amodal-late");
  expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
});

test("Mod-J does not re-request an in-flight needs-you page", async () => {
  const held = heldNeedsYouPage("local:held");
  render(<AppShell client={held.client} />);
  await screen.findByText("No session open");
  await held.entered;
  const historyBefore = window.history.length;
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await act(async () => {
    held.release();
    await Promise.resolve();
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:held" }));
  expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
  expect(window.location.pathname).toBe("/s/local%3Aheld");
  expect(focusedSessionRef()).toBe("local:held");
  expect(window.history.length).toBe(historyBefore + 1);
});

test("Mod-J retries a failed needs-you page on a later deliberate activation", async () => {
  const reads: NeedsYouWireRead[] = [];
  const client = needsYouClient((params) => {
    reads.push({ offset: params.offset, limit: params.limit });
    if (reads.length === 1) throw new Error("transport failure");
    return wireSnapshot(
      params,
      { sessions: [{ ref: "local:recovered" }], remaining: 0 },
      '"recovered"',
      1,
      "generation_test",
    );
  });
  render(<AppShell client={client} />);
  await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
  await waitFor(() => expect(reads).toEqual([{ offset: 0, limit: 50 }]));
  await waitFor(() => expect(needsYouPages().some((page) => !!page.error)).toBe(true));
  // Failure is contained: the shell stays put with no session opened.
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("session");

  fireEvent.keyDown(window, { key: "j", metaKey: true });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:recovered" }));
  expect(reads).toEqual([
    { offset: 0, limit: 50 },
    { offset: 0, limit: 50 },
  ]);
  expect(window.location.pathname).toBe("/s/local%3Arecovered");
  expect(focusedSessionRef()).toBe("local:recovered");
});

test("Mod-J does not re-request an empty needs-you page", async () => {
  const reads: NeedsYouWireRead[] = [];
  const client = needsYouClient((params) => {
    reads.push({ offset: params.offset, limit: params.limit });
    return wireSnapshot(params, { sessions: [], remaining: 0 }, '"empty"', 1, "generation_test");
  });
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => expect(reads).toEqual([{ offset: 0, limit: 50 }]));
  await waitFor(() =>
    expect(
      needsYouPages().some(
        (page) => page.key.kind === "section" && page.key.offset === 0 && page.data !== null && !page.loading,
      ),
    ).toBe(true),
  );
  await act(async () => {
    await Promise.resolve();
  });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await act(async () => {
    await Promise.resolve();
  });
  expect(reads).toEqual([{ offset: 0, limit: 50 }]);
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("session");
});

// ---------------------------------------------------------------------------
// Route and history cancellation during a deferred attention hop.
//
// These cases hold real wire reads at the FakeClient transport boundary: the
// needs-you page that carries the hop's target, plus any session location read
// a case asks to defer. Every controller, navigation store, route observer,
// keyboard binding, attention-history seam and opener stays real; only the
// transport reply timing is scripted. No assertion below reads human-readable
// copy - the oracles are paths, browser history length and entry identity,
// wire reads, pane object identity and attention-entry tokens.
// ---------------------------------------------------------------------------
interface HeldRouteWire {
  client: FakeClient;
  needsYouReads: NeedsYouWireRead[];
  locationReads: string[];
  needsYouDemanded: Promise<void>;
  locationDemanded: (ref: string) => Promise<void>;
  releaseNeedsYou: () => void;
  releaseLocation: (ref: string) => void;
}

function heldRouteWire(targetRef: string, heldLocationRefs: readonly string[]): HeldRouteWire {
  const needsYouReads: NeedsYouWireRead[] = [];
  const locationReads: string[] = [];
  const needsYouEntered = deferred<void>();
  const locationEntered = new Map(heldLocationRefs.map((ref) => [ref, deferred<void>()]));
  const needsYouFinishes: Array<() => void> = [];
  const locationFinishes = new Map<string, Array<() => void>>();
  const client = navClient();
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, needs_you: { count: 1 } } },
        '"test"',
      );
    }
    if (params.resource === "section" && params.section === "needs_you") {
      needsYouReads.push({ offset: params.offset, limit: params.limit });
      needsYouEntered.resolve();
      return new Promise<NavigationReadResponse>((resolve) => {
        needsYouFinishes.push(() =>
          resolve(
            wireSnapshot(
              params,
              {
                sessions: [
                  { ...TREE_SESSION, ref: targetRef, session_id: targetRef, title: targetRef, state: "awaiting" },
                ],
                remaining: 0,
              },
              '"held-needs-you"',
              1,
              "generation_test",
            ),
          ),
        );
      });
    }
    if (params.resource === "location") {
      const ref = params.ref;
      if (ref === undefined || !heldLocationRefs.includes(ref)) return navigationRead(params);
      locationReads.push(ref);
      locationEntered.get(ref)?.resolve();
      return new Promise<NavigationReadResponse>((resolve) => {
        const finishes = locationFinishes.get(ref) ?? [];
        finishes.push(() =>
          resolve(
            wireSnapshot(
              params,
              {
                ref,
                top_level_ref: ref,
                top_level: true,
                session: { ...TREE_SESSION, ref, session_id: ref },
              },
              '"held-location"',
              1,
              "generation_test",
            ),
          ),
        );
        locationFinishes.set(ref, finishes);
      });
    }
    return navigationRead(params);
  });
  const release = (finishes: Array<() => void>, what: string): void => {
    if (finishes.length === 0) throw new Error(`No actual ${what} request to release`);
    for (const finish of finishes.splice(0)) finish();
  };
  return {
    client,
    needsYouReads,
    locationReads,
    needsYouDemanded: needsYouEntered.promise,
    locationDemanded: async (ref) => {
      const entered = locationEntered.get(ref);
      if (!entered) throw new Error(`location ${ref} was not scripted to be held`);
      await entered.promise;
    },
    releaseNeedsYou: () => release(needsYouFinishes, "needs-you page"),
    releaseLocation: (ref) => release(locationFinishes.get(ref) ?? [], `location ${ref}`),
  };
}

// Starts on the welcome route, completes the boot fan-out, then follows one
// real ordinary session navigation so the source reader is placed by the same
// AppShell route path a user reaches. Returns the exact placed pane record.
async function beginAtRoutedSource(held: HeldRouteWire, sourceRef: string): Promise<OpenPaneRecord> {
  window.history.pushState({}, "", "/");
  render(<AppShell client={held.client} />);
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.type).toBe("welcome"));
  await held.needsYouDemanded;
  await act(async () => {
    navigate(`/s/${encodeURIComponent(sourceRef)}`);
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef }));
  const sourcePane = paneFor(sourceRef);
  if (!sourcePane) throw new Error("missing routed source reader");
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(focusedSessionRef()).toBe(sourceRef);
  return sourcePane;
}

function recordAcceptedRoutes(): string[] {
  const accepted: string[] = [];
  const record = () => accepted.push(window.location.pathname);
  window.addEventListener("popstate", record);
  onTestFinished(() => window.removeEventListener("popstate", record));
  return accepted;
}

test("held Next is cancelled when an accepted ordinary route holds its location read", async () => {
  const sourceRef = "local:route-source";
  const ordinaryRef = "local:route-ordinary";
  const targetRef = "local:route-next";
  const held = heldRouteWire(targetRef, [ordinaryRef]);
  const sourcePane = await beginAtRoutedSource(held, sourceRef);
  const accepted = recordAcceptedRoutes();
  const historyBeforeRoute = window.history.length;

  fireEvent.keyDown(window, { key: "j", metaKey: true });

  await act(async () => {
    navigate(`/s/${encodeURIComponent(ordinaryRef)}`);
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(true),
  );
  await held.locationDemanded(ordinaryRef);

  // The ordinary route is accepted and its location read is still in flight,
  // so the exact source reader survives in place while the route waits.
  expect(accepted).toEqual([`/s/${encodeURIComponent(ordinaryRef)}`]);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(ordinaryRef)}`);
  expect(window.history.length).toBe(historyBeforeRoute + 1);
  expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(focusedSessionRef()).toBe(sourceRef);
  expect(currentAttentionEntryToken()).toBeNull();

  // Release the hop's page first: the newer accepted route must win.
  await act(async () => {
    held.releaseNeedsYou();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  // The hop's target was a real, selectable next row when the read landed;
  // the absence of navigation below is the cancellation, not a missing page.
  expect(selectNeedsYouRows(navigationStore.getState()).some((row) => row.ref === targetRef)).toBe(true);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(ordinaryRef)}`);
  expect(window.history.length).toBe(historyBeforeRoute + 1);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(currentAttentionEntryToken()).toBeNull();

  // Settling the ordinary location then places it; nothing revives the hop.
  await act(async () => {
    held.releaseLocation(ordinaryRef);
    await Promise.resolve();
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: ordinaryRef }));
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(ordinaryRef)}`);
  expect(focusedSessionRef()).toBe(ordinaryRef);
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(ordinaryRef)?.id);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(currentAttentionEntryToken()).toBeNull();
  expect(window.history.length).toBe(historyBeforeRoute + 1);
});

test("held Next stays cancelled after an A to X to A traversal before reads settle", async () => {
  const sourceRef = "local:route-back";
  const ordinaryRef = "local:route-cross";
  const targetRef = "local:route-back-next";
  const held = heldRouteWire(targetRef, [ordinaryRef]);
  const sourcePane = await beginAtRoutedSource(held, sourceRef);
  const accepted = recordAcceptedRoutes();
  const historyBeforeTraversal = window.history.length;

  fireEvent.keyDown(window, { key: "j", metaKey: true });

  await act(async () => {
    navigate(`/s/${encodeURIComponent(ordinaryRef)}`);
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(true),
  );
  await held.locationDemanded(ordinaryRef);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(ordinaryRef)}`);
  expect(paneFor(sourceRef)).toBe(sourcePane);

  // Back to the source before the intervening location read settles. The
  // cached source location places it exactly; no new pane is created.
  await act(async () => {
    navigate(`/s/${encodeURIComponent(sourceRef)}`);
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef }));
  expect(accepted).toEqual([`/s/${encodeURIComponent(ordinaryRef)}`, `/s/${encodeURIComponent(sourceRef)}`]);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(sourceRef)}`);
  expect(window.history.length).toBe(historyBeforeTraversal + 2);
  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef });
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(focusedSessionRef()).toBe(sourceRef);

  // Even though the final ref matches the hop's own original source, the
  // cancelled action must not navigate now.
  await act(async () => {
    held.releaseNeedsYou();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  expect(selectNeedsYouRows(navigationStore.getState()).some((row) => row.ref === targetRef)).toBe(true);
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(sourceRef)}`);
  expect(window.history.length).toBe(historyBeforeTraversal + 2);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(currentAttentionEntryToken()).toBeNull();

  // Settle the remaining wire request; the cancelled hop stays dead.
  await act(async () => {
    held.releaseLocation(ordinaryRef);
    await Promise.resolve();
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(
      false,
    ),
  );
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent(sourceRef)}`);
  expect(window.history.length).toBe(historyBeforeTraversal + 2);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef });
  expect(focusedSessionRef()).toBe(sourceRef);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(currentAttentionEntryToken()).toBeNull();
});

test("held Next stays cancelled when browser history traverses to a same-path entry", async () => {
  const sourceRef = "local:route-same";
  const ordinaryRef = "local:route-between";
  const targetRef = "local:route-same-next";
  const sourcePath = `/s/${encodeURIComponent(sourceRef)}`;
  const ordinaryPath = `/s/${encodeURIComponent(ordinaryRef)}`;
  const held = heldRouteWire(targetRef, [ordinaryRef]);
  const sourcePane = await beginAtRoutedSource(held, sourceRef);
  const firstEntryId = routeHistoryEntryId(window.history.state);
  if (firstEntryId === null) throw new Error("source route entry has no identity");

  const accepted = recordAcceptedRoutes();
  fireEvent.keyDown(window, { key: "j", metaKey: true });

  // Build a second A entry behind an intervening route, then return to the
  // first entry through real browser history. Pathname alone cannot tell the
  // two entries apart; their recorded entry identities can.
  await act(async () => {
    navigate(`/s/${encodeURIComponent(ordinaryRef)}`);
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(true),
  );
  await held.locationDemanded(ordinaryRef);
  await act(async () => {
    navigate(sourcePath);
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef }));
  const secondEntryId = routeHistoryEntryId(window.history.state);
  if (secondEntryId === null) throw new Error("second source route entry has no identity");
  expect(window.location.pathname).toBe(sourcePath);
  expect(secondEntryId).not.toBe(firstEntryId);
  const historyBeforeTraversal = window.history.length;

  const toIntervening = new Promise<void>((resolve) =>
    window.addEventListener("popstate", () => resolve(), { once: true }),
  );
  await act(async () => {
    window.history.back();
    await toIntervening;
  });
  expect(window.location.pathname).toBe(ordinaryPath);
  expect(accepted).toEqual([ordinaryPath, sourcePath, ordinaryPath]);

  const toFirstEntry = new Promise<void>((resolve) =>
    window.addEventListener("popstate", () => resolve(), { once: true }),
  );
  await act(async () => {
    window.history.back();
    await toFirstEntry;
  });
  expect(window.location.pathname).toBe(sourcePath);
  expect(routeHistoryEntryId(window.history.state)).toBe(firstEntryId);
  expect(window.history.length).toBe(historyBeforeTraversal);
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(focusedSessionRef()).toBe(sourceRef);

  // The same pathname, but the traversal's own popstate cancelled the hop.
  await act(async () => {
    held.releaseNeedsYou();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  expect(selectNeedsYouRows(navigationStore.getState()).some((row) => row.ref === targetRef)).toBe(true);
  expect(window.location.pathname).toBe(sourcePath);
  expect(routeHistoryEntryId(window.history.state)).toBe(firstEntryId);
  expect(window.history.length).toBe(historyBeforeTraversal);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(currentAttentionEntryToken()).toBeNull();

  await act(async () => {
    held.releaseLocation(ordinaryRef);
    await Promise.resolve();
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(
      false,
    ),
  );
  expect(window.location.pathname).toBe(sourcePath);
  expect(window.history.length).toBe(historyBeforeTraversal);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(currentAttentionEntryToken()).toBeNull();
});

test("held Next stays cancelled through repeated A to X to A traversal", async () => {
  const sourceRef = "local:route-loop";
  const ordinaryRef = "local:route-loop-cross";
  const targetRef = "local:route-loop-next";
  const held = heldRouteWire(targetRef, [ordinaryRef]);
  const sourcePane = await beginAtRoutedSource(held, sourceRef);
  const accepted = recordAcceptedRoutes();
  const historyBeforeTraversal = window.history.length;
  const sourcePath = `/s/${encodeURIComponent(sourceRef)}`;
  const ordinaryPath = `/s/${encodeURIComponent(ordinaryRef)}`;

  fireEvent.keyDown(window, { key: "j", metaKey: true });

  for (let pass = 0; pass < 2; pass++) {
    await act(async () => {
      navigate(ordinaryPath);
    });
    await waitFor(() =>
      expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(
        true,
      ),
    );
    await held.locationDemanded(ordinaryRef);
    expect(window.location.pathname).toBe(ordinaryPath);
    await act(async () => {
      navigate(sourcePath);
    });
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: sourceRef }));
    await waitFor(() => expect(window.location.pathname).toBe(sourcePath));
    expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
    expect(paneFor(sourceRef)).toBe(sourcePane);
  }

  expect(accepted).toEqual([ordinaryPath, sourcePath, ordinaryPath, sourcePath]);
  // The one deferred location read is reused across both passes, so the wire
  // was asked exactly once.
  expect(held.locationReads).toEqual([ordinaryRef]);
  expect(window.history.length).toBe(historyBeforeTraversal + 4);

  await act(async () => {
    held.releaseNeedsYou();
    await Promise.resolve();
  });
  await waitFor(() => expect(needsYouPages().every((page) => !page.loading)).toBe(true));
  expect(selectNeedsYouRows(navigationStore.getState()).some((row) => row.ref === targetRef)).toBe(true);
  expect(window.location.pathname).toBe(sourcePath);
  expect(window.history.length).toBe(historyBeforeTraversal + 4);
  expect(paneFor(targetRef)).toBeUndefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(sourcePane.id);
  expect(paneFor(sourceRef)).toBe(sourcePane);
  expect(currentAttentionEntryToken()).toBeNull();

  await act(async () => {
    held.releaseLocation(ordinaryRef);
    await Promise.resolve();
  });
  await waitFor(() =>
    expect(navigationStore.getState().resources.get(keyID({ kind: "location", ref: ordinaryRef }))?.loading).toBe(
      false,
    ),
  );
  expect(window.location.pathname).toBe(sourcePath);
  expect(window.history.length).toBe(historyBeforeTraversal + 4);
  expect(paneFor(targetRef)).toBeUndefined();
});

test("clicking any [data-search-trigger] element opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  // Any element in the app can mark itself as a search trigger; the global
  // listener AppShell installs opens the palette on its click.
  const trigger = document.createElement("button");
  trigger.setAttribute("data-search-trigger", "");
  trigger.textContent = "Search";
  document.body.appendChild(trigger);
  await user.click(trigger);

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
  trigger.remove();
});

test("clicking the rail's own Search button opens the command palette", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  // The real shipped affordance, not a synthetic stand-in: the rail header's
  // icon-only Search button is the app's one clickable way into the palette,
  // so the wiring between it and the global listener is worth an end-to-end
  // assertion of its own.
  await user.click(await screen.findByTestId("rail-search"));

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("populates connectionStore metadata from one injected initialize response", async () => {
  const fake = new FakeClient("connecting");
  const scripted: InitializeResponse = {
    serverInfo: { name: "evener-hub-test", version: "9.9.9" },
    protocolVersion: "1",
    sourceId: "src_test",
    features: ALL_FEATURES_OFF,
  };
  fake.scriptConnect(() => {
    fake.emitReady(scripted);
    return scripted;
  });

  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  await waitFor(() => {
    expect(connectionStore.getState().serverInfo).toEqual({ name: "evener-hub-test", version: "9.9.9" });
    expect(connectionStore.getState().features).toEqual(scripted.features);
  });
});

test("a late boot connect result cannot overwrite automatic reconnect metadata", async () => {
  const fake = new FakeClient("connecting");
  const initial: InitializeResponse = {
    serverInfo: { name: "initial-hub", version: "1.0.0" },
    protocolVersion: "1",
    sourceId: "initial-source",
    features: ALL_FEATURES_OFF,
  };
  const reconnected = { ...initial, serverInfo: { name: "reconnected-hub", version: "2.0.0" } };
  const pending = deferred<InitializeResponse>();
  fake.scriptConnect(() => {
    fake.emitReady(initial);
    return pending.promise;
  });
  render(<AppShell client={fake} />);
  await waitFor(() => expect(connectionStore.getState().serverInfo).toEqual(initial.serverInfo));

  act(() => {
    fake.emitStateChange("reconnecting");
    fake.emitReady(reconnected);
  });
  await act(async () => {
    pending.resolve(initial);
    await pending.promise;
  });

  expect(connectionStore.getState().serverInfo).toEqual(reconnected.serverInfo);
  expect(connectionStore.getState().features).toEqual(reconnected.features);
});

test("closes the client it constructed itself on unmount", async () => {
  // Supersession, Jesse 2026-10-14: the old same-tick close assertion pinned
  // the defect. Require the disposal's final read to settle before one close.
  resetThreadsStoreForTests();
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `appshell-owned-final-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  await cache.getClearEpoch();
  const finalRead = deferred<ReturnType<typeof currentThreadRead>>();
  const issued = deferred<void>();
  const transport = backgroundReadyClient();
  await transport.connect();
  expect(transport.initializeResult?.navigation?.generationId).toBe("generation_test");
  let final = false;
  transport.on("thread/read", (params) => {
    if (!final) return currentThreadRead(params);
    issued.resolve();
    return finalRead.promise.then((response) => ({ ...response, requestGeneration: params.requestGeneration }));
  });
  // Only the transport is scripted. Discovery, leases, repository retirement,
  // cache writes and background disposal are the production implementations.
  const state = vi.spyOn(AppwireClient.prototype, "state", "get").mockReturnValue("ready");
  const initialize = vi
    .spyOn(AppwireClient.prototype, "initializeResult", "get")
    .mockReturnValue(transport.initializeResult);
  const ready = vi.spyOn(AppwireClient.prototype, "onReady").mockImplementation(transport.onReady.bind(transport));
  const changes = vi
    .spyOn(AppwireClient.prototype, "onStateChange")
    .mockImplementation(transport.onStateChange.bind(transport));
  const notifications = vi
    .spyOn(AppwireClient.prototype, "onNotification")
    .mockImplementation(transport.onNotification.bind(transport));
  const request = vi.spyOn(AppwireClient.prototype, "request").mockImplementation(transport.request.bind(transport));
  const closeSpy = vi.spyOn(AppwireClient.prototype, "close");
  const owners = trackBackgroundOwners();
  const { unmount } = render(<AppShell />);
  const wired = connectionStore.getState().client;
  try {
    await act(async () => {
      await transport.connect();
    });
    // Owned connect is deliberately disabled in test mode. Initialize the
    // real navigation binding with the scripted transport's negotiated v3.
    act(() => {
      resetNavigationStoreForTests();
      initNavigation(wired!, { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] });
    });
    await waitFor(() => expect(threadsModule.productionEntryTranscriptForTests("local:s1").baseline).toBeDefined());
    await owners.records[0]!.owner.settled();
    final = true;
    const boundary = request.mock.calls.length;
    unmount();
    await act(async () => {
      await issued.promise;
    });
    const finalIndex = request.mock.calls.findIndex(([method], index) => index >= boundary && method === "thread/read");
    expect(finalIndex).toBeGreaterThanOrEqual(boundary);
    expect(request.mock.contexts[finalIndex]).toBe(wired);
    expect(request.mock.calls[finalIndex]?.[1]).toMatchObject({ ref: "local:s1", includeTurns: true });
    expect(closeSpy).not.toHaveBeenCalled();
    await act(async () => {
      finalRead.resolve(currentThreadRead({ ref: "local:s1" }));
      await owners.joinDisposals();
    });
    expect(closeSpy).toHaveBeenCalledTimes(1);
    expect(closeSpy.mock.contexts[0]).toBe(wired);
  } finally {
    finalRead.resolve(currentThreadRead({ ref: "local:s1" }));
    unmount();
    await owners.joinDisposals();
    owners.restore();
    request.mockRestore();
    closeSpy.mockRestore();
    initialize.mockRestore();
    state.mockRestore();
    ready.mockRestore();
    changes.mockRestore();
    notifications.mockRestore();
    resetThreadsStoreForTests();
    cache.close();
  }
});

test.each([false, true])("bounds stuck disposal without closing injected client=%s", async (injected) => {
  const stuck = deferred<void>();
  const start = vi.spyOn(backgroundTranscriptsModule, "startBackgroundTranscripts").mockReturnValue({
    settled: async () => {},
    dispose: () => stuck.promise,
  });
  const close = vi.spyOn(AppwireClient.prototype, "close");
  const client = new FakeClient("ready");
  const injectedClose = vi.spyOn(client, "close");
  const view = render(<AppShell client={injected ? client : undefined} />);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(29_999);
    });
    expect(close).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(close).toHaveBeenCalledTimes(injected ? 0 : 1);
    expect(injectedClose).not.toHaveBeenCalled();
    await act(async () => {
      stuck.resolve();
      await stuck.promise;
    });
    expect(close).toHaveBeenCalledTimes(injected ? 0 : 1);
    expect(injectedClose).not.toHaveBeenCalled();
  } finally {
    stuck.resolve();
    vi.useRealTimers();
    start.mockRestore();
    close.mockRestore();
    injectedClose.mockRestore();
  }
});

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

// --- mobile full-bleed shell (2026-07-30-mobile-session-layout-design.md, decision 1) ---
// jsdom performs no layout, so media-query rules are verified by reading the
// CSS module's own source, the same way panescaffold.test.tsx verifies its
// truncation rule.

test("mobile: the shell content frame drops its padding so the workspace is full-bleed", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const contentRule = mobile![1]!.match(/\.content \{([^}]*)\}/);
  expect(contentRule).not.toBeNull();
  expect(contentRule![1]).toContain("padding: 0");
  // The flush sidebar is the rule at EVERY width now, so the mobile block
  // carries no gap reset of its own; a gap: here would mean someone added
  // one back on .content above, which the full-bleed contract test forbids.
  expect(contentRule![1]).not.toContain("gap:");
});

test("mobile: the shell spends --keyboard-inset as bottom padding so the composer clears the on-screen keyboard", () => {
  // See useKeyboardInset.ts's header for the mechanism. Comments are stripped
  // before matching (testing.md: a comment quoting the declaration must not
  // satisfy the assertion).
  const css = readModuleCss(import.meta.url, "AppShell.module.css");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const shellRule = mobile![1]!.match(/\.shell \{([\s\S]*?)\n[ ]{2}\}/);
  expect(shellRule).not.toBeNull();
  expect(shellRule![0]).toContain("padding-bottom: var(--keyboard-inset, 0px)");
});

test("mobile: the shared shell follows the visible viewport while retaining a vh fallback", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const shellRule = css.match(/(^|\n)\.shell \{([\s\S]*?)\n\}/);
  expect(shellRule).not.toBeNull();
  expect(shellRule![0]).toContain("height: 100vh");
  expect(shellRule![0]).not.toContain("height: 100dvh");

  const supportsBlock = css.match(/@supports \(height: 100dvh\) \{([\s\S]*?)\n\}/);
  expect(supportsBlock).not.toBeNull();
  expect(supportsBlock!.index).toBeGreaterThan(shellRule!.index ?? -1);

  const supportsShellRule = supportsBlock![1]!.match(/\.shell \{([\s\S]*?)\n[ ]{2}\}/);
  expect(supportsShellRule).not.toBeNull();
  expect(supportsShellRule![0]).toContain("height: 100dvh");
  expect(supportsShellRule![0]).not.toContain("height: 100vh");
});

// --- useKeyboardInset wiring (pins the AppShell -> hook call) --------------
// The hook's own tests pin that it sets --keyboard-inset given a visualViewport,
// and the CSS test above pins that the mobile .shell rule consumes it, but
// nothing connects the two through the real component: jsdom has no
// visualViewport, so every AppShell render in this file never exercises it.
// This test stubs one and renders the real AppShell to close that gap.

test("AppShell mounts useKeyboardInset, so a visualViewport resize sets --keyboard-inset on the root", async () => {
  // A real EventTarget so the hook's add/removeEventListener round-trips; the
  // hook reads height/offsetTop/scale off the same object.
  class FakeVisualViewport extends EventTarget {
    height = 768;
    offsetTop = 0;
    scale = 1;
  }
  const fake = new FakeVisualViewport();
  vi.stubGlobal("visualViewport", fake);
  Object.defineProperty(window, "innerHeight", { value: 768, configurable: true, writable: true });
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  // Mount sets the initial value (0px: the fake viewport covers the layout).
  expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");
  // Keyboard opens: visualViewport shrinks; the hook writes the occluded strip.
  fake.height = 400;
  fake.dispatchEvent(new Event("resize"));
  expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("368px");
  unmount();
  vi.unstubAllGlobals();
});

// jsdom implements no matchMedia at all (useIsMobile.test.ts's own header
// comment documents the probe), so a mobile-layout test installs one: the
// mobile query matches, every other query does not. Only useIsMobile reads
// this surface, and only ever at mount here - no test in this file crosses
// the breakpoint mid-run, so the listeners are inert stubs.
function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

test("ordinary mobile toolbar Back publishes only the Welcome transition", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  installLocationForRoute("local:origin");
  window.history.pushState({}, "", "/s/local%3Aorigin");
  const client = navClient();
  client.on("thread/read", () => ({ thread: threadStartResponse("local:origin").thread }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByRole("textbox", { name: "Message" });
  expect(workspaceStore.getState().mainPane()).toMatchObject({
    type: "session",
    params: { ref: "local:origin" },
  });
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
  expect(getDockviewApi()).toBeNull();

  const events: { method: "push" | "replace"; path: string }[] = [];
  const pushState = window.history.pushState.bind(window.history);
  const replaceState = window.history.replaceState.bind(window.history);
  const push = vi.spyOn(window.history, "pushState").mockImplementation((...args) => {
    pushState(...args);
    events.push({ method: "push", path: window.location.pathname });
  });
  const replace = vi.spyOn(window.history, "replaceState").mockImplementation((...args) => {
    replaceState(...args);
    events.push({ method: "replace", path: window.location.pathname });
  });
  try {
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => {
      const workspace = workspaceStore.getState();
      expect(workspace.panes.find((pane) => pane.id === workspace.focusedPaneId)?.type).toBe("welcome");
      expect(window.location.pathname).toBe("/");
      expect(document.querySelector("[data-stacked-pane-type]")?.getAttribute("data-stacked-pane-type")).toBe(
        "welcome",
      );
    });
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    expect(events).toEqual([{ method: "push", path: "/" }]);
  } finally {
    push.mockRestore();
    replace.mockRestore();
  }
});

test("mobile unencoded session mount still reconciles later location ownership", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  installLocationForRoute("local:origin");
  window.history.pushState({}, "", "/s/local:origin");
  const client = navClient();
  client.on("thread/read", () => ({ thread: threadStartResponse("local:origin").thread }));
  render(<AppShell client={client} />);
  await screen.findByRole("textbox", { name: "Message" });
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:origin" });

  act(() => installLocationForRoute("local:origin", "local:owner"));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" }));
  expect(paneFor("local:origin")).toMatchObject({ type: "session", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:origin")?.id);
  expect(window.location.pathname).toBe("/s/local%3Aorigin");
});

test.each(["phone", "desktop"])(
  "%s keeps an opened child focused after the already placed parent location settles",
  async (host) => {
    if (host === "phone") {
      vi.stubGlobal("innerWidth", 390);
      installMobileViewport();
    }
    let releaseLocation = () => {};
    const locationReady = new Promise<void>((resolve) => {
      releaseLocation = resolve;
    });
    const client = navClient();
    // Opening the child conversation makes the phone's URL sync publish the
    // child's own /s/{ref} route, so its location must say the child is
    // nested under the settled owner - the same ancestry the thread fixtures
    // describe - or the route placement would treat it as a top-level session.
    installLocationForRoute("local:child");
    client.on("evener/navigation/read", async (params) => {
      if (params.resource === "location" && params.ref === "local:owner") await locationReady;
      return navigationRead(params);
    });
    client.on("thread/read", ({ ref }) => activityDetailsThread(ref, { name: ref }));
    client.on("thread/unsubscribe", () => ({}));
    client.on("evener/thread/activity/read", answerActivityRead);
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={client} />
        <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
      </>,
    );
    await screen.findByText("No session open");
    act(() => {
      workspaceStore.getState().openPane("session", { ref: "local:owner" });
      navigate("/s/local%3Aowner");
    });
    await screen.findByRole("textbox", { name: "Message" });
    await waitFor(() =>
      expect(client.calls).toContainEqual(
        expect.objectContaining({
          method: "evener/navigation/read",
          params: expect.objectContaining({ resource: "location", ref: "local:owner" }),
        }),
      ),
    );
    await act(async () => {
      releaseLocation();
      await navigationStore.getState().lookupLocation("local:owner");
    });
    const owner = workspaceStore.getState().mainPane();
    expect(owner?.params).toEqual({ ref: "local:owner" });
    await user.click(screen.getByRole("button", { name: "Open child" }));
    const child = paneFor("local:child");
    expect(child).toMatchObject({ type: "session", params: { ref: "local:child" } });
    expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    // On the phone the address bar names the focused conversation pane; the
    // desktop host never publishes a URL, so the owner's route stays.
    expect(window.location.pathname).toBe(host === "phone" ? "/s/local%3Achild" : "/s/local%3Aowner");
  },
);

// Back from a child opened on phone uses StackHost's own history. The walk
// below covers children opened on desktop, where the host swap discards it.
test("phone Back from an opened child returns to its owner", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  const client = navClient();
  // The child conversation pane has a deep link, and the phone's URL sync
  // publishes it when the child is opened, so the child's location must be
  // loaded and say it is nested under its owner.
  installLocationForRoute("local:child");
  client.on("thread/read", ({ ref }) => {
    if (ref !== "local:owner" && ref !== "local:child") {
      throw new Error(`Unexpected transcript ref: ${ref}`);
    }
    return { thread: { ...threadStartResponse(ref).thread, name: ref } };
  });
  window.history.pushState({}, "", "/s/local%3Aowner");
  const user = userEvent.setup();
  render(
    <>
      <AppShell client={client} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
    </>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  const owner = paneFor("local:owner");
  expect(owner).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(owner?.id));
  expect(getDockviewApi()).toBeNull();

  await user.click(screen.getByRole("button", { name: "Open child" }));
  const child = paneFor("local:child");
  expect(child).toMatchObject({ type: "session", params: { ref: "local:child" } });
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
  expect(workspaceStore.getState().mainPane()).toEqual(owner);
  const back = await screen.findByRole("button", { name: "Back" });
  await waitFor(() => expect(screen.getByTestId("topbar-title").textContent).toBe("local:child"));
  expect(window.location.pathname).toBe("/s/local%3Achild");

  await user.click(back);
  expect(workspaceStore.getState().focusedPaneId).toBe(owner?.id);
  expect(screen.getByTestId("topbar-title").textContent).toBe("local:owner");
  expect(paneFor("local:owner")).toEqual(owner);
  expect(workspaceStore.getState().mainPane()).toEqual(owner);
  expect(window.location.pathname).toBe("/s/local%3Aowner");
  expect(screen.queryByText("No session open")).toBeNull();
});

// Back must use retained immediate-parent context even when Open ran before
// StackHost mounted. A fresh host falling through to Welcome instead of that
// parent is the bug.
test.each(["local:owner", "local:child"])(
  "responsive retained-parent walk: mixed chain from routed session %s keeps exact parents and owner",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 1280);
    const setMobile = installSwitchableViewport();
    window.history.pushState({}, "", `/s/${encodeURIComponent(routeRef)}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const client = navClient();
    client.on("thread/read", ({ ref }) => {
      if (
        typeof ref !== "string" ||
        !["local:owner", "local:child", "local:grandchild", "local:great-grandchild"].includes(ref)
      ) {
        throw new Error(`Unexpected transcript ref: ${ref}`);
      }
      return { thread: { ...threadStartResponse(ref).thread, name: ref } };
    });
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={client} />
        <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" label="Open grandchild" />
        <OpenTranscriptButton
          transcriptRef="local:great-grandchild"
          parentRef="local:grandchild"
          label="Open great-grandchild"
        />
      </>,
    );
    await screen.findByRole("heading", { name: routeRef });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    const owner = paneFor("local:owner");
    expect(owner).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
    expect(getDockviewApi()).not.toBeNull();
    if (routeRef === "local:owner") await user.click(screen.getByRole("button", { name: "Open child" }));
    const child = paneFor("local:child");
    expect(child).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:child" },
    });
    await user.click(screen.getByRole("button", { name: "Open grandchild" }));
    const grandchild = paneFor("local:grandchild");
    expect(grandchild).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:grandchild" },
    });
    await user.click(screen.getByRole("button", { name: "Open great-grandchild" }));
    const descendant = paneFor("local:great-grandchild");
    expect(descendant).toMatchObject({
      type: "session",
      slot: "secondary",
      params: { ref: "local:great-grandchild" },
    });
    expect(workspaceStore.getState().focusedPaneId).toBe(descendant?.id);
    const retained = workspaceStore.getState().panes;
    act(() => {
      vi.stubGlobal("innerWidth", 390);
      setMobile(true);
    });
    await waitFor(() => expect(screen.getByTestId("topbar-title").textContent).toBe("local:great-grandchild"));
    expect(getDockviewApi()).toBeNull();
    expect(workspaceStore.getState().focusedPaneId).toBe(descendant?.id);
    // Literal return order is independent of the production parent resolver.
    const returnTargets = [
      { target: grandchild, title: "local:grandchild" },
      { target: child, title: "local:child" },
      // A routed child carries the same recorded parent context a drilled
      // one does, so Back returns to the retained owner before Welcome.
      { target: owner, title: "local:owner" },
    ];
    for (const { target, title } of returnTargets) {
      expect(target).toBeDefined();
      await user.click(screen.getByRole("button", { name: "Back" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(target?.id);
      expect(screen.getByTestId("topbar-title").textContent).toBe(title);
      expect(workspaceStore.getState().panes).toEqual(retained);
      expect(workspaceStore.getState().mainPane()).toEqual(owner);
      // The address bar names the focused conversation pane on the phone;
      // the nested location reconciles without disturbing the placement.
      expect(window.location.pathname).toBe(`/s/${encodeURIComponent(title)}`);
    }
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByText("No session open")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  },
);

test("Opening the conversation retains focused child after settled parent route reconciliation", async () => {
  vi.stubGlobal("innerWidth", 390);
  installMobileViewport();
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  // The opened conversation pane is deep-linkable and the phone's URL sync
  // publishes its route, so the child's location must be loaded and say it
  // is nested under the owner.
  installLocationForRoute("local:child");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  expect(parent).toMatchObject({ type: "session", slot: "main", params: { ref: "local:owner" } });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  // userEvent's pointer interaction is wrapped in React act, so the assertion
  // below observes the AppShell workspace-panes effect, not the handler's
  // immediate pre-effect focus.
  const openButton = screen.getByRole("button", { name: "Open conversation" });
  await user.click(openButton);

  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(child).toMatchObject({ type: "session", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  await user.click(openButton);
  const repeatedChildren = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(repeatedChildren).toHaveLength(1);
  expect(repeatedChildren[0]?.id).toBe(child?.id);
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
});

test("Opening the conversation retains focused child on desktop until explicit navigation takes precedence", async () => {
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  await user.click(screen.getByRole("button", { name: "Open conversation" }));
  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  act(() => {
    window.history.pushState({}, "", "/s/local:next");
    installLocationForRoute("local:next");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:next" }));
  expect(workspaceStore.getState().focusedPaneId).toBe(workspaceStore.getState().mainPane()?.id);
  expect(paneFor("local:child")).toBeUndefined();
});

test("Opening a nested conversation's job log retains focus on the settled parent route", async () => {
  window.history.pushState({}, "", "/s/local:owner");
  installLocationForRoute("local:owner");
  const user = userEvent.setup();

  render(
    <>
      <AppShell client={navClient()} />
      <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
      <OpenTranscriptButton transcriptRef="job:child-run" parentRef="local:child" label="Open job log" />
    </>,
  );
  await screen.findByText(/loading transcript/i);
  const parent = paneFor("local:owner");
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));

  await user.click(screen.getByRole("button", { name: "Open child" }));
  const child = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);

  // The child's job log: its parent context follows the child's recorded
  // conversation parent (the routed owner), so the settled route must treat
  // the focused job log as a companion instead of refocusing the owner.
  await user.click(screen.getByRole("button", { name: "Open job log" }));
  const job = paneFor("job:child-run");
  expect(job).toMatchObject({ type: "transcript", slot: "secondary" });
  expect(workspaceStore.getState().focusedPaneId).toBe(job?.id);
});

test.each(["local:owner", "local:child"])(
  "Opening the conversation retains focused grandchild on route %s with owner and immediate parent context",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={navClient()} />
        {routeRef === "local:owner" && (
          <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        )}
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    const owner = workspaceStore.getState().mainPane();
    expect(owner).toMatchObject({ type: "session", params: { ref: "local:owner" } });
    if (routeRef === "local:owner") {
      await user.click(screen.getByRole("button", { name: "Open child" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
    }
    const parent = paneFor("local:child");
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    const grandchild = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild");
    expect(grandchild).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);
    expect(grandchild).toMatchObject({
      slot: "secondary",
      params: { ref: "local:grandchild" },
    });
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    expect(paneFor("local:child")).toEqual(parent);
    if (routeRef === "local:owner") expect(parent?.params).toEqual({ ref: "local:child" });

    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    expect(
      workspaceStore
        .getState()
        .panes.filter(
          (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild",
        ),
    ).toEqual([grandchild]);
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);

    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(parent?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);

    // An explicit new nested route wins even when it shares the same owner.
    act(() => {
      window.history.pushState({}, "", "/s/local:next-child");
      installLocationForRoute("local:next-child");
      const resource = navigationStore.getState().resources.get(keyID({ kind: "location", ref: "local:next-child" }));
      installLocation({
        ...(resource?.data as NavigationSessionLocation),
        top_level: false,
        top_level_ref: "local:owner",
      });
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:next-child")?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(owner);
    expect(workspaceStore.getState().focusedPaneId).not.toBe(grandchild?.id);
  },
);

test.each([
  ["local:owner", "local:other-owner"],
  ["local:child", "local:other-owner"],
  ["local:child", "local:owner"],
])("settled route %s rejects an unrelated focused transcript context under %s", async (routeRef, parentRef) => {
  window.history.pushState({}, "", `/s/${routeRef}`);
  installLocationForRoute(routeRef);
  render(<AppShell client={navClient()} />);
  await screen.findAllByText(/loading transcript/i);
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
  let unrelated!: string;
  act(() => {
    unrelated = workspaceStore
      .getState()
      .openPane("transcript", { ref: "local:unrelated", parentRef }, { slot: "secondary" });
    workspaceStore.getState().focusPane(unrelated);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
  expect(workspaceStore.getState().focusedPaneId).not.toBe(unrelated);
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" });
});

test.each(["local:owner", "local:child"])(
  "Open next descendant retains mixed-chain context on route %s",
  async (routeRef) => {
    vi.stubGlobal("innerWidth", 390);
    installMobileViewport();
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    // Every conversation pane the funnel opens is deep-linkable, and the
    // phone's URL sync publishes each one's route, so each descendant needs a
    // location naming the owner as its top level.
    installLocationForRoute("local:child");
    installLocationForRoute("local:grandchild", "local:owner");
    installLocationForRoute("local:great-grandchild", "local:owner");
    const user = userEvent.setup();
    render(
      <>
        <AppShell client={navClient()} />
        {routeRef === "local:owner" && (
          <OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" label="Open child" />
        )}
        <OpenTranscriptButton transcriptRef="local:grandchild" parentRef="local:child" />
        <OpenTranscriptButton
          transcriptRef="local:great-grandchild"
          parentRef="local:grandchild"
          label="Open next descendant"
        />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor(routeRef)?.id));
    if (routeRef === "local:owner") {
      await user.click(screen.getByRole("button", { name: "Open child" }));
      expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id);
    }
    await user.click(screen.getByRole("button", { name: "Open conversation" }));
    const grandchild = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:grandchild");
    expect(grandchild).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id);
    const originalOwner = workspaceStore.getState().mainPane();
    expect(originalOwner).toMatchObject({ type: "session", params: { ref: "local:owner" } });
    await user.click(screen.getByRole("button", { name: "Open next descendant" }));
    const next = workspaceStore
      .getState()
      .panes.find(
        (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:great-grandchild",
      );
    expect(next).toBeDefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(next?.id);
    expect(workspaceStore.getState().mainPane()).toEqual(originalOwner);
    expect(next?.params).toEqual({ ref: "local:great-grandchild" });
    expect(paneFor("local:grandchild")).toEqual(grandchild);
    await user.click(screen.getByRole("button", { name: "Open next descendant" }));
    expect(
      workspaceStore
        .getState()
        .panes.filter(
          (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:great-grandchild",
        ),
    ).toEqual([next]);
    expect(workspaceStore.getState().focusedPaneId).toBe(next?.id);
    await user.click(screen.getByRole("button", { name: "Back" }));
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(grandchild?.id));
    expect(workspaceStore.getState().mainPane()).toEqual(originalOwner);
  },
);

test.each(["missing", "cycle"])(
  "Open next descendant preserves the workspace when %s context cannot resolve an owner",
  async (context) => {
    window.history.pushState({}, "", "/s/local:child");
    installLocationForRoute("local:child");
    render(
      <>
        <AppShell client={navClient()} />
        <OpenTranscriptButton transcriptRef="local:next" parentRef="local:detached" />
      </>,
    );
    await screen.findAllByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id));
    const originalMain = workspaceStore.getState().mainPane();
    act(() => {
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:detached", parentRef: "local:bridge" }, { slot: "secondary" });
      if (context === "cycle")
        workspaceStore
          .getState()
          .openPane("transcript", { ref: "local:bridge", parentRef: "local:detached" }, { slot: "secondary" });
      fireEvent.click(screen.getByRole("button", { name: "Open conversation" }));
    });
    // The detached/cyclic context has no loaded location, so the owner cannot
    // be proven. The workspace is preserved: the retained main session stays,
    // and the transcript opens beside it rather than promoting an unproven
    // parent that would discard the restored panes.
    await waitFor(() => expect(paneFor("local:next")).toBeDefined());
    expect(workspaceStore.getState().mainPane()).toEqual(originalMain);
    expect(paneFor("local:next")?.params).toEqual({ ref: "local:next" });
  },
);

function installSwitchableViewport(): (mobile: boolean) => void {
  let mobile = false;
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const mediaQuery = {
    media: "(max-width: 899px)",
    get matches() {
      return mobile;
    },
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.add(listener as (event: MediaQueryListEvent) => void);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.delete(listener as (event: MediaQueryListEvent) => void);
    },
  } as MediaQueryList;
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) =>
      query === mediaQuery.media
        ? mediaQuery
        : { matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} },
    ),
  );
  return (nextMobile: boolean) => {
    mobile = nextMobile;
    const event = { matches: mobile, media: mediaQuery.media } as MediaQueryListEvent;
    for (const listener of listeners) listener(event);
  };
}

// A /s/{ref} route can remain deferred until the AppWire location read says
// whether the ref is nested, and no read can resolve inside the first commit.
// StackHost fills an empty stack with welcome and publishes the focused pane's
// URL, which used to overwrite the address bar with "/" during exactly that
// beat: the deep link was gone before the location arrived, and no later
// evener/changed push could name it again.
test("mobile: a /s/{ref} deep link still opens once the tree lands, instead of being overwritten by welcome", async () => {
  navigationStore.setState({ mode: "v3" });
  installMobileViewport();

  window.history.pushState({}, "", "/s/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);

  // The mobile host settles on its welcome fallback while the location is
  // still unavailable, but must preserve the deep-link URL.
  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/s/local%3As1");

  act(() => installLocationForRoute("local:s1"));

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  // And the address bar now names it in paneToURL's own canonical form.
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3As1"));
});

test("crossing desktop → mobile → desktop preserves a focused panel and its return path", async () => {
  const setMobile = installSwitchableViewport();
  window.history.pushState({}, "", "/s/local:s1");
  installLocationForRoute("local:s1");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText(/loading transcript/i);

  let panelId!: string;
  act(() => {
    panelId = workspaceStore.getState().openPane("sessionDetails", { ref: "local:s1" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await screen.findByRole("status", { name: "Loading session panel" });
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));

  act(() => setMobile(true));
  expect(await screen.findByRole("status", { name: "Loading session panel" })).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  // The scaffold header is display:none below 900px - StackHost's top-bar
  // Back is the VISIBLE mobile return path, wired to the parent session for
  // panel panes.
  expect(screen.getByRole("button", { name: "Back" })).toBeTruthy();

  act(() => {
    setMobile(false);
    // Still the mobile host, after the event but before React commits it.
    // DockRegion's shared lazy payload mounts DockHost immediately at commit.
    expect(getDockviewApi()).toBeNull();
  });
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  expect(getDockviewApi()).not.toBeNull();

  act(() => setMobile(true));
  await screen.findByRole("status", { name: "Loading session panel" });
  await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).not.toBe(panelId));
  expect(
    workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId)?.type,
  ).toBe("session");
});

// --- kata 098n: a /thread share link is not rewritten into /s ------------

// A /thread/{ref} share link is a single-pane route: isSinglePaneRoute
// (singlePane.ts) keys the chrome-stripped layout off the PATHNAME, so the
// mode survives only as long as the address bar keeps naming it. On mobile
// StackHost publishes the focused pane's URL, and a session pane serializes
// back to /s/{ref} - so the share link used to be rewritten out from under
// the user the moment the routed pane took focus, silently turning
// single-pane mode off. Desktop never rewrites it (DockHost writes no URL at
// all), which is the behaviour this pins mobile to.
test("kata 098n: on mobile a /thread/{ref} share link keeps its URL and its single-pane chrome", async () => {
  installMobileViewport();
  window.history.pushState({}, "", "/thread/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);
  // Mobile never mounts RailHost, so there is no rail-owned navigation read;
  // the notification engine's baseline AppWire read is independent of that
  // chrome and already covers this path in production.
  await act(async () => {
    await Promise.resolve();
  });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  await waitFor(() => expect(window.location.pathname).toBe("/thread/local:s1"));
  expect(document.querySelector("[data-single-pane]")).not.toBeNull();
  expect(screen.queryByRole("button", { name: "Sessions" })).toBeNull();
});

// --- kata p5w9: one boot, one baseline AppWire navigation read -------------------------

// A desktop boot has two consumers of the same navigation snapshot:
// initNotifications()'s baseline and the rail. Both must share the typed
// AppWire read seam. Publishing ready with its matching metadata must not
// trigger an extra connection event for the reconnect subscriber.
//
// The notifications engine is a module singleton already initialized by
// AppShell.tsx's own import, so a REAL boot is modelled by resetting and
// re-initializing it here - and it is deliberately left initialized
// afterwards, exactly as module evaluation leaves it for every other test in
// this file.
test("desktop boot uses the typed AppWire navigation read seam", async () => {
  const client = navClient();
  resetNotificationsForTests();
  initNotifications();
  render(<AppShell client={client} />);

  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());
  await waitFor(() => expect(connectionStore.getState().serverInfo).toBeDefined());

  // evener/command/list is the command catalog's connection-driven read
  // (stores/commandCatalog.ts): a ready client loads it without waiting for
  // a palette open.
  // thread/read and thread/unsubscribe are the background Live roster owner's
  // additive membership reads (it discovers the complete Live section), which
  // run alongside the typed navigation reads this test guards.
  expect(
    client.calls.every(
      ({ method }) =>
        method === "evener/navigation/read" ||
        method === "evener/instance/list" ||
        method === "evener/command/list" ||
        method === "thread/read" ||
        method === "thread/unsubscribe",
    ),
  ).toBe(true);
  expect(client.calls).toContainEqual({
    method: "evener/navigation/read",
    params: { resource: "manifest", representationVersion: 3 },
  });
  // Required, not just allowed above: a regression that stops loading the
  // catalog on connect must fail this test, not slip past the allow-list.
  expect(client.calls).toContainEqual({ method: "evener/command/list", params: {} });
});

// FIX 1 (real-browser bug): Settings' Escape/close used to call
// workspaceStore.closePane directly, which replaces the main pane with
// welcome IN THE STORE but leaves window.location.pathname on /settings/* -
// so AppShell's own route-reconciliation effect (routePlacementIsApplied /
// openRouteAsPane above) sees the URL still asking for settings, decides
// placement has drifted, and reinstates the settings pane right back into
// main. A pane-store-only assertion (Settings.test.tsx's own unit tests)
// can't see this at all, because nothing there mounts AppShell's
// reconciliation effect - hence this integration test through the real
// shell, matching needsYouCycle.ts's own documented rationale for routing
// exits through navigate()/paneToURL rather than poking the store.
test("Escape closes Settings even when focus never entered the pane (gear-click leaves focus in the rail)", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  // The real-browser repro: focus sits outside the settings subtree (the
  // gear button / body), so a React onKeyDown scoped to the pane's div
  // never fires. The exit must listen at the document level.
  fireEvent.keyDown(document.body, { key: "Escape" });

  await screen.findByText("No session open");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
});

test("Escape in Settings exits to welcome and the URL stays there, not reinstated by route reconciliation", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByRole("navigation", { name: "Settings sections" });

  fireEvent.keyDown(screen.getByRole("button", { name: "Appearance" }), { key: "Escape" });

  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
  // Give the reconciliation effect a chance to run again (it fires off the
  // pathname/tree/workspacePanes deps below) and confirm it did NOT undo it.
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.type).not.toBe("settings");
  });
  expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
});

// --- Alt+ArrowLeft/Right session-pane cycling (webui-keybindings-p3 Task 1)
//
// AppShell registers session.next/session.previous against the keybindings
// registry (desktop only - the rail.toggle inertness pattern); the actions
// drive workspaceStore.focusPane through shell/sessionCycle.ts. These tests
// pin the WIRING (real dispatcher, real defaults, real shell); the cycling
// order/wrap/no-op semantics themselves are sessionCycle.test.ts's.

test("Alt+ArrowRight/Left cycle the open session panes through the shell, wrapping", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  let b = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    b = workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);

  // Wrap: the last session pane's next is the first.
  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);

  await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(b);
});

test("Alt+Arrow cycling is a no-op with a single session pane open", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let only = "";
  act(() => {
    only = workspaceStore.getState().openPane("session", { ref: "local:cycle-only" });
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(1));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
  await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(only);
});

test("Alt+Arrow cycling is suppressed from an editable target", async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  try {
    await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
    expect(workspaceStore.getState().focusedPaneId).toBe(a);
  } finally {
    input.remove();
  }
});

test("mobile: Alt+Arrow cycling registers nothing and is inert", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");

  let a = "";
  act(() => {
    a = workspaceStore.getState().openPane("session", { ref: "local:cycle-a" });
    workspaceStore.getState().openPane("session", { ref: "local:cycle-b" });
    workspaceStore.getState().focusPane(a);
  });
  await waitFor(() => expect(workspaceStore.getState().panes).toHaveLength(2));

  await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  vi.unstubAllGlobals();
});

// --- Alt+Shift+ArrowRight/Left live-session navigation
//
// AppShell registers session.liveNext/session.livePrevious against the
// keybindings registry; the actions navigate across the rail's live section
// in server order through shell/rail/liveSessionCycle.ts. These tests pin
// the WIRING (real dispatcher, real defaults, real shell); the cycling
// order/wrap/no-op semantics themselves are liveSessionCycle.test.ts's.

const LIVE_CYCLE_A: NavigationSessionSummary = {
  ref: "local:live-a",
  host_id: "local",
  session_id: "live-a",
  title: "Live A",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};
const LIVE_CYCLE_B: NavigationSessionSummary = {
  ref: "local:live-b",
  host_id: "local",
  session_id: "live-b",
  title: "Live B",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};

// navClient, but with a two-row live section: everything else delegates to
// the shared navigationRead.
function navClientWithLive(sessions: NavigationSessionSummary[]): FakeClient {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      return wireSnapshot(params, { sessions, remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

test("Alt+Shift+ArrowRight/Left navigate across the rail's live sessions, wrapping", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-b");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);

  // Wrap: the last live session's next is the first.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-b");
  expect((window.history.state as Record<string, unknown> | null)?.__evenerAttention).toBeUndefined();
  expect(workspaceStore.getState().panes.some((pane) => pane.slot === "parked")).toBe(false);
});

test("Alt+Shift+Arrow live-session navigation is suppressed from an editable target", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  try {
    await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
    expect(window.location.pathname).toBe("/s/local%3Alive-a");
  } finally {
    input.remove();
  }
});

test("mobile: Alt+Shift+Arrow live-session navigation registers nothing and is inert", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  vi.unstubAllGlobals();
});

// The rail's live section paginates (limit 50 per page). At the last LOADED
// live session, next must demand-load the following page and continue into
// it — wrapping over the loaded subset would skip every live session behind
// the remaining count (roborev PR #1044 finding 1).
test("Alt+Shift+ArrowRight at the last loaded live session demand-loads the next page instead of wrapping", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      if ((params.offset ?? 0) === 0) {
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
      }
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => {
    expect(window.location.pathname).toBe("/s/local%3Alive-b");
  });
});

// Round-2 demand-load lifecycle: the pending-page cache must not brick after
// a failed load, and an in-flight demand must go inert when a newer press or
// an open palette supersedes it (roborev PR #1044 round-2 mediums 1-2).

// A two-page live section whose page-two request is scriptable per test.
function navClientWithDeferredLivePageTwo(script: {
  onPageTwo: (params: NavigationReadParams) => NavigationReadResponse | Promise<NavigationReadResponse>;
}): FakeClient {
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
        }
        return script.onPageTwo(params);
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  return client;
}

test("live-next demand-load retries after the page request fails", async () => {
  let pageTwoAttempts = 0;
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) => {
      pageTwoAttempts++;
      // The background Live roster owner discovers the complete roster and
      // spends the first attempt; the chord's first demand spends the second,
      // and its retry is the third.
      if (pageTwoAttempts <= 2) throw new Error("transient read failure");
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    },
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");
  // Page two was already read once (and failed) by background discovery.
  await waitFor(() => expect(pageTwoAttempts).toBe(1));

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The first demand re-requests the failed page and fails again; the view
  // stays and no navigation happens.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(pageTwoAttempts).toBe(2));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  // A cached "already demanded" entry must not brick the retry.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(pageTwoAttempts).toBe(3));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

test("an in-flight live demand-load goes inert when a newer live-nav press supersedes it", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          // Two loaded rows and one more page behind them, so the second press
          // below (previous from the last loaded row) is a DIRECT mid-list
          // step, not a demand - a previous from the FIRST loaded row would
          // itself demand the same in-flight page (round-5 tail rule).
          return wireSnapshot(
            params,
            { sessions: [LIVE_CYCLE_A, LIVE_CYCLE_B], remaining: 1, truncated: false },
            '"test"',
          );
        }
        return new Promise<NavigationReadResponse>((resolve) => {
          deferred.params = params;
          deferred.resolve = resolve;
        });
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live B");

  await user.click(rail().getByText("Live B"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-b" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // An ordinary press (direct mid-list step to A) is newer intent: it must
  // supersede the demand.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// With NO live rows loaded (the initial section read failed or is still in
// flight), selectSectionRemaining is 0, so the boundary rule alone would
// leave the chord inert until an external refresh. The manifest's live count
// is the authority there: a press re-requests page zero and continues into
// it (roborev PR #1044 round 3; the needs-you handler's manifest-count
// bootstrap).
test("live-next with an unloaded live section re-requests page zero when the manifest reports live sessions", async () => {
  let liveReads = 0;
  let allowLive = false;
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      liveReads++;
      // Fail every automatic attempt - the manifest hydration's read and the
      // background roster owner's discovery - so the section stays unloaded
      // until the chord's own demand can be answered.
      if (!allowLive) throw new Error("transient initial failure");
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  // The initial hydration read failed: no live rows, nothing focused.
  await screen.findByText("No session open");
  expect(rail().queryByText("Live A")).toBeNull();
  const unloadedReads = liveReads;

  allowLive = true;
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift>}{/Alt}");
  await waitFor(() => expect(liveReads).toBeGreaterThan(unloadedReads));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
});

// #4958: once a visible live page has settled with no remaining rows, its
// own answer owns completion. The manifest's live count refreshes on its own
// schedule and may still report rows; neither direction may spend a page read
// on that stale count.
test("live chord does not demand a page once a settled live page owns completion", async () => {
  const liveReads: number[] = [];
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 2 } } },
        '"test"',
      );
    }
    if (params.resource === "section" && params.section === "live") {
      liveReads.push(params.offset ?? 0);
      return wireSnapshot(params, { sessions: [], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  // The chord reads the store's current action, so a wrapper counts demands
  // without depending on whether a settled page answers from cache.
  const demands: Array<{ offset?: number }> = [];
  const loadSection = navigationStore.getState().loadSection;
  onTestFinished(() => navigationStore.setState({ loadSection }));
  navigationStore.setState({
    loadSection: (section, offset, limit) => {
      if (section === "live") demands.push({ offset });
      return loadSection(section, offset, limit);
    },
  });

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => {
    const page = [...navigationStore.getState().resources.values()].find(
      (resource) => resource.key.kind === "section" && resource.key.section === "live",
    );
    expect(page?.loading).toBe(false);
    expect(page?.data).not.toBeNull();
  });
  expect(liveReads.length).toBeGreaterThan(0);
  const demandedBefore = demands.length;

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  expect(demands.length).toBe(demandedBefore);
  expect(window.location.pathname).toBe("/");
});

// Round 4: the previous direction must reach the live TAIL, not the first
// newly loaded row (roborev PR #1044 round-4 medium 1).
test("live-previous with an unloaded live section demand-loads to the tail", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "manifest") {
        return wireSnapshot(
          params,
          { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 2 } } },
          '"test"',
        );
      }
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return new Promise<NavigationReadResponse>((resolve) => {
            deferred.params = params;
            deferred.resolve = resolve;
          });
        }
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  // Page zero's read (the manifest hydration's) is in flight, deferred.
  await waitFor(() => expect(deferred.params).not.toBeNull());

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // Page zero resolves with one row and one more page behind it: the
  // previous direction must keep loading to the tail, not land on A.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-zero request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 4: a second same-direction press while the demanded page is still in
// flight must not invalidate the demand it is waiting on (roborev PR #1044
// round-4 medium 2).
test("rapid live-next presses at the boundary still navigate when the demand lands", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"');
        }
        return new Promise<NavigationReadResponse>((resolve) => {
          deferred.params = params;
          deferred.resolve = resolve;
        });
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Two presses before the demanded page lands: one demand, kept live.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(deferred.params).not.toBeNull());
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");

  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 5: with the live section PARTIALLY loaded, a previous press that
// would wrap (from the first loaded row, or from a non-live session) must
// demand-load to the true tail rather than landing on the last loaded row
// (roborev PR #1044 round-5 medium 1).
test("live-previous wrapping with more pages on the server demand-loads to the tail", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 3 } } },
        '"test"',
      );
    }
    if (params.resource === "section" && params.section === "live") {
      if ((params.offset ?? 0) === 0) {
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 2, truncated: false }, '"test"');
      }
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  // Focus the FIRST loaded live session; previous from here wraps.
  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

test("an in-flight live demand-load goes inert while the palette is open", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  act(() => {
    paletteStore.setState({ open: true, query: "" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 6: the demand dedupe keyed the page alone, so an opposite-direction
// press while that page was in flight returned early WITHOUT bumping the
// intent counter - the stale continuation still owned the navigation. With no
// live rows loaded, Previous then Next while page zero loads must leave the
// Next intent in charge: the first returned row, not the previous direction's
// tail (roborev PR #1044 round-6 medium).
test("an opposite-direction press while a demanded page is in flight supersedes the prior intent", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = new FakeClient("ready");
  client.on(
    "evener/navigation/read",
    (params: NavigationReadParams): NavigationReadResponse | Promise<NavigationReadResponse> => {
      if (params.resource === "manifest") {
        return wireSnapshot(
          params,
          { ...EMPTY_NAV_RESPONSE, sections: { ...EMPTY_NAV_RESPONSE.sections, live: { count: 3 } } },
          '"test"',
        );
      }
      if (params.resource === "section" && params.section === "live") {
        if ((params.offset ?? 0) === 0) {
          return new Promise<NavigationReadResponse>((resolve) => {
            deferred.params = params;
            deferred.resolve = resolve;
          });
        }
        return wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
      }
      return navigationRead(params);
    },
  );
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));

  const user = userEvent.setup();
  render(<AppShell client={client} />);
  // Page zero (the manifest hydration's) is in flight, deferred.
  await screen.findByText("No session open");
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // Previous first: with nothing loaded it demands page zero toward the tail.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // Then the user flips to Next: the newest intent must own the navigation.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");

  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-zero request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_A], remaining: 1, truncated: false }, '"test"'));
  });
  // Next from nothing opens the FIRST live row; the previous direction's
  // continuation must not run past it toward the tail.
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
});

// Round 7: the demand-load continuation never re-checked the navigation
// client generation. A reconnect bumps clientGenerationID while the demanded
// page is in flight; the handler's press-time reset only covers NEW presses,
// so the old promise resolving with retained (provisional) rows navigated
// into the previous generation (roborev PR #1044 round-7 medium 2).
test("an in-flight live demand-load goes inert when the client generation changes", async () => {
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}"); // demand in flight
  await waitFor(() => expect(deferred.params).not.toBeNull());

  // A reconnect boots a new generation while the demand is still in flight.
  act(() => {
    navigationStore.setState({ clientGenerationID: "generation_reconnected" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  // The stale-generation continuation must not navigate.
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 8, medium 3: live navigation must FOCUS the target session even
// when the URL already matches - a secondary panel or another pane can hold
// focus while the route names the session (roborev PR #1044 round-8 medium 3).
test("live-next focuses the session pane even when the URL already matches", async () => {
  // Two live rows on one page: A, B.
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    if (params.resource === "section" && params.section === "live") {
      return wireSnapshot(params, { sessions: [LIVE_CYCLE_A, LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"');
    }
    return navigationRead(params);
  });
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v4",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0, readVersions: [3] },
  }));
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Focus a secondary session panel over the same session: the URL stays
  // /s/local:live-a while the focused pane is the panel.
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-a" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/));

  // Navigate to B so the URL differs from the wrap target, then wrap
  // previous. But focus first: the route's own reconciliation would fight
  // the panel focus, so drive the navigation by CLICKING the row (focus
  // follows), then re-focus the panel.
  await user.click(rail().getByText("Live B"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-b" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
  // The URL now matches B while the PANEL holds focus.
  expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/);

  // The URL-equal press: previous from the panel. focusedSessionRef() is
  // null (the panel is not a session pane), so previous wraps to the list
  // HEAD... which is A, not the panel's B. Hmm - the URL changes to A. The
  // decisive URL-equal case is NEXT from B's panel: null current targets
  // the FIRST live row (A), and the URL is ALREADY /s/local%3Alive-b only
  // if... no. The decisive case as documented: pressing next with the URL
  // already on the TARGET. Target A, URL /s/local%3Alive-b -> differs.
  // Target B, URL on B: previous with current=null targets the LAST row
  // (B): URL EQUAL. That press must still refocus the session pane.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  // The press must refocus the session pane (the navigation the user asked
  // for), even though the URL did not move - it already named B.
  const focused = workspaceStore.getState().panes.find((p) => p.id === workspaceStore.getState().focusedPaneId);
  expect(focused?.type).toBe("session");
});

// The same URL-equal press against a CASCADE main: the placement seam now
// preserves a cascade whose session source is the pressed ref (and must keep
// preserving it - and never steal focus on its own, or a restored layout's
// saved focus would not survive boot placement), but the live chord's own
// contract is that the press always ends with the session pane focused. A
// preserved cascade keeps the pane id, so the guarantee has to reach it too.
test("live-previous focuses a preserved cascade main when the URL already matches", async () => {
  const user = userEvent.setup();
  render(<AppShell client={navClientWithLive([LIVE_CYCLE_A, LIVE_CYCLE_B])} />);
  await rail().findByText("Live B");

  // The URL names B with B's plain session pane in main, then the zoom
  // system's in-place retype turns that pane into a cascade on the same
  // source - the exact main a saved layout restores.
  await user.click(rail().getByText("Live B"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-b" });
  });
  act(() => {
    retypeMainToCascade("local:live-b", "local:live-child");
  });

  // A secondary panel holds focus while the URL still names B.
  act(() => {
    const panelId = workspaceStore.getState().openPane("sessionTasks", { ref: "local:live-b" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(panelId);
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toMatch(/^pane_sessionTasks_/));

  // URL-equal press: previous from the panel targets the LAST live row (B),
  // whose URL the route already holds. The cascade must survive the press
  // AND take focus.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  const state = workspaceStore.getState();
  expect(state.mainPane()?.type).toBe("sessionZoom");
  expect(state.focusedPaneId).toBe(state.mainPane()?.id);
});

// Round 8, low 1: a COMPLETED demand's dedupe key must leave the in-flight
// set. After an invalidation (not a generation change) re-stales the live
// pages, the same page+direction demand must be issuable again instead of
// being silently swallowed by the stale key (roborev PR #1044 round-8 low 1).
test("a live demand can be re-issued after an invalidation restales the pages", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // First demand: page two loads and navigates to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));

  // Go back to A, then invalidate the live section (same generation): the
  // loaded pages go stale, so the boundary press must re-demand page two.
  await user.keyboard("{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  const beforeDemandPress = pageTwoLoads; // count BEFORE the invalidation's own refresh
  await act(async () => {
    client.emitNotification({
      method: "evener/navigation/invalidated",
      params: {
        generationId: "generation_test",
        sequence: 1,
        targets: [{ kind: "section", section: "live" }],
      },
    });
  });
  // The invalidation re-requests the stale live pages (the revalidator's
  // own refresh) - wait for that to settle before the boundary press.
  await waitFor(() => expect(pageTwoLoads).toBeGreaterThan(0));
  await waitFor(() => expect(pageTwoLoads).toBeGreaterThanOrEqual(beforeDemandPress));

  // Press next at the same boundary: a fresh demand for the same
  // page+direction must be issued.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(beforeDemandPress + 1));
  const resolve2 = deferred.resolve;
  const params2 = deferred.params;
  if (!params2 || !resolve2) throw new Error("second page-two request was not issued");
  await act(async () => {
    resolve2(wireSnapshot(params2, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 9, medium 3: the inert early return must clear the in-flight key so
// the set keeps tracking in-flight demands only. A same-key re-demand is not
// reachable end-to-end (a page that resolves with data always advances the
// next-demand offset past itself, and an error/abort resolves through the
// already-deleting error path), so this pins the reachable lifecycle instead:
// a palette-inert completion leaves nothing sticky - after the palette
// closes, the same chord still navigates, here via the page the inert
// completion loaded (roborev PR #1044 round-9 medium 3).
test("a demand that completes inert does not leave the live chord stuck", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // First demand: the palette is open when it completes, so it goes inert
  // (no navigation) even though the page resolved with B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  act(() => {
    paletteStore.setState({ open: true, query: "" });
  });
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
  act(() => {
    paletteStore.setState({ open: false, query: "" });
  });

  // After the palette closes, the same chord must still work - the demand's
  // page is loaded, so this is the direct step to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 13, low 5: the demand's completion never re-checks editable focus.
// The chord is suppressed while an editable target has focus at PRESS time,
// but a demand pressed from a non-editable target still navigates when it
// completes even if the user focused the composer mid-flight - the keydown
// was swallowed then, so the navigation surprises a typing user. The
// completion must go inert (delete the in-flight key, no navigation) when
// focus is editable (roborev PR #1044 round-13 low 5).
test("a demand that completes while the composer has focus goes inert", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The press comes from a non-editable target: the demand issues.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  // The user focuses an editable surface (the composer) while the demand is
  // in flight. The pane's real composer mounts lazily in this fixture, so
  // the test focuses a plain input: the completion guard checks
  // isEditableTarget(document.activeElement), which any editable element
  // satisfies identically.
  const editable = document.createElement("input");
  document.body.appendChild(editable);
  await act(async () => {
    editable.focus();
  });

  // The completion must not navigate: focus is editable.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(pageTwoLoads).toBe(1));
  expect(window.location.pathname).toBe("/s/local%3Alive-a");
});

// Round 9, medium 4: leaving the session and returning before a demand
// resolves restores an identical focused pane and session ref, so the
// press-time guards alone read as "never left" and the stale completion
// navigates under the returned user. The route epoch must invalidate the
// demand instead (roborev PR #1044 round-9 medium 4).
test("an in-flight live demand-load goes inert after leaving the session and returning", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });
  const paneAtPress = workspaceStore.getState().focusedPaneId;
  // The route-placement effect re-focuses the session pane on the return
  // leg only when the location resource is present, so the test installs
  // it exactly as the app's own location lookup would have.
  act(() => installLocationForRoute("local:live-a"));

  // Demand in flight from A (the last loaded live row). The leave/return
  // must be a round trip the OLD guards cannot see: going to "/" opens the
  // welcome pane in SECONDARY (openPane never displaces a non-welcome
  // main), and returning re-matches the still-open session pane, so the
  // pane id, focused ref, and pathname are all identical to press time -
  // only the route epoch knows the user left at all.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  await act(async () => {
    navigate("/");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/"));
  await act(async () => {
    navigate("/s/local%3Alive-a");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  await waitFor(() => {
    expect(workspaceStore.getState().focusedPaneId).toBe(paneAtPress);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // The stale completion must NOT navigate: the demand left with the route
  // it was pressed on, and the epoch has since moved.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  expect(window.location.pathname).toBe("/s/local%3Alive-a");

  // And the chord is not permanently bricked by the inert completion: page
  // two is loaded now, so the same press takes the direct step to B.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// Round 11, medium 1: the leave-and-return round trip also stranding a
// SECOND press pressed while the demand is still in flight. The dedupe must
// not treat the in-flight demand as live: its recorded owner's guards are
// stale, so the newer press ADOPTS the pending load by rebinding fresh
// guards, and the load's completion navigates under the returned user.
// Under the old set-based dedupe the second press silently deduped against
// the dead demand, whose completion then went inert - the keypress produced
// no navigation at all (roborev PR #1044 round-11 medium 1).
test("a second press adopts an in-flight demand whose guards went stale", async () => {
  let pageTwoLoads = 0;
  const deferred: { params: NavigationReadParams | null; resolve: ((r: NavigationReadResponse) => void) | null } = {
    params: null,
    resolve: null,
  };
  const client = navClientWithDeferredLivePageTwo({
    onPageTwo: (params) =>
      new Promise<NavigationReadResponse>((resolve) => {
        pageTwoLoads++;
        deferred.params = params;
        deferred.resolve = resolve;
      }),
  });
  const user = userEvent.setup();
  render(<AppShell client={client} />);
  await rail().findByText("Live A");

  await user.click(rail().getByText("Live A"));
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });
  const paneAtPress = workspaceStore.getState().focusedPaneId;
  // The route-placement effect re-focuses the session pane on the return
  // leg only when the location resource is present, so the test installs it
  // exactly as the app's own location lookup would have.
  act(() => installLocationForRoute("local:live-a"));

  // Press one: demand in flight from A (the last loaded live row).
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  await waitFor(() => expect(pageTwoLoads).toBe(1));

  // Leave and return: the pane id, focused ref, and pathname all read
  // identical to press time - only the route epoch knows the user left, so
  // the recorded owner's guards are stale.
  await act(async () => {
    navigate("/");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/"));
  await act(async () => {
    navigate("/s/local%3Alive-a");
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-a"));
  await waitFor(() => {
    expect(workspaceStore.getState().focusedPaneId).toBe(paneAtPress);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:live-a" });
  });

  // Press two at the same boundary: it adopts the in-flight load. The
  // revalidator dedupes the concurrent read of the same page, so the
  // adoption issues no second request.
  await user.keyboard("{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}");
  expect(pageTwoLoads).toBe(1);

  // The load lands: the displaced owner no-ops on its map-identity check
  // and the adopter's completion navigates to the newly loaded row.
  const params = deferred.params;
  const resolve = deferred.resolve;
  if (!params || !resolve) throw new Error("page-two request was not issued");
  await act(async () => {
    resolve(wireSnapshot(params, { sessions: [LIVE_CYCLE_B], remaining: 0, truncated: false }, '"test"'));
  });
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Alive-b"));
});

// M1 (round 6): the settings selection must be synchronized with the route at the
// popstate itself, before React mounts the settings pane. Otherwise a Back/Forward
// into a host-scoped settings URL mounts that pane on the previous (local)
// selection and renders - and reads - for a host the route does not name.
test("a popstate into a host-scoped settings URL selects the host before the pane mounts", async () => {
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(settingsHostStore.getState().host).toBe("local");

  // Read the selection during the popstate dispatch: after the shell's own route
  // listener has run, but before React can mount the settings pane.
  let hostAtDispatch: string | null = null;
  const probe = () => {
    hostAtDispatch = settingsHostStore.getState().host;
  };
  window.addEventListener("popstate", probe);
  try {
    act(() => {
      window.history.pushState({}, "", "/settings/credentials?host=beta");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
  } finally {
    window.removeEventListener("popstate", probe);
  }

  expect(hostAtDispatch).toBe("beta");
  // The pane mounts for beta and settles on the registry's answer (this client
  // scripts none, so the read fails) before the test ends.
  expect(await screen.findByText("Couldn't check beta's registration")).toBeTruthy();
});

test.each([
  { retained: false, source: "main" },
  { retained: true, source: "main" },
  { retained: false, source: "nested" },
])(
  "desktop Activity drill keeps grandchild focus from $source with retained ref-only observer=$retained",
  async ({ retained, source }) => {
    onTestFinished(installJobLogRangeGeometry());
    const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get: () => 500 });
    onTestFinished(() => {
      if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
      else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
    });
    vi.stubGlobal("innerWidth", 1280);
    const routeRef = source === "nested" ? "local:child" : "local:owner";
    window.history.pushState({}, "", `/s/${routeRef}`);
    installLocationForRoute(routeRef);
    resetActivitySidebarStoreForTests();
    const client = navClient();
    if (source === "nested")
      client.on("evener/navigation/read", (params) =>
        params.resource === "location" && params.ref === "local:child"
          ? wireSnapshot(
              params,
              {
                ref: "local:child",
                top_level_ref: "local:owner",
                top_level: false,
                session: { ...TREE_SESSION, ref: "local:child", session_id: "sess_local:child", kind: "subagent" },
              },
              '"nested"',
            )
          : navigationRead(params),
      );
    const context = (ref: string) => ({
      ...activityContext(ref),
      sessionId: `sess_${ref}`,
      rootRef: "local:owner",
      ...(ref !== "local:owner" ? { parentRef: ref === "local:child" ? "local:owner" : "local:child" } : {}),
      ancestors:
        ref === "local:owner"
          ? []
          : [
              { ref: "local:owner", sessionId: "sess_local:owner", title: "Root" },
              ...(ref === "local:grandchild"
                ? [{ ref: "local:child", sessionId: "sess_local:child", title: "Observer" }]
                : []),
            ],
    });
    client.on("thread/read", ({ ref, includeTurns, requestGeneration }) => {
      if (!ref) throw new Error("thread ref required");
      return {
        requestGeneration,
        thread: {
          ...threadStartResponse(ref).thread,
          name: ref,
          turns:
            includeTurns === false
              ? []
              : [
                  {
                    id: "settled",
                    status: "completed",
                    itemsView: "full",
                    items: [
                      {
                        id: "report",
                        turnId: "settled",
                        type: "agentMessage",
                        text: `report-${ref}`,
                        status: "completed",
                      },
                    ],
                  },
                ],
        },
      };
    });
    client.on("evener/thread/activity/read", (params) => ({
      ...answerActivityRead(params),
      context: context(params.ref),
    }));
    client.on("evener/thread/delegates/list", ({ ref }) => ({
      context: context(ref),
      scope: "session",
      delegates:
        ref === "local:grandchild"
          ? []
          : [
              activityDelegate({
                ownerRef: ref,
                rootRef: "local:owner",
                childRef: ref === "local:owner" ? "local:child" : "local:grandchild",
                name: ref === "local:owner" ? "observer" : "grandchild",
                terminal: true,
                lifecycle: "idle",
                phase: "done",
                status: "completed",
              }),
            ],
      page: { complete: true, issues: [] },
    }));
    client.on("evener/thread/jobs/list", ({ ref }) => ({
      context: context(ref),
      scope: "session",
      jobs: [],
      page: { complete: true, issues: [] },
    }));
    client.on("evener/jobs/get", () => {
      throw new Error("retained metadata unavailable");
    });
    client.on("evener/jobs/output", () => ({
      data: {
        offsetBytes: 0,
        bytesReturned: 23,
        totalBytes: 23,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "retained-history-output",
      },
    }));
    const user = userEvent.setup();
    let historyPane: string | undefined;
    // #4187: this journey flaked under heavy load and the failing assertion was
    // never captured, so a recurrence must name the state it saw: which pane
    // held focus and the transitions that moved it, what the retained observer
    // pane and the source conversation held, and the route, workspace and
    // rendered cascade every assertion reads. `describeFailure` starts as a
    // placeholder and is bound to the real report once the journey's identities
    // exist; the catch below appends it to whatever fails - an assertion or a
    // waiting find - so a red run reports its state instead of only
    // "expected 'pane_x' to be 'pane_y'".
    let describeFailure = (): string => "the drill failed before it captured its state";
    // A pane's identity for a failure report: id, type, slot and ref, so the
    // node that held focus is named, not just its opaque id. Defined before the
    // recorder so a transition can name the pane on each side of the move.
    const describePane = (pane: OpenPaneRecord | undefined): string =>
      pane === undefined ? "none" : `${pane.id}(${pane.type},${pane.slot},ref=${refParam(pane.params) ?? "-"})`;
    const describeFocused = (panes: readonly OpenPaneRecord[], id: string | null): string => {
      if (id === null) return "none";
      const pane = panes.find((candidate) => candidate.id === id);
      // A pane that is already gone still names itself, so a focus move onto a
      // closed pane is not reported as a bare "none".
      return pane === undefined ? `${id}(gone)` : describePane(pane);
    };
    const focusHistory: string[] = [];
    onTestFinished(
      workspaceStore.subscribe((state, previous) => {
        if (state.focusedPaneId !== previous.focusedPaneId)
          focusHistory.push(
            `${describeFocused(previous.panes, previous.focusedPaneId)}->${describeFocused(state.panes, state.focusedPaneId)}`,
          );
      }),
    );
    try {
      render(<AppShell client={client} />);
      await screen.findByRole("heading", { name: "local:owner" });
      await screen.findByText("report-local:owner");
      await waitFor(() => expect(getDockviewApi()).not.toBeNull());
      if (source === "nested") await screen.findByRole("heading", { name: "local:child" });
      const sourceId =
        source === "transcript"
          ? await act(async () =>
              workspaceStore
                .getState()
                .openPane("transcript", { ref: "local:owner", parentRef: "local:owner" }, { slot: "secondary" }),
            )
          : workspaceStore.getState().focusedPaneId;
      const sourcePane = () => workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
      const original = sourcePane();
      if (!original) throw new Error("Missing original Activity source");
      const lifetime = conversationPaneLifetime(original);
      let inspectorId = "";
      const inspectorPane = () => workspaceStore.getState().panes.find((pane) => pane.id === inspectorId);
      // The retained ref-only observer pane this journey drills past: an
      // ordinary session pane for the child, beside the cascade.
      const isRefOnlyChild = (pane: OpenPaneRecord): boolean =>
        pane.type === "session" && pane.id !== sourceId && refParam(pane.params) === "local:child";
      const renderedScopeRefs = (testId: string): string =>
        screen
          .queryAllByTestId(testId)
          .map((node) => node.getAttribute("data-scope-ref") ?? "-")
          .join(" ");
      const drillState = (): string => {
        const workspace = workspaceStore.getState();
        return [
          `path=${window.location.pathname}`,
          `focus=${describeFocused(workspace.panes, workspace.focusedPaneId)}`,
          `dockviewActive=${getDockviewApi()?.activePanel?.id ?? "none"}`,
          `inspector=${inspectorId === "" ? "unset" : describeFocused(workspace.panes, inspectorId)}`,
          `source=${describeFocused(workspace.panes, sourceId)}`,
          `sourceIsOriginal=${String(sourcePane() === original)}`,
          `sourceLifetimeAlive=${String(conversationPaneLifetime(original).alive)}`,
          `retainedObserver=${describePane(workspace.panes.find(isRefOnlyChild))}`,
          `retained=${retained}`,
          `case=${source}`,
          `columns=[${renderedScopeRefs("cascade-column")}]`,
          `spines=[${renderedScopeRefs("cascade-spine")}]`,
          `panes=[${workspace.panes.map(describePane).join(" ")}]`,
          `focusHistory=[${focusHistory.join(" ")}]`,
        ].join(" ");
      };
      describeFailure = drillState;
      act(() => {
        historyPane = workspaceStore
          .getState()
          .openPane("transcript", { ref: "job:history", parentRef: "local:owner" }, { slot: "secondary" });
        workspaceStore.getState().focusPane(sourceId ?? "");
      });
      if (source === "main") await screen.findByText("retained-history-output");
      expect(workspaceStore.getState().panes.some((pane) => pane.id === historyPane)).toBe(true);
      if (retained)
        act(() => {
          workspaceStore.getState().openPane("session", { ref: "local:child" }, { slot: "secondary" });
          workspaceStore.getState().focusPane(sourceId ?? "");
        });
      act(() => activitySidebarStore.getState().openWith("agents"));
      const sidebar = within(screen.getByTestId("activity-sidebar"));
      await sidebar.findByRole("button", { name: "Inactive subagents (1)" });
      const first = source === "nested" ? /grandchild/ : /observer/;
      if (!sidebar.queryByRole("button", { name: first }))
        await user.click(sidebar.getByRole("button", { name: "Inactive subagents (1)" }));
      const observer = await sidebar.findByRole("button", { name: first });
      await act(async () => fireEvent.click(observer));
      await waitFor(() => {
        const pane = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
        expect(pane?.type).toBe("sessionZoom");
        expect(pane?.slot).toBe("secondary");
        expect(pane?.id).not.toBe(sourceId);
        inspectorId = pane?.id ?? "";
        expect((pane?.params as { ref?: string })?.ref).toBe(source === "nested" ? "local:grandchild" : "local:child");
        expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
        expect(sourcePane()).toBe(original);
      });
      expect(window.location.pathname).toBe(`/s/${routeRef}`);
      if (source !== "nested") {
        act(() => activitySidebarStore.getState().openWith("agents"));
        await sidebar.findByRole("button", { name: "Inactive subagents (1)" });
        if (!sidebar.queryByRole("button", { name: /grandchild/ }))
          await user.click(sidebar.getByRole("button", { name: "Inactive subagents (1)" }));
        const grandchild = await sidebar.findByRole("button", { name: /grandchild/ });
        await act(async () => fireEvent.click(grandchild));
      }
      await screen.findByText("report-local:grandchild");
      expect(sourcePane()).toBe(original);
      expect(conversationPaneLifetime(original)).toBe(lifetime);
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect((inspectorPane()?.params as { ref?: string })?.ref).toBe("local:grandchild");
      // The origin conversation never renders in the pane. The nested source
      // (local:child) is the drilled leaf's immediate parent, so removing it
      // leaves the owner as the only spine and the leaf the only column. A
      // main or transcript origin (local:owner) is the path's root, so the
      // child stays the readable parent and no spine renders at all.
      const spines = screen.queryAllByTestId("cascade-spine");
      expect(spines.map((spine) => spine.getAttribute("data-scope-ref"))).toEqual(
        source === "nested" ? ["local:owner"] : [],
      );
      for (const spine of spines) expect(within(spine).queryByText("report-local:owner")).toBeNull();
      expect(screen.getAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"))).toEqual(
        source === "nested" ? ["local:grandchild"] : ["local:child", "local:grandchild"],
      );
      expect(workspaceStore.getState().panes.some((p) => p.id === historyPane)).toBe(true);
      expect(workspaceStore.getState().panes.filter(isRefOnlyChild)).toHaveLength(retained ? 1 : 0);
      // The cascade column's "Open conversation" link is gone - the column
      // embeds the conversation directly - so the click flow that opened a
      // session pane for the grandchild is deleted with it. The workspace and
      // focus guarantees live on in the crumb-pop assertions below.
      act(() => workspaceStore.getState().focusPane(inspectorId));
      // The origin conversation never renders, so its crumb is not offered
      // and the cascade cannot pop to it; the nearest rendered ancestor
      // crumb still pops. Nested: the origin is local:child, so local:owner
      // is the working pop target above it.
      if (source !== "nested") expect(sidebar.queryByRole("button", { name: "local:owner" })).toBeNull();
      const target = source === "nested" ? "local:owner" : "local:child";
      await act(async () => fireEvent.click(sidebar.getByRole("button", { name: target })));
      expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
      expect(sourcePane()).toBe(original);
      expect(inspectorPane()?.type).toBe("sessionZoom");
      expect((inspectorPane()?.params as { ref?: string })?.ref).toBe(target);
      const poppedColumn = screen.getByTestId("cascade-column");
      expect(await within(poppedColumn).findByText(`report-${target}`)).toBeTruthy();
      act(() => activitySidebarStore.getState().openWith("agents"));
      // Disclosure persistence belongs to the retention suite; this journey
      // verifies navigation and focus with the core sidebar's local disclosure.
      const disclosed = source === "nested" ? /observer/ : /grandchild/;
      if (!sidebar.queryByRole("button", { name: disclosed }))
        await user.click(await sidebar.findByRole("button", { name: "Inactive subagents (1)" }));
      expect(await sidebar.findByRole("button", { name: disclosed })).toBeTruthy();
      act(() => navigate("/s/local%3As1"));
      await waitFor(() => {
        expect(workspaceStore.getState().mainPane()?.type).toBe("session");
        expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:s1" });
      });
      expect(workspaceStore.getState().panes).toHaveLength(1);
    } catch (error) {
      // Any failure in the journey - an assertion or a waiting find - reports
      // the state above it, so a recurrence names the pane that took focus and
      // what the workspace held instead of only the bare matcher diff.
      if (error instanceof Error)
        error.message = `${error.message}\n\n[#4187 desktop Activity drill state] ${describeFailure()}`;
      throw error;
    } finally {
      act(() => resetActivitySidebarStoreForTests());
    }
  },
);

test.each([false, true])(
  "rail session activation opens live chrome with retained transcript and nested=%s",
  async (nested) => {
    const user = userEvent.setup();
    const client = navClient();
    client.on("thread/read", ({ ref }) => {
      if (!ref) throw new Error("session ref required");
      return { thread: threadStartResponse(ref).thread };
    });
    if (nested)
      client.on("evener/navigation/read", (params) =>
        params.resource === "location" && params.ref === "local:s1"
          ? wireSnapshot(
              params,
              {
                ref: "local:s1",
                top_level_ref: "local:owner",
                top_level: false,
                session: { ...TREE_SESSION, kind: "subagent" },
              },
              '"nested"',
            )
          : navigationRead(params),
      );
    window.history.pushState({}, "", "/s/local%3Aowner");
    render(<AppShell client={client} />);
    await rail().findByText("Session one");
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:owner" }));
    const owner = workspaceStore.getState().mainPane();
    const transcript = await act(async () =>
      workspaceStore
        .getState()
        .openPane("transcript", { ref: "local:s1", parentRef: "local:owner" }, { slot: "secondary" }),
    );
    await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(transcript));
    if (nested)
      act(() =>
        installLocation({
          generation_id: "generation_test",
          revision: 1,
          ref: "local:s1",
          top_level_ref: "local:owner",
          top_level: false,
          session: { ...TREE_SESSION, kind: "subagent", state: "idle" },
        }),
      );
    await user.click(rail().getByText("Session one"));
    expect(window.location.pathname).toBe("/s/local%3As1");
    await waitFor(() => {
      const state = workspaceStore.getState();
      const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
      expect(focused?.type).toBe("session");
      expect(focused?.params).toEqual({ ref: "local:s1" });
    });
    expect(workspaceStore.getState().mainPane()?.type).toBe("session");
    if (nested) {
      expect(workspaceStore.getState().mainPane()?.id).toBe(owner?.id);
      expect(
        workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId)?.slot,
      ).toBe("secondary");
      expect(await screen.findAllByRole("textbox", { name: "Message" })).toHaveLength(2);
    } else expect(await screen.findByRole("textbox", { name: "Message" })).toBeTruthy();
  },
);

// --- one shared needs-you owner for the committed shell lifetime ----------
// Call-through observation of the real owner factory and the registration
// surfaces the controller itself owns. The factory returns the real
// controller and every method forwards in full; the record only counts the
// synchronous resources the real factory/cleanup registered. `creating` bounds
// attribution to the factory call, so the shell's own subscriptions on the
// same stores are not miscounted. Consumer subscriptions (the provider's
// useSyncExternalStore hook) are counted separately and must balance.
type NeedsYouOwnerRecord = {
  controller: ReturnType<typeof needsYouActionsModule.createNeedsYouController>;
  disposed: boolean;
  disposeCalls: number;
  nextCalls: number;
  selectRefs: string[];
  consumerSubscriptions: number;
  consumerUnsubscribes: number;
  navigationSubscriptions: number;
  workspaceSubscriptions: number;
  paletteSubscriptions: number;
  routeObservations: number;
  popstateListeners: EventListenerOrEventListenerObject[];
  popstateRemovals: number;
  observer: MutationObserver | null;
  observeCalls: number;
  disconnects: number;
  releases: Array<ReturnType<typeof vi.fn>>;
};

function trackNeedsYouOwners() {
  const realCreate = needsYouActionsModule.createNeedsYouController;
  const realSubscribeNavigation = navigationStore.subscribe;
  const realSubscribeWorkspace = workspaceStore.subscribe;
  const realSubscribePalette = paletteStore.subscribe;
  const realObserveRoute = routingModule.observeRouteHistory;
  const records: NeedsYouOwnerRecord[] = [];
  let creating: NeedsYouOwnerRecord | null = null;

  vi.spyOn(navigationStore, "subscribe").mockImplementation((listener) => {
    const release = realSubscribeNavigation(listener);
    if (!creating) return release;
    creating.navigationSubscriptions += 1;
    const tracked = vi.fn(release);
    creating.releases.push(tracked);
    return tracked;
  });
  vi.spyOn(workspaceStore, "subscribe").mockImplementation((listener) => {
    const release = realSubscribeWorkspace(listener);
    if (!creating) return release;
    creating.workspaceSubscriptions += 1;
    const tracked = vi.fn(release);
    creating.releases.push(tracked);
    return tracked;
  });
  vi.spyOn(paletteStore, "subscribe").mockImplementation((listener) => {
    const release = realSubscribePalette(listener);
    if (!creating) return release;
    creating.paletteSubscriptions += 1;
    const tracked = vi.fn(release);
    creating.releases.push(tracked);
    return tracked;
  });
  vi.spyOn(routingModule, "observeRouteHistory").mockImplementation((observer) => {
    const release = realObserveRoute(observer);
    if (!creating) return release;
    creating.routeObservations += 1;
    const tracked = vi.fn(release);
    creating.releases.push(tracked);
    return tracked;
  });
  const addListener = vi.spyOn(window, "addEventListener");
  const removeListener = vi.spyOn(window, "removeEventListener");
  const observeSpy = vi.spyOn(MutationObserver.prototype, "observe");
  const disconnectSpy = vi.spyOn(MutationObserver.prototype, "disconnect");

  vi.spyOn(needsYouActionsModule, "createNeedsYouController").mockImplementation((options) => {
    const record: NeedsYouOwnerRecord = {
      controller: null as unknown as ReturnType<typeof realCreate>,
      disposed: false,
      disposeCalls: 0,
      nextCalls: 0,
      selectRefs: [],
      consumerSubscriptions: 0,
      consumerUnsubscribes: 0,
      navigationSubscriptions: 0,
      workspaceSubscriptions: 0,
      paletteSubscriptions: 0,
      routeObservations: 0,
      popstateListeners: [],
      popstateRemovals: 0,
      observer: null,
      observeCalls: 0,
      disconnects: 0,
      releases: [],
    };
    records.push(record);
    const addBefore = addListener.mock.calls.length;
    const observeBefore = observeSpy.mock.calls.length;
    let owner: ReturnType<typeof realCreate>;
    creating = record;
    try {
      owner = realCreate(options);
    } finally {
      creating = null;
    }
    record.controller = owner;
    record.observeCalls = observeSpy.mock.calls.length - observeBefore;
    record.observer = (observeSpy.mock.contexts[observeBefore] as MutationObserver | undefined) ?? null;
    record.popstateListeners = addListener.mock.calls
      .slice(addBefore)
      .filter(([type]) => type === "popstate")
      .map((call) => call[1] as EventListenerOrEventListenerObject);

    const realNext = owner.next.bind(owner);
    vi.spyOn(owner, "next").mockImplementation(() => {
      record.nextCalls += 1;
      return realNext();
    });
    const realSelect = owner.select.bind(owner);
    vi.spyOn(owner, "select").mockImplementation((ref) => {
      record.selectRefs.push(ref);
      realSelect(ref);
    });
    const realSubscribe = owner.subscribe.bind(owner);
    vi.spyOn(owner, "subscribe").mockImplementation((listener) => {
      record.consumerSubscriptions += 1;
      // The controller defers observation to its first subscriber, so the
      // observer and popstate listener it registers belong to this record even
      // though they appear after creation.
      const addBeforeSubscribe = addListener.mock.calls.length;
      const observeBeforeSubscribe = observeSpy.mock.calls.length;
      let release: () => void;
      creating = record;
      try {
        release = realSubscribe(listener);
      } finally {
        creating = null;
      }
      const startedHere = observeSpy.mock.calls.length - observeBeforeSubscribe;
      if (startedHere > 0) {
        record.observeCalls += startedHere;
        record.observer = (observeSpy.mock.contexts[observeBeforeSubscribe] as MutationObserver | undefined) ?? null;
        record.popstateListeners = [
          ...record.popstateListeners,
          ...addListener.mock.calls
            .slice(addBeforeSubscribe)
            .filter(([type]) => type === "popstate")
            .map((call) => call[1] as EventListenerOrEventListenerObject),
        ];
      }
      return () => {
        record.consumerUnsubscribes += 1;
        release();
      };
    });
    const realDispose = owner.dispose.bind(owner);
    vi.spyOn(owner, "dispose").mockImplementation(() => {
      record.disposeCalls += 1;
      if (record.disposed) {
        realDispose();
        return;
      }
      record.disposed = true;
      const removeBefore = removeListener.mock.calls.length;
      const disconnectBefore = disconnectSpy.mock.calls.length;
      realDispose();
      for (let index = removeBefore; index < removeListener.mock.calls.length; index += 1) {
        const call = removeListener.mock.calls[index];
        if (call?.[0] !== "popstate") continue;
        if (record.popstateListeners.includes(call[1] as EventListenerOrEventListenerObject))
          record.popstateRemovals += 1;
      }
      for (let index = disconnectBefore; index < disconnectSpy.mock.calls.length; index += 1) {
        if (disconnectSpy.mock.contexts[index] === record.observer) record.disconnects += 1;
      }
    });
    return owner;
  });

  return {
    records,
    active: () => records.filter((record) => !record.disposed),
    activeCount: () => records.filter((record) => !record.disposed).length,
    observing: () => records.filter((record) => record.observeCalls > 0 && !record.disposed),
    retired: () => records.filter((record) => record.disposed),
    restore: () => vi.restoreAllMocks(),
  };
}

function requireNeedsYouOwner(owners: ReturnType<typeof trackNeedsYouOwners>, index = 0): NeedsYouOwnerRecord {
  const record = owners.records[index];
  if (!record) throw new Error(`missing needs-you owner ${index}`);
  return record;
}

// The live owner registered exactly one of each owned resource, and none of
// the real release callbacks has run.
function expectOwnerRegistrations(record: NeedsYouOwnerRecord): void {
  expect(record.navigationSubscriptions).toBe(1);
  expect(record.workspaceSubscriptions).toBe(1);
  expect(record.paletteSubscriptions).toBe(1);
  expect(record.routeObservations).toBe(1);
  expect(record.releases).toHaveLength(4);
  for (const release of record.releases) expect(release).not.toHaveBeenCalled();
  expect(record.popstateListeners).toHaveLength(1);
  expect(record.popstateRemovals).toBe(0);
  expect(record.observeCalls).toBe(1);
  expect(record.observer).not.toBeNull();
  expect(record.disconnects).toBe(0);
}

// Disposal ran the owner's real cleanup once, released every subscription it
// created, removed its one popstate listener and disconnected its observer.
function expectOwnerReleased(record: NeedsYouOwnerRecord): void {
  expect(record.disposeCalls).toBe(1);
  expect(record.disposed).toBe(true);
  expect(record.navigationSubscriptions).toBe(1);
  expect(record.workspaceSubscriptions).toBe(1);
  expect(record.paletteSubscriptions).toBe(1);
  expect(record.routeObservations).toBe(1);
  expect(record.releases).toHaveLength(4);
  for (const release of record.releases) expect(release).toHaveBeenCalledTimes(1);
  expect(record.popstateListeners).toHaveLength(1);
  expect(record.popstateRemovals).toBe(1);
  expect(record.observeCalls).toBe(1);
  expect(record.disconnects).toBe(1);
}

// A controller React built and discarded without ever observing registered
// nothing, so there is nothing for it to release. The provider builds its
// controller during render, so a discarded render leaves exactly this record.
function expectOwnerUnobserved(record: NeedsYouOwnerRecord): void {
  expect(record.observeCalls).toBe(0);
  expect(record.navigationSubscriptions).toBe(0);
  expect(record.workspaceSubscriptions).toBe(0);
  expect(record.paletteSubscriptions).toBe(0);
  expect(record.routeObservations).toBe(0);
  expect(record.releases).toHaveLength(0);
  expect(record.popstateListeners).toHaveLength(0);
  expect(record.consumerSubscriptions).toBe(0);
  expect(record.disconnects).toBe(0);
}

function NeedsYouConsumerProbe({ onValue }: { onValue: (value: ReturnType<typeof useNeedsYou>) => void }): null {
  const value = useNeedsYou();
  useEffect(() => {
    onValue(value);
  }, [onValue, value]);
  return null;
}

test("additional consumers share the owner and mount or unmount adds no owner subscription", async () => {
  const owners = trackNeedsYouOwners();
  installNeedsYouRows();
  const seen: Array<ReturnType<typeof useNeedsYou>> = [];
  const capture = (value: ReturnType<typeof useNeedsYou>) => seen.push(value);
  let view: ReturnType<typeof render> | undefined;
  try {
    view = render(
      <NeedsYouProvider>
        <NeedsYouConsumerProbe onValue={capture} />
        <NeedsYouConsumerProbe onValue={capture} />
      </NeedsYouProvider>,
    );
    await waitFor(() => expect(seen.length).toBeGreaterThanOrEqual(2));
    const record = requireNeedsYouOwner(owners);
    expect(owners.records).toHaveLength(1);
    expect(seen[0]?.controller).toBe(record.controller);
    expect(seen[1]?.controller).toBe(record.controller);
    await waitFor(() => expect(record.observeCalls).toBe(1));
    expectOwnerRegistrations(record);
    const subscriptionsWithTwo = record.consumerSubscriptions;
    const unsubscribesWithTwo = record.consumerUnsubscribes;
    // The provider owns one shared store subscription; each consumer reads the
    // same snapshot through context and adds none of its own.
    expect(subscriptionsWithTwo - unsubscribesWithTwo).toBe(1);

    view.rerender(
      <NeedsYouProvider>
        <NeedsYouConsumerProbe onValue={capture} />
      </NeedsYouProvider>,
    );
    await waitFor(() => expect(seen.length).toBeGreaterThanOrEqual(2));
    expect(owners.records).toHaveLength(1);
    expect(record.disposeCalls).toBe(0);
    expectOwnerRegistrations(record);
    expect(record.consumerSubscriptions - record.consumerUnsubscribes).toBe(1);
    expect(record.consumerUnsubscribes).toBe(unsubscribesWithTwo);

    view.rerender(
      <NeedsYouProvider>
        <NeedsYouConsumerProbe onValue={capture} />
        <NeedsYouConsumerProbe onValue={capture} />
      </NeedsYouProvider>,
    );
    await waitFor(() => expect(seen.length).toBeGreaterThanOrEqual(3));
    expect(seen.at(-1)?.controller).toBe(record.controller);
    expect(owners.records).toHaveLength(1);
    expectOwnerRegistrations(record);
    expect(record.consumerSubscriptions - record.consumerUnsubscribes).toBe(1);
  } finally {
    view?.unmount();
    owners.restore();
  }
});

test("keyboard and palette consumers act through one shared needs-you owner", async () => {
  const owners = trackNeedsYouOwners();
  installNeedsYouRows();
  const user = userEvent.setup();
  let view: ReturnType<typeof render> | undefined;
  try {
    view = render(<AppShell client={navClient()} />);
    await waitFor(() => expect(owners.activeCount()).toBe(1));
    const record = requireNeedsYouOwner(owners);
    expect(owners.records).toHaveLength(1);
    expectOwnerRegistrations(record);

    await user.keyboard("{Meta>}j{/Meta}");
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
    expect(record.nextCalls).toBe(1);
    expect(window.location.pathname).toBe("/s/local%3Any1");
    expect(owners.records).toHaveLength(1);
    expectOwnerRegistrations(record);

    await user.keyboard("{Meta>}k{/Meta}");
    const paletteChoice = await screen.findByRole("option", { name: /Needs you 2/i });
    await user.click(paletteChoice);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny2" }));
    expect(record.selectRefs).toEqual(["local:ny2"]);
    expect(window.location.pathname).toBe("/s/local%3Any2");
    expect(owners.records).toHaveLength(1);
    expectOwnerRegistrations(record);
  } finally {
    view?.unmount();
    owners.restore();
  }
});

// The palette's registered next-needs-you command and Mod+J are one action:
// each enters the provider's single controller and must produce the same
// demand, URL, history entry and pane placement from the same paging state.
// Run each path in its own fresh shell so the second does not read the page
// the first loaded.
test("the palette next-needs-you command is Mod+J from the same state", async () => {
  const owners = trackNeedsYouOwners();
  const current = {
    ...TREE_SESSION,
    ref: "local:cmd-page-one-last",
    session_id: "cmd-page-one-last",
    title: "Command page one last",
    state: "awaiting",
  };
  const next = {
    ...TREE_SESSION,
    ref: "local:cmd-page-two-first",
    session_id: "cmd-page-two-first",
    title: "Command page two first",
    state: "awaiting",
  };
  const firstPageRows = [
    ...Array.from({ length: 49 }, (_, index) => ({
      ...TREE_SESSION,
      ref: `local:cmd-page-one-${index}`,
      session_id: `cmd-page-one-${index}`,
      title: `Command page one ${index}`,
      state: "awaiting",
    })),
    current,
  ];
  const makeRun = () => {
    const reads: NeedsYouWireRead[] = [];
    const client = needsYouClient((params) => {
      reads.push({ offset: params.offset, limit: params.limit });
      return wireSnapshot(
        params,
        params.offset === 0 ? { sessions: firstPageRows, remaining: 1 } : { sessions: [next], remaining: 0 },
        `"cmd-page-${params.offset}"`,
        1,
        "generation_test",
      );
    }, firstPageRows.length + 1);
    return { reads, client };
  };
  const user = userEvent.setup();
  const paneShape = (ref: string) => {
    const pane = paneFor(ref);
    return pane ? { type: pane.type, params: pane.params, slot: pane.slot } : null;
  };
  let view: ReturnType<typeof render> | undefined;
  try {
    const keyboardRun = makeRun();
    window.history.pushState({}, "", "/s/local%3Acmd-page-one-last");
    view = render(<AppShell client={keyboardRun.client} />);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: current.ref }));
    await waitFor(() =>
      expect(
        needsYouPages().some((page) => page.key.kind === "section" && page.key.offset === 0 && !page.loading),
      ).toBe(true),
    );
    const keyboardSourcePane = paneFor(current.ref);
    const keyboardHistory = window.history.length;

    fireEvent.keyDown(window, { key: "j", metaKey: true });

    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: next.ref }));
    const keyboard = {
      reads: [...keyboardRun.reads],
      path: window.location.pathname,
      historyDelta: window.history.length - keyboardHistory,
      currentPanePreserved: paneFor(current.ref) === keyboardSourcePane,
      nextPane: paneShape(next.ref),
      focusedRef: focusedSessionRef(),
      attentionEntry: currentAttentionEntryToken() !== null,
    };
    const keyboardOwner = requireNeedsYouOwner(owners, 0);
    expect(keyboardOwner.nextCalls).toBe(1);
    expect(keyboardOwner.selectRefs).toEqual([]);

    view.unmount();
    view = undefined;
    expect(owners.activeCount()).toBe(0);
    resetWorkspaceStoreForTests();
    resetNavigationStoreForTests();
    navigationStore.setState({ mode: "v3" });
    paletteStore.setState({ open: false, query: "" });
    localStorage.clear();
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });

    const commandRun = makeRun();
    window.history.pushState({}, "", "/s/local%3Acmd-page-one-last");
    view = render(<AppShell client={commandRun.client} />);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: current.ref }));
    await waitFor(() =>
      expect(
        needsYouPages().some((page) => page.key.kind === "section" && page.key.offset === 0 && !page.loading),
      ).toBe(true),
    );
    const commandSourcePane = paneFor(current.ref);
    const commandHistory = window.history.length;

    await user.keyboard("{Meta>}k{/Meta}");
    const input = await screen.findByRole("combobox", { name: "Command palette" });
    await user.type(input, "/next-needs-you");
    await user.keyboard("{Enter}");

    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: next.ref }));
    const command = {
      reads: [...commandRun.reads],
      path: window.location.pathname,
      historyDelta: window.history.length - commandHistory,
      currentPanePreserved: paneFor(current.ref) === commandSourcePane,
      nextPane: paneShape(next.ref),
      focusedRef: focusedSessionRef(),
      attentionEntry: currentAttentionEntryToken() !== null,
    };
    const commandOwner = requireNeedsYouOwner(owners, 1);
    expect(commandOwner.nextCalls).toBe(1);
    expect(commandOwner.selectRefs).toEqual([]);

    expect(command).toEqual(keyboard);
    expect(keyboard.reads).toEqual([
      { offset: 0, limit: 50 },
      { offset: 50, limit: 50 },
    ]);
    expect(keyboard.path).toBe("/s/local%3Acmd-page-two-first");
    expect(keyboard.historyDelta).toBe(1);
    expect(keyboard.currentPanePreserved).toBe(true);
    expect(keyboard.nextPane).toMatchObject({ type: "session", params: { ref: next.ref }, slot: "main" });
    expect(keyboard.focusedRef).toBe(next.ref);
    expect(keyboard.attentionEntry).toBe(true);
  } finally {
    view?.unmount();
    owners.restore();
  }
});

test("desktop to mobile to desktop keeps the same needs-you owner and Next usable", async () => {
  const owners = trackNeedsYouOwners();
  const setMobile = installSwitchableViewport();
  window.history.replaceState({}, "", "/s/local%3Any1");
  installLocationForRoute("local:ny1");
  installNeedsYouRows();
  const user = userEvent.setup();
  let view: ReturnType<typeof render> | undefined;
  try {
    view = render(<AppShell client={navClient()} />);
    await screen.findByText(/loading transcript/i);
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
    const record = requireNeedsYouOwner(owners);
    const source = workspaceStore.getState().mainPane();
    if (!source) throw new Error("missing initial needs-you reader");
    const historyBefore = window.history.length;
    expectOwnerRegistrations(record);

    act(() => setMobile(true));
    await screen.findByRole("button", { name: "Back" });
    act(() => setMobile(false));
    await waitFor(() => expect(getDockviewApi()).not.toBeNull());
    expect(window.location.pathname).toBe("/s/local%3Any1");
    expect(window.history.length).toBe(historyBefore);
    expect(owners.records).toHaveLength(1);
    expect(record.disposeCalls).toBe(0);
    expectOwnerRegistrations(record);
    expect(workspaceStore.getState().mainPane()).toBe(source);
    expect(workspaceStore.getState().focusedPaneId).toBe(source.id);

    await user.keyboard("{Meta>}j{/Meta}");
    await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny2" }));
    expect(record.nextCalls).toBe(1);
    expect(window.location.pathname).toBe("/s/local%3Any2");
    expect(window.history.length).toBe(historyBefore + 1);
    expect(owners.records).toHaveLength(1);
    expectOwnerRegistrations(record);
    expect(workspaceStore.getState().panes).toContain(source);
  } finally {
    view?.unmount();
    owners.restore();
  }
});

test("StrictMode retires its first owner and unmount releases every owner resource", async () => {
  const owners = trackNeedsYouOwners();
  installNeedsYouRows();
  let view: ReturnType<typeof render> | undefined;
  try {
    view = render(
      <StrictMode>
        <AppShell client={navClient()} />
      </StrictMode>,
    );
    await waitFor(() => expect(owners.observing()).toHaveLength(1));
    await waitFor(() => expect(owners.records.length).toBeGreaterThanOrEqual(2));
    // Anything React built and discarded registered nothing, so the only owner
    // holding resources is the one that observes.
    for (const record of owners.records) if (record.observeCalls === 0) expectOwnerUnobserved(record);
    const active = owners.observing();
    expect(active).toHaveLength(1);
    const live = requireNeedsYouOwner(owners, owners.records.indexOf(active[0] as NeedsYouOwnerRecord));
    const retired = owners.retired();
    for (const record of retired) if (record.observeCalls > 0) expectOwnerReleased(record);
    expectOwnerRegistrations(live);

    view.unmount();
    view = undefined;
    expect(owners.observing()).toHaveLength(0);
    expectOwnerReleased(live);
    expect(live.consumerSubscriptions).toBeGreaterThan(0);
    expect(live.consumerUnsubscribes).toBe(live.consumerSubscriptions);

    // A store, modal and route event after disposal must not update the retired
    // owner or navigate. The controller is already disposed; subscribing after
    // that point also proves the disposed instance cannot fan out notifications.
    const notifications = vi.fn();
    const unsubscribe = live.controller.subscribe(notifications);
    const pathname = window.location.pathname;
    const historyLength = window.history.length;
    await act(async () => {
      navigationStore.setState({ mode: "v3" });
      const modal = document.createElement("div");
      modal.setAttribute("aria-modal", "true");
      document.body.appendChild(modal);
      window.dispatchEvent(new PopStateEvent("popstate"));
      await Promise.resolve();
      modal.remove();
    });
    unsubscribe();
    expect(notifications).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe(pathname);
    expect(window.history.length).toBe(historyLength);
    expect(workspaceStore.getState().mainPane()?.type).not.toBe("session");
    for (const record of owners.records) if (record.observeCalls === 0) expectOwnerUnobserved(record);
  } finally {
    view?.unmount();
    owners.restore();
  }
});

test.each(["retained", "released"] as const)(
  "a held Next demand released after unmount cannot navigate and a fresh shell still acts with navigation %s",
  async (navigationLifetime) => {
    const owners = trackNeedsYouOwners();
    const held = heldNeedsYouPage("local:after-unmount");
    const requests = vi.spyOn(held.client, "request");
    // The first init owns navigation, same-client init in the shell is a no-op.
    // Retain that real owner only for the case where late data must still land.
    // Otherwise the shell owns the final release and must fence the response.
    const releaseNavigation = navigationLifetime === "retained" ? initNavigation(held.client) : () => {};
    const historyBefore = window.history.length;
    let view: ReturnType<typeof render> | undefined;
    let freshView: ReturnType<typeof render> | undefined;
    try {
      view = render(<AppShell client={held.client} />);
      await waitFor(() => expect(owners.activeCount()).toBe(1));
      await held.entered;
      fireEvent.keyDown(window, { key: "j", metaKey: true });
      const record = requireNeedsYouOwner(owners);
      await waitFor(() => expect(record.nextCalls).toBe(1));
      const next = vi.mocked(record.controller.next).mock.results[0];
      if (next?.type !== "return") throw new Error("missing real Next demand promise");
      expect(next.value).toBeInstanceOf(Promise);
      const pageRequestIndex = requests.mock.calls.findIndex(
        ([method, params]) =>
          method === "evener/navigation/read" && (params as NavigationReadParams).section === "needs_you",
      );
      const pageRequest = requests.mock.results[pageRequestIndex];
      if (pageRequest?.type !== "return") throw new Error("missing held navigation request promise");
      expect(held.reads).toEqual([{ offset: 0, limit: 50 }]);
      expect(needsYouPages()).toHaveLength(1);
      expect(needsYouPages()[0]?.loading).toBe(true);
      expect(window.location.pathname).toBe("/");
      expect(window.history.length).toBe(historyBefore);

      view.unmount();
      view = undefined;
      expect(owners.activeCount()).toBe(0);
      expectOwnerReleased(record);
      expect(record.consumerSubscriptions).toBeGreaterThan(0);
      expect(record.consumerUnsubscribes).toBe(record.consumerSubscriptions);

      await act(async () => {
        held.release();
        // Await the actual transport response and the real keyboard action's
        // continuation, not an empty-resource poll or a fixed microtask count.
        await pageRequest.value;
        await next.value;
      });
      if (navigationLifetime === "retained") {
        expect(navigationStore.getState().mode).toBe("v3");
        expect(needsYouPages()).toHaveLength(1);
        expect(needsYouPages().every((page) => !page.loading)).toBe(true);
        // The released page really landed, so the "no navigation" assertion is not
        // vacuous.
        expect(
          needsYouPages().some((page) =>
            ((page.data as { sessions?: Array<{ ref: string }> } | null)?.sessions ?? []).some(
              (row) => row.ref === "local:after-unmount",
            ),
          ),
        ).toBe(true);
      } else {
        // The shell was the final navigation owner. A response that actually
        // settled after teardown must not revive its resources or selectable rows.
        expect(needsYouPages()).toEqual([]);
        expect(navigationStore.getState().resources.size).toBe(0);
        expect(navigationStore.getState().mode).toBe("unknown");
        expect(selectNeedsYouRows(navigationStore.getState())).toEqual([]);
      }
      expect(window.location.pathname).toBe("/");
      expect(window.history.length).toBe(historyBefore);
      expect(workspaceStore.getState().mainPane()?.type).not.toBe("session");
      expect(owners.records).toHaveLength(1);

      releaseNavigation();
      // A later fresh shell must still create one owner and act normally.
      freshView = render(<AppShell client={navClient()} />);
      await waitFor(() => expect(owners.activeCount()).toBe(1));
      await waitFor(() => expect(connectionStore.getState().state).toBe("ready"));
      await waitFor(() => expect(owners.activeCount()).toBe(1));
      const fresh = owners.active()[0];
      if (!fresh) throw new Error("fresh shell created no needs-you owner");
      expect(fresh.controller).not.toBe(record.controller);
      await act(async () => {
        installNeedsYouRows();
        await Promise.resolve();
      });
      await waitFor(() => expect(selectNeedsYouRows(navigationStore.getState())).toHaveLength(2));
      fireEvent.keyDown(window, { key: "j", metaKey: true });
      await waitFor(() => expect(fresh.nextCalls).toBe(1));
      await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
      expect(window.location.pathname).toBe("/s/local%3Any1");
      expect(owners.records).toHaveLength(2);
    } finally {
      view?.unmount();
      freshView?.unmount();
      releaseNavigation();
      owners.restore();
    }
  },
);

// The real browser guard measured the floating Next-needs-you control at seven
// widths after a phone/desktop host swap and found it only at 1024 and 1400.
// This reproduces the swap half in the shell: one focused live session, a
// populated needs-you tier, a desktop mount (whose composer mount autofocus
// hides the control until the reader clicks away), then StackHost in its
// place. The control's mount gate lives in ConversationBody; nothing about it
// may depend on which host is showing the pane.
test("phone host swap keeps attention navigation over the focused session", async () => {
  vi.stubGlobal("innerWidth", 1000);
  const setMobile = installSwitchableViewport();
  const client = navClient();
  client.on("thread/read", ({ ref }) => {
    if (!ref) throw new Error("thread/read requires ref");
    return { thread: { ...threadStartResponse(ref).thread, name: ref } };
  });
  client.on("thread/unsubscribe", () => ({}));
  client.on("evener/thread/activity/read", answerActivityRead);
  installNeedsYouRows(["local:ny1", "local:ny2"]);
  window.history.pushState({}, "", "/s/local%3Aowner");
  installLocationForRoute("local:owner");
  render(<AppShell client={client} />);

  const editor = await screen.findByRole("textbox", { name: "Message" });
  // The desktop mount autofocuses this editor. Focus alone no longer hides the
  // control, so the same conversation offers it without a reader's click.
  await waitFor(() => expect(document.activeElement).toBe(editor));
  await screen.findByTestId("next-needs-you-next");

  act(() => setMobile(true));
  await screen.findByRole("button", { name: "Back" });
  await screen.findByRole("textbox", { name: "Message" });
  const owner = paneFor("local:owner");
  expect(workspaceStore.getState().focusedPaneId).toBe(owner?.id);
  await screen.findByTestId("next-needs-you-next");
});
