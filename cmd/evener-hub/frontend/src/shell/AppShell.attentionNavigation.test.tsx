import type { NavigationReadParams, NavigationReadResponse, NavigationSessionLocation } from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, type ConnectionState } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect } from "react";

import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { retainedTranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { enterAgentCascade } from "../panes/zoom/actions";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { resetAuthStatusesStoreForTests } from "../stores/authStatuses";
import { connectionStore } from "../stores/connection";
import { selectNeedsYouRows } from "../stores/navigation/selectors";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetPrefsStoreForTests } from "../stores/prefs";
import { activityDelegate, answerActivityRead } from "../stores/sessionActivityTestUtils";
import { resetSettingsHostForTests } from "../stores/settingsHost";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import { installFailedLocation, installSwitchableViewport, threadStartResponse } from "./appShellTestUtils";
import { currentAttentionEntryToken, openAttentionSession } from "./attentionHistory";
import { focusedSessionRef } from "./focusedSession";
import { NeedsYouProvider, useNeedsYou } from "./NeedsYouProvider";
import * as needsYouActionsModule from "./needsYouActions";
import { paletteStore } from "./palette/paletteController";
import { conversationPaneLifetime } from "./paneLifetime";
import * as routingModule from "./routing";
import { navigate, routeHistoryEntryId } from "./routing";
import { getDockviewApi, type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

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
