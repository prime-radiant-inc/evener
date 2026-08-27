import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import * as composerFocus from "../panes/session/composer/composerFocus";
import { AppwireClient, type ConnectionState } from "../protocol/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InitializeResponse, NavigationSessionLocation, ThreadStartResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { type NavigationStoreState, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { keyID } from "../stores/navigation/types";
import { resetSettingsOverviewStoreForTests } from "../stores/settingsOverview";
import { AppShell } from "./AppShell";
import { DockHost } from "./DockHost";
import { paletteStore } from "./palette/paletteController";
import { getDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// Matches DockHost.tsx's own LAYOUT_STORAGE_KEY exactly (not exported - a
// deliberately internal implementation detail; duplicated here the same
// way DockHost.test.tsx's own LAYOUT_KEY is).
const LAYOUT_KEY = "evener.workspace.layout.v2";
const NAV_URL = "/api/navigation";

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

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => (body === undefined ? Promise.reject(new Error("no body")) : Promise.resolve(body)),
  } as Response;
}

// A navigation manifest Response with the headers the store's requestFor
// requires (X-Evener-Navigation-Generation/Revision/etag). The plain
// jsonResponse() omits these, so any test that lets initNavigation actually
// fetch the manifest would fail with a "missing generation" protocol error.
function navResponse(body: unknown = EMPTY_NAV_RESPONSE): Response {
  return new Response(JSON.stringify(body), {
    headers: {
      "Content-Type": "application/json",
      etag: '"test"',
      "X-Evener-Navigation-Generation": "generation_test",
      "X-Evener-Navigation-Revision": "1",
    },
  });
}

// A fetch handler that serves proper navigation Responses for every
// /api/navigation URL the store's boot() or Rail might request: the manifest,
// sections, catalogs, and projects. Each returns valid JSON with the required
// headers so the revalidator's requestFor validation passes. The live section
// includes TREE_SESSION so the rail can render "Session one".
function navFetchHandler(url: string): Promise<Response> {
  const u = String(url);
  const headers = {
    "Content-Type": "application/json",
    etag: '"test"',
    "X-Evener-Navigation-Generation": "generation_test",
    "X-Evener-Navigation-Revision": "1",
  };
  if (u === NAV_URL) return Promise.resolve(navResponse());
  if (u.includes("/api/navigation/sections/live")) {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          generation_id: "generation_test",
          revision: 1,
          sessions: [TREE_SESSION],
          remaining: 0,
          truncated: false,
        }),
        { headers },
      ),
    );
  }
  if (u.includes("/api/navigation/sections/needs-you")) {
    return Promise.resolve(
      new Response(
        JSON.stringify({ generation_id: "generation_test", revision: 1, sessions: [], remaining: 0, truncated: false }),
        { headers },
      ),
    );
  }
  if (u.includes("/api/navigation/catalogs/projects")) {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          generation_id: "generation_test",
          revision: 1,
          projects: [{ key: "proj1", name: "Project one", session_count: 1, working_dir: "" }],
          remaining: 0,
        }),
        { headers },
      ),
    );
  }
  if (u.includes("/api/navigation/catalogs/")) {
    return Promise.resolve(
      new Response(JSON.stringify({ generation_id: "generation_test", revision: 1, projects: [], remaining: 0 }), {
        headers,
      }),
    );
  }
  if (u.includes("/api/navigation/projects/")) {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          generation_id: "generation_test",
          revision: 1,
          key: "proj1",
          current: { sessions: [TREE_SESSION], remaining: 0 },
          recent: { sessions: [], remaining: 0 },
          archived: { sessions: [], remaining: 0 },
          truncated: false,
        }),
        { headers },
      ),
    );
  }
  return Promise.resolve(new Response(JSON.stringify({}), { headers }));
}

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
  children: [
    {
      row_id: "project:proj1:local:sub1",
      ref: "local:sub1",
      host_id: "local",
      session_id: "sub1",
      title: "Finished helper",
      project: "prime-radiant",
      state: "ended",
      kind: "subagent",
      live: false,
      children: [],
    },
  ],
};
const EMPTY_NAV_RESPONSE = {
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

// A FakeClient whose connect() advertises a v1 navigation capability with a
// generation matching EMPTY_NAV_RESPONSE. Tests that render <AppShell/> and
// depend on the navigation store being in mode "v1" (rather than "error")
// must use this instead of a bare `new FakeClient("ready")`, whose default
// InitializeResponse has no navigation capability.
function navClient(initialState: ConnectionState = "ready"): FakeClient {
  const client = new FakeClient(initialState);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0 },
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
  queue: false,
  goal: false,
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
    mode: "v1",
    clientGenerationID: location.generation_id,
    resources,
  });
}

function installLocationForRoute(ref: string): void {
  const owner = ref === "local:child" ? "local:owner" : ref === "local:sub1" ? "local:s1" : ref;
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

function installNeedsYouRows(): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = [
    { ...TREE_SESSION, ref: "local:ny1", session_id: "ny1", title: "Needs you one", state: "awaiting" },
    { ...TREE_SESSION, ref: "local:ny2", session_id: "ny2", title: "Needs you two", state: "awaiting" },
  ];
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
  navigationStore.setState({ mode: "v1", resources });
}

// jsdom has no ResizeObserver (dockview-core dials one on mount to drive its
// auto-resizing) and, separately, Node 26's own global `localStorage`
// accessor shadows jsdom's real one without --localstorage-file - both
// verified via a live probe, duplicated here rather than shared since this
// project has no cross-test-file test-utils module (see stores/
// threads.test.ts's own identical note on duplicating a helper for the same
// reason). See DockHost.test.tsx's own comments for the full detail on
// each.
class StubResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
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
// leaves the first render suspending, committing its Suspense fallback, and
// then waiting out react-dom's FALLBACK_THROTTLE_MS (300ms, react-dom 19.2)
// before it will commit the revealed content - a flicker guard that is pure
// wall clock and does not shrink on a fast machine. The welcome route
// crosses two nested boundaries (AppShell's lazy DockHost, then PaneHost's
// lazy Welcome); each other pane crosses one more of its own. Measured here:
// ~654ms for the two-boundary welcome route and ~310-380ms per additional
// pane the first time, ~10-20ms every time after. Mirrors App.test.tsx's own
// warmRoute (commit c1a8616ea) for the same reason.
//
// The landmark wait gets WARM_ROUTE_TRIPWIRE_MS rather than findBy's 1000ms
// default. That default is an assertion window - it exists to hold a test to a
// responsiveness bar - and a warm-up has no such bar to hold: its whole job is
// to absorb the variable cost so the real assertions don't. At ~654ms of a
// 1000ms budget the warm-up had under 40% headroom and failed roughly one full
// suite run in two. The awaitable half of the cost is already awaited (the
// beforeAll imports below); what remains is react-dom's fixed per-boundary
// flicker throttle, which publishes no completion signal to wait on, so a
// deadline here can only ever be a tripwire for a hung render.
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
  render(<AppShell client={new FakeClient("ready")} />);
  await findLandmark({ timeout: WARM_ROUTE_TRIPWIRE_MS });
  // Unmounting also clears DockHost's pending debounced layout save (its own
  // effect cleanup), so no warm render leaks a write into a later test.
  cleanup();
  resetWorkspaceStoreForTests();
  // The /settings/general warm route mounts the real GeneralSection, whose
  // mount effect calls settingsOverviewStore.getState().fetch() for real
  // against this warm-up's own FakeClient (no handler scripted for
  // "evener/settings/overview", so it settles into a scripted-looking
  // "no handler" error) - settingsOverviewStore is a module singleton, so
  // that leftover error/inflight bookkeeping must not survive into this
  // file's own tests or the next file in the worker.
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
  // @ts-expect-error MemoryStorage deliberately implements only the Storage
  // methods DockHost.tsx actually calls (getItem/setItem/removeItem/clear),
  // not length/key() - see DockHost.test.tsx's own MemoryStorage comment.
  globalThis.localStorage = new MemoryStorage();
  vi.stubGlobal("fetch", (url: string) => navFetchHandler(url));
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
  await warmRoute("/settings/general", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v1" });
  // afterEach restores Vitest globals; recreate deterministic storage before
  // clearing it so DockHost cannot restore the prior test's layout.
  // @ts-expect-error MemoryStorage implements the subset used by DockHost.
  globalThis.localStorage = new MemoryStorage();
  localStorage.clear();
  vi.stubGlobal("fetch", (url: string) => navFetchHandler(url));
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // The command palette store is a module singleton; reset it so one test's
  // open palette never leaks into the next.
  paletteStore.setState({ open: false, query: "" });
  // Several tests below navigate to /settings/* and mount the real
  // GeneralSection, whose mount effect touches settingsOverviewStore for
  // real - same module-singleton reasoning as warmRoute's own reset above.
  resetSettingsOverviewStoreForTests();
  // Rendering <AppShell/> above calls notifications/index.ts's
  // initNotifications() at module scope (guarded by its own "only once"
  // flag), wiring its reconnect detector to whichever FakeClient this test
  // connected to "ready". Left unreset, that detector's stale "sawReady"
  // flag makes a later file's own fresh ready-client connect read as a
  // spurious reconnect, firing an unexpected navigationStore.refresh() into that
  // file's own fetch-call assertions (see App.test.tsx's identical reset
  // and its own comment; ConnectionBanner.test.tsx's Retry test was a
  // confirmed victim of this exact leak before this reset was added).
  //
  // AppShell.tsx's module-scope initNotifications() call only ever fires
  // once per worker (its own "only once" guard), so leaving it reset would
  // leave the engine permanently uninitialized for the rest of this
  // isolate:false worker - so it is re-run immediately below, restoring the
  // same state a fresh module evaluation would have left (kata p5w9's
  // identical pattern below). initNotifications() seeds its
  // `sawReady`/baseline snapshot from whatever connectionStore/navigationStore
  // hold AT THIS MOMENT, so both are forced back to their neutral
  // pre-render values FIRST - seeding from a still-"ready" connectionStore
  // (as this test's own render left it moments ago) would wrongly arm the
  // "reconnect" detector this reset exists to neutralize. Run before
  // vi.unstubAllGlobals() below so initNotifications()'s baseline
  // ensureLoaded() fetch still hits this file's own beforeEach fetch stub
  // instead of a real network call.
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  resetNotificationsForTests();
  initNotifications();
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
  await screen.findByText(/Loading session panel…/);

  unmount();
  expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  resetWorkspaceStoreForTests();
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

test("mounts and renders the welcome pane", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("wires the injected client into connectionStore (connects on mount)", () => {
  const fake = new FakeClient("ready");
  render(<AppShell client={fake} />);
  expect(connectionStore.getState().client).toBe(fake);
  expect(connectionStore.getState().state).toBe("ready");
});

test("shows no banner while the injected client is ready", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(screen.queryByText(/reconnecting/i)).toBeNull();
  expect(screen.queryByText(/connection closed/i)).toBeNull();
});

test("banner reflects reconnecting state when injected", async () => {
  const fake = new FakeClient("ready");
  render(<AppShell client={fake} />);
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

  expect(focusSpy).toHaveBeenCalledWith("local:ref_abc123");
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

// --- Mod+J cycles needs-you sessions (UX fix) -----------------------------
// (needs-you rows are installed directly via installNeedsYouRows, not via fetch)

test("Mod+J opens the first needs-you session when nothing is focused", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "generation_test", sequence: 0 },
  }));
  vi.stubGlobal("fetch", (_url: string) => Promise.resolve(jsonResponse({})));
  render(<AppShell client={client} />);
  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());

  await user.keyboard("{Meta>}j{/Meta}");

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:ny1" }));
});

test("Mod+J cycles from the focused needs-you session to the next one, wrapping", async () => {
  const user = userEvent.setup();
  installNeedsYouRows();
  const client = navClient();
  vi.stubGlobal("fetch", (url: string) => navFetchHandler(url));
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

test("v1 Mod+J cold-demand requests page zero once and opens its first ref", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const row = { ...TREE_SESSION, ref: "local:cold", session_id: "cold", title: "Cold row", state: "awaiting" };
  const loadSection = vi.fn(async (_section: "needs_you", offset = 0) => {
    expect(offset).toBe(0);
    const key = { kind: "section", section: "needs_you", offset, limit: 50 } as const;
    const locationKey = { kind: "location", ref: row.ref } as const;
    const location: NavigationSessionLocation = {
      generation_id: "generation_test",
      revision: 1,
      ref: row.ref,
      top_level_ref: row.ref,
      top_level: true,
      session: { ...row, children: [] },
    };
    const resources = new Map(navigationStore.getState().resources);
    resources.set(keyID(key), {
      key,
      data: { generation_id: "generation_test", revision: 1, sessions: [row], remaining: 0, truncated: false },
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "row",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    });
    resources.set(keyID(locationKey), {
      key: locationKey,
      data: location,
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "loc",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    });
    navigationStore.setState({ resources });
    return navigationStore.getState().resources.get(keyID(key)) as never;
  });
  act(() =>
    navigationStore.setState({
      mode: "v1",
      manifest: {
        data: {
          generation_id: "generation_test",
          revision: 1,
          sources: [],
          attentionSummary: { needsYou: 1, error: 0, working: 0 },
          catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        },
      } as never,
      loadSection,
    }),
  );
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: row.ref }));
});

function seedColdModJPage(ref = "local:late") {
  const row = { ...TREE_SESSION, ref, session_id: ref, title: "Late row", state: "awaiting" };
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: { generation_id: "generation_test", revision: 1, sessions: [row], remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "late",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  navigationStore.setState({ resources });
}

function setColdModJState(loadSection: NavigationStoreState["loadSection"]) {
  navigationStore.setState({
    mode: "v1",
    manifest: {
      data: {
        generation_id: "generation_test",
        revision: 1,
        sources: [],
        attentionSummary: { needsYou: 1, error: 0, working: 0 },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
      },
    } as never,
    loadSection,
  });
}

test("late Mod-J page success does not navigate after focus moves to Settings", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  let resolveLoad!: (value: never) => void;
  const loadSection = vi.fn(() => new Promise<never>((resolve) => (resolveLoad = resolve)));
  setColdModJState(loadSection);
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  act(() => workspaceStore.getState().replacePrimary("settings", {}));
  seedColdModJPage();
  resolveLoad(undefined as never);
  await Promise.resolve();
  await Promise.resolve();
  expect(window.location.pathname).toBe("/");
  expect(workspaceStore.getState().mainPane()?.type).toBe("settings");
});

test("late Mod-J page success does not navigate after a modal opens", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  let resolveLoad!: (value: never) => void;
  const loadSection = vi.fn(() => new Promise<never>((resolve) => (resolveLoad = resolve)));
  setColdModJState(loadSection);
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  document.body.appendChild(modal);
  seedColdModJPage("local:modal-late");
  resolveLoad(undefined as never);
  await Promise.resolve();
  await Promise.resolve();
  expect(window.location.pathname).toBe("/");
  modal.remove();
});

test("v1 Mod-J does not re-request an in-flight needs-you page", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  let resolveLoad!: (value: never) => void;
  const loadSection = vi.fn(() => new Promise<never>((resolve) => (resolveLoad = resolve)));
  setColdModJState(loadSection);
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  resolveLoad(undefined as never);
  await Promise.resolve();
  expect(loadSection).toHaveBeenCalledTimes(1);
});

test("v1 Mod-J does not re-request a failed needs-you page", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const loadSection = vi.fn(() => Promise.reject(new Error("network failed")));
  setColdModJState(loadSection);
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await Promise.resolve();
  await Promise.resolve();
  expect(loadSection).toHaveBeenCalledTimes(1);
});

test("v1 Mod-J does not re-request an empty needs-you page", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  const loadSection = vi.fn(async () => {
    const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
    const resources = new Map(navigationStore.getState().resources);
    resources.set(keyID(key), {
      key,
      data: { generation_id: "generation_test", revision: 1, sessions: [], remaining: 0, truncated: false },
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "empty",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    });
    navigationStore.setState({ resources });
    return navigationStore.getState().resources.get(keyID(key)) as never;
  });
  setColdModJState(loadSection);
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await waitFor(() => expect(loadSection).toHaveBeenCalledTimes(1));
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  fireEvent.keyDown(window, { key: "j", metaKey: true });
  await Promise.resolve();
  await Promise.resolve();
  expect(loadSection).toHaveBeenCalledTimes(1);
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
  await user.click(screen.getByTestId("rail-search"));

  expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("populates connectionStore.serverInfo from the injected client's scripted connect() response", async () => {
  const fake = new FakeClient("ready");
  const scripted: InitializeResponse = {
    serverInfo: { name: "evener-hub-test", version: "9.9.9" },
    protocolVersion: "1",
    sourceId: "src_test",
    features: ALL_FEATURES_OFF,
  };
  fake.scriptConnect(() => scripted);

  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  await waitFor(() => {
    expect(connectionStore.getState().serverInfo).toEqual({ name: "evener-hub-test", version: "9.9.9" });
  });
});

test("closes the client it constructed itself on unmount", () => {
  const closeSpy = vi.spyOn(AppwireClient.prototype, "close");
  // No client prop: AppShell constructs its own real AppwireClient (never
  // connected under MODE==="test" - see AppShell.tsx) and must own tearing
  // it down again on unmount, the same one-client-per-window invariant a
  // real navigation away from the app would need.
  const { unmount } = render(<AppShell />);

  unmount();

  expect(closeSpy).toHaveBeenCalledTimes(1);
  closeSpy.mockRestore();
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
  // THEN checks the ref appears twice (tab + pane body title, no thread
  // name known so both fall back to the raw ref - see Session.tsx).
  expect(await screen.findByText(/loading transcript/i)).toBeTruthy();
  expect(screen.getAllByText("local:ref_abc123")).toHaveLength(2);
});

test("a nested location opens its explicit owner without loading a project", async () => {
  const child = "local:collapsed-child";
  const fetchMock = vi.fn((url: string) => navFetchHandler(url));
  vi.stubGlobal("fetch", fetchMock);
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
  render(<AppShell client={navClient()} />);

  await waitFor(() => expect(paneFor(child)?.slot).toBe("secondary"));
  expect(paneFor("local:owner")?.slot).toBe("main");
  expect([...navigationStore.getState().resources.values()].some((resource) => resource.key.kind === "project")).toBe(
    false,
  );
  expect(
    fetchMock.mock.calls.some((call) => String((call as unknown[])[0]).includes("/api/navigation/projects/")),
  ).toBe(false);
});

test("retained non-404 location data does not retry or lose its owner", async () => {
  const child = "local:retained-child";
  const fetchMock = vi.fn(() => Promise.resolve(jsonResponse({})));
  vi.stubGlobal("fetch", fetchMock);
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "1",
    sourceId: "fake",
    features: ALL_FEATURES_OFF,
    navigation: { version: 1, generationId: "generation_test", sequence: 0 },
  }));
  navigationStore.setState({ mode: "v1" });
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
    resources.set(key, { ...resource, stale: true, error: { status: 500 } });
    navigationStore.setState({ resources });
  });
  await waitFor(() => expect(paneFor("local:retained-owner")?.slot).toBe("main"));
  expect(paneFor(child)?.slot).toBe("secondary");
  await Promise.resolve();
  expect(
    fetchMock.mock.calls.filter((call) => String((call as unknown[])[0]).includes("/api/navigation/sessions/")).length,
  ).toBe(0);
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

test("opening /s/{ref} replaces unrelated main session instead of opening a secondary", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:existing" });
  const fetchMock = vi.fn((url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);

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

  expect(await screen.findByText("Page not found")).toBeTruthy();
  expect(screen.queryByText("No session open")).toBeNull();
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
// main slot. Driven through the REAL rail delete (menu, confirmation, POST,
// refetch) against the real AppShell + DockHost, because the re-open only
// happens with the route effect and the dock host both live.
test("deleting the session the address bar names lands on welcome instead of re-opening its pane", async () => {
  vi.stubGlobal("fetch", (url: string) => {
    if (url === "/api/sessions/local%3As1/delete") {
      return Promise.resolve(jsonResponse({ deleted: ["s1"], skipped: [] }));
    }
    return navFetchHandler(url);
  });

  const user = userEvent.setup();
  window.history.pushState({}, "", "/s/local:s1");
  installLocationForRoute("local:s1");
  render(<AppShell client={navClient()} />);
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

test("navigating from Settings to /new replaces Settings and clears secondary panes", async () => {
  workspaceStore.getState().openPane("settings", { section: "general" });
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
  navigationStore.setState({ mode: "v1" });

  const fetchMock = vi.fn((url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);
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
  const fetchMock = vi.fn((url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);
  workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
  workspaceStore.getState().openPane("doc", {
    session: "local:unrelated",
    path: "README.md",
    kind: "text",
  });

  window.history.pushState({}, "", "/s/local:child");

  installLocationForRoute("local:child");
  render(<AppShell client={new FakeClient("ready")} />);

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
    workspaceStore.getState().openPane("sessionActivity", { ref: "local:sub1" }, { slot: "secondary" });
  });

  await waitFor(() =>
    expect(workspaceStore.getState().focusedPaneId).toBe(
      workspaceStore.getState().panes.find((pane) => pane.type === "sessionActivity")?.id,
    ),
  );
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-a" });
});

test("a focused session panel does not invalidate a settled nested route", async () => {
  vi.stubGlobal("fetch", (url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return Promise.resolve(jsonResponse({}));
  });
  window.history.pushState({}, "", "/s/local:child");
  installLocationForRoute("local:child");
  render(<AppShell client={new FakeClient("ready")} />);
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
  navigationStore.setState({ mode: "v1" });

  window.history.pushState({}, "", "/s/local:child");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(paneFor("local:child")).toBeUndefined();
  installLocationForRoute("local:child");
  await waitFor(() => expect(paneFor("local:child")?.slot).toBe("secondary"));
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:child")?.id));
});

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
  vi.stubGlobal("fetch", (url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return Promise.resolve(jsonResponse({}));
  });
  window.history.pushState({}, "", "/s/local:child");
  installLocationForRoute("local:child");
  render(<AppShell client={new FakeClient("ready")} />);
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
  navigationStore.setState({ mode: "v1" });
  vi.stubGlobal("fetch", (url: string) =>
    url.includes("/api/navigation/sessions/")
      ? Promise.resolve(jsonResponse({}, 404))
      : Promise.resolve(jsonResponse({})),
  );

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={new FakeClient("ready")} />);

  act(() =>
    navigationStore.setState({
      resources: new Map(
        [...navigationStore.getState().resources].filter(([, resource]) => resource.key.kind !== "location"),
      ),
      mode: "v1",
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
  navigationStore.setState({ mode: "v1" });
  vi.stubGlobal("fetch", (url: string) =>
    url.includes("/api/navigation/sessions/")
      ? Promise.resolve(jsonResponse({}, 404))
      : Promise.resolve(jsonResponse({})),
  );

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={new FakeClient("ready")} />);

  act(() =>
    navigationStore.setState({
      resources: new Map(
        [...navigationStore.getState().resources].filter(([, resource]) => resource.key.kind !== "location"),
      ),
      mode: "v1",
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
  const fetchMock = vi.fn((url: string) => {
    if (url === NAV_URL) return Promise.resolve(jsonResponse(EMPTY_NAV_RESPONSE));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);

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
  // Single-pane mode never mounts RailHost (its own mount effect is the
  // usual /api/navigation/legacy trigger - see the file-level comment on RailHost's
  // rendering condition just above the JSX), so nothing here fetches the
  // tree on its own; a real boot's baseline fetch (initNotifications()'s
  // ensureLoaded(), module-scope, fires only once ever) already covers this
  // path in production. Mirrors "nested deep-link waits for successful tree
  // refresh..." above for the same reason.
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
  expect(screen.getByTestId("rail-search")).toBeTruthy();
});

// --- settings routing (this task) -------------------------------------

test("deep-linking to /settings/{section} opens the settings pane showing that section", async () => {
  window.history.pushState({}, "", "/settings/theme");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Theme" }).getAttribute("aria-current")).toBe("page");
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Theme"]);
});

test("navigating to /settings/{section} replaces main settings and removes secondary settings", async () => {
  workspaceStore.setState({
    panes: [
      {
        id: "settings-main",
        type: "settings",
        params: { section: "general" },
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

test("deep-linking to bare /settings opens the settings pane on its default (General) section", async () => {
  window.history.pushState({}, "", "/settings");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "General" }).getAttribute("aria-current")).toBe("page");
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
  window.history.pushState({}, "", "/settings/general");
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
  await waitFor(() => expect(screen.getByLabelText("Working directory").textContent).toContain("/home/me/app"));

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
  await waitFor(() => expect(screen.getByLabelText("Working directory").textContent).toContain("/home/other"));
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

// --- kata bbsv: a mobile deep link outlives the wait for the tree --------

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
    vi.fn(() => mediaQuery),
  );
  return (nextMobile: boolean) => {
    mobile = nextMobile;
    const event = { matches: mobile, media: mediaQuery.media } as MediaQueryListEvent;
    for (const listener of listeners) listener(event);
  };
}

// A /s/{ref} route cannot be placed until /api/navigation/legacy says whether the ref is
// nested (openRouteAsPane defers it), and no fetch can resolve inside the
// first commit - so on mobile the shell always spends a beat with the deep
// link parsed but unplaced. StackHost fills an empty stack with welcome and
// publishes the focused pane's URL, which used to overwrite the address bar
// with "/" during exactly that beat: the deep link was gone before the tree
// it was waiting for ever landed, and no later evener/tree/changed push could
// name it again.
test("mobile: a /s/{ref} deep link still opens once the tree lands, instead of being overwritten by welcome", async () => {
  navigationStore.setState({ mode: "v1" });
  installMobileViewport();

  window.history.pushState({}, "", "/s/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);

  // The mobile host settles on its welcome fallback while the location is
  // still unavailable, but must preserve the deep-link URL.
  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/s/local%3As1");

  installLocationForRoute("local:s1");

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
  await screen.findByText("Loading session panel…");
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));

  setMobile(true);
  expect(await screen.findByText("Loading session panel…")).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  // The scaffold header is display:none below 900px - StackHost's top-bar
  // Back is the VISIBLE mobile return path, wired to the parent session for
  // panel panes.
  expect(screen.getByRole("button", { name: "Back" })).toBeTruthy();

  setMobile(false);
  await waitFor(() => expect(getDockviewApi()).toBeNull());
  await waitFor(() => expect(getDockviewApi()?.panels.some((panel) => panel.id === panelId)).toBe(true));
  expect(workspaceStore.getState().focusedPaneId).toBe(panelId);
  expect(getDockviewApi()).not.toBeNull();

  setMobile(true);
  await screen.findByText("Loading session panel…");
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
  // Mobile never mounts RailHost (its own mount effect is the usual
  // /api/navigation/legacy trigger - see the file-level comment on RailHost's rendering
  // condition), so nothing here fetches the tree on its own; a real boot's
  // baseline fetch (initNotifications()'s ensureLoaded(), module-scope,
  // fires only once ever) already covers this path in production.
  await act(async () => {
    await Promise.resolve();
  });

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  await waitFor(() => expect(window.location.pathname).toBe("/thread/local:s1"));
  expect(document.querySelector("[data-single-pane]")).not.toBeNull();
});

// --- kata p5w9: one boot, one GET /api/navigation/legacy ------------------------------

// A desktop boot has TWO unconditional mount-time tree fetchers -
// initNotifications()'s baseline (run at AppShell.tsx's module evaluation, so
// it fires on every host, including the mobile one where no rail mounts) and
// the rail's own mount effect - and used to issue a full GET /api/navigation/legacy from
// each, milliseconds apart, for the same snapshot. Plus a third: AppShell
// publishes serverInfo through connectionStore once its connect() resolves,
// which the reconnect subscriber read as a new connection.
//
// The notifications engine is a module singleton already initialized by
// AppShell.tsx's own import, so a REAL boot is modelled by resetting and
// re-initializing it here - and it is deliberately left initialized
// afterwards, exactly as module evaluation leaves it for every other test in
// this file.
test("desktop boot does not issue a legacy whole-tree request", async () => {
  const fetchMock = vi.fn((url: string) => Promise.resolve(jsonResponse(url === NAV_URL ? EMPTY_NAV_RESPONSE : {})));
  vi.stubGlobal("fetch", fetchMock);

  resetNotificationsForTests();
  initNotifications();
  render(<AppShell client={new FakeClient("ready")} />);

  await screen.findByText("No session open");
  await waitFor(() => expect(navigationStore.getState().resources).not.toBeNull());
  await waitFor(() => expect(connectionStore.getState().serverInfo).toBeDefined());

  expect(fetchMock.mock.calls.filter(([url]) => url === NAV_URL)).toHaveLength(0);
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

  fireEvent.keyDown(screen.getByRole("button", { name: "General" }), { key: "Escape" });

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
