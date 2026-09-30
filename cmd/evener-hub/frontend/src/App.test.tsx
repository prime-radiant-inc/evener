import type { NavigationReadParams, NavigationReadResponse } from "@evener/appwire-client";
import { AppwireClient } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "./notifications";
import { StubResizeObserver } from "./resizeObserverTestUtils";
import { AppShell } from "./shell/AppShell";
import { resetWorkspaceStoreForTests } from "./shell/workspace";
import { installLocalStorage, MemoryStorage } from "./storageTestUtils";
import { connectionStore } from "./stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "./stores/navigation/store";
import { resetThreadsStoreForTests } from "./stores/threads";
import { resetToastStoreForTests } from "./widgets/toast/store";

// AppShell's default createClient constructs a REAL AppwireClient (no test
// client is injected anywhere in this file), which dials a real jsdom
// WebSocket and, once "closed", runs its own real setTimeout-based
// exponential-backoff reconnect loop (protocol/client.ts) that close() is
// the only thing that cancels. Every route render in this file (including
// beforeAll's own warmRoute calls, which mount <App/> multiple times with
// no close() in between) constructs its own such client; connectionStore
// only ever mirrors the MOST RECENT one, so closing just "whatever's
// current" leaves every earlier route's client redialing through the
// file's later tests. This subscription records every distinct real client
// this file ever sees, so closeAllCreatedClients can close them all, not
// just the last.
const allCreatedClients = new Set<AppwireClient>();
connectionStore.subscribe((state) => {
  if (state.client instanceof AppwireClient) allCreatedClients.add(state.client);
});

// closeAllCreatedClients closes every real client THIS file's own routes
// ever constructed (see allCreatedClients above), not just whichever one
// connectionStore currently mirrors. It nulls connectionStore's own
// reference FIRST: connection.ts's client->store state mirror only
// republishes while `connectionStore.getState().client === client` still
// holds (its own guard), and close() synchronously fires that client's
// "closed" state change. Closing while the reference is still current lets
// that mirror republish state through it, re-triggering threads.ts's
// connectionStore.subscribe -> rewireClient(client) and re-wiring
// notification/ready handlers onto a client this store is about to
// discard - clearing the reference first makes the mirror's own guard skip
// republishing, so close() cannot re-arm rewireClient.
function closeAllCreatedClients(): void {
  if (connectionStore.getState().client instanceof AppwireClient) {
    connectionStore.setState({ client: null });
  }
  for (const client of allCreatedClients) client.close();
  allCreatedClients.clear();
}

let App: typeof import("./App").App;

const escapedFetches = vi.hoisted(() => {
  const calls: unknown[] = [];
  vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) => {
    calls.push(args[0]);
    throw new Error(`escaped fetch before App.test fake: ${String(args[0])}`);
  });
  return calls;
});

const EMPTY_NAV_RESPONSE = {
  generation_id: "test-generation",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function navigationReadResponse(generationId: string): NavigationReadResponse {
  return wireSnapshot(
    { resource: "manifest", representationVersion: 3 },
    { ...EMPTY_NAV_RESPONSE, generation_id: generationId },
    '"test"',
    1,
    generationId,
  );
}

function stubDeferredNavigationRead(client: FakeClient): { requested: Promise<void>; release: () => void } {
  let signalRequest!: () => void;
  let releaseResponse!: () => void;
  const requested = new Promise<void>((resolve) => {
    signalRequest = resolve;
  });
  const response = new Promise<void>((resolve) => {
    releaseResponse = resolve;
  });
  client.on("evener/navigation/read", (params) => {
    if (params.resource !== "manifest") throw new Error(`unexpected navigation resource: ${params.resource}`);
    signalRequest();
    return response.then(() => navigationReadResponse("test-generation"));
  });
  return {
    requested,
    release: () => {
      releaseResponse();
    },
  };
}

// Renders a route to completion so both halves of its lazy-loading cost are
// already paid by the time a test measures it. The module cache is only the
// first half: React.lazy keeps a payload of its own that stays uninitialized
// until React first RENDERS the component, so a warm module cache still
// leaves the first render suspending. The default route crosses two nested
// boundaries (AppShell's lazy DockHost, then PaneHost's lazy Welcome).
// Rendering inside an awaited act lets each reveal commit as soon as its
// already-imported chunk resolves; outside act, react-dom holds every reveal
// for FALLBACK_THROTTLE_MS (300ms, react-dom 19.2) on a real timer.
// The landmark wait gets WARM_ROUTE_TRIPWIRE_MS rather than the 1000ms findBy
// default: a warm-up has no responsiveness bar to hold, so the deadline here
// is a tripwire for a hung render.
const WARM_ROUTE_TRIPWIRE_MS = 10_000;

async function warmRoute(path: string, text: string | RegExp): Promise<void> {
  window.history.pushState({}, "", path);
  await act(async () => {
    render(<App />);
  });
  await screen.findByText(text, undefined, { timeout: WARM_ROUTE_TRIPWIRE_MS });
  cleanup();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
}

// Await every lazily-loaded route's module ONCE up front so React.lazy
// resolves from a warm module cache. The slow part of lazy-loading in a
// full parallel vitest run is the transform/import work, which is an
// awaitable completion — not something to race with a widened findBy
// deadline. A genuinely broken module fails this await with its real error
// instead of a timeout.
beforeAll(async () => {
  // The default route mounts AppShell -> DockHost -> real dockview-react, which
  // needs a ResizeObserver (jsdom has none, verified via a live probe) and
  // localStorage (storageTestUtils' MemoryStorage).
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  await import("./dev/WidgetGallery");
  await import("./dev/DevHarness");
  await import("./panes/welcome/Welcome");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path); the default-route test
  // below renders through AppShell -> DockHost, same reasoning as the
  // three imports above.
  await import("./shell/DockHost");
  ({ App } = await import("./App"));

  // Then render each route once, for the React.lazy half of the cost — see
  // warmRoute above. Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", "No session open");
  await warmRoute("/dev/widgets", /widget gallery/i);
  await warmRoute("/dev/harness", /connection:/i);
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  closeAllCreatedClients();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  localStorage.clear();
  // The dev widget gallery route pushes real toasts through the
  // module-singleton toast store (widgets/toast/store.ts), and unmounting
  // cancels their timers without removing them; reset so no test starts
  // with another test's toasts queued.
  resetToastStoreForTests();
  // notifications/index.ts keeps module state (its init guard, the
  // "reconnect" detector's sawReady, the attention baseline) for every test
  // in this file, and a test that connects a client straight to "ready" arms
  // that detector. Reset and re-init before each test's own fresh connect so
  // every test starts from the state a fresh module evaluation leaves:
  // engine started, seeded from the idle connection and reset navigation
  // store above, with nothing carried over from the previous test.
  resetNotificationsForTests();
  initNotifications();
});

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
  // Each test above renders <App/> with no test client injected, so every
  // one constructs a fresh real AppwireClient and wires it into
  // connectionStore - which threads.ts's module-scope
  // connectionStore.subscribe (rewireClient) reacts to. closeAllCreatedClients
  // nulls connectionStore's client reference before closing each one (see its
  // own comment on why the order matters - closing first would let
  // connection.ts's still-guarded state mirror republish through the
  // about-to-be-discarded client and re-arm threads.ts's wiredClient).
  // resetThreadsStoreForTests runs last so nothing re-arms wiredClient
  // afterward.
  closeAllCreatedClients();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

test("renders the app shell (welcome pane) at the default route", async () => {
  render(<App />);
  expect(await screen.findByText("No session open")).toBeTruthy();
  // DevHarness moved out of the default route onto its own /dev/harness
  // route (see the next test) - it must not also render here.
  expect(screen.queryByText(/connection:/i)).toBeNull();
});

test("initiates and settles the welcome navigation load without an error", async () => {
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v6",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "test-generation", sequence: 0, readVersions: [3] },
  }));
  const navRead = stubDeferredNavigationRead(client);
  render(<AppShell client={client} />);
  await act(async () => {
    await navRead.requested;
    navRead.release();
    await navigationStore.getState().loadManifest();
  });
  const manifest = navigationStore.getState().manifest;
  expect(manifest).not.toBeNull();
  expect(manifest?.error).toBeNull();
});

test("AppShell's injected v2 handshake selects navigation through AppWire", async () => {
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v6",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "app-generation", sequence: 0, readVersions: [3] },
  }));
  const calls: NavigationReadParams[] = [];
  client.on("evener/navigation/read", (params) => {
    calls.push(params);
    if (params.resource !== "manifest") throw new Error(`unexpected navigation resource: ${params.resource}`);
    return { ...navigationReadResponse("app-generation"), etag: '"app-manifest"' };
  });

  const connect = vi.spyOn(client, "connect");
  const loadManifest = vi.spyOn(navigationStore.getState(), "loadManifest");
  try {
    render(<AppShell client={client} />);
    expect(connect).toHaveBeenCalled();
    const connectionResult = connect.mock.results[0];
    if (connectionResult?.type !== "return") throw new Error("AppShell did not start the handshake");
    await act(async () => {
      await connectionResult.value;
      expect(loadManifest).toHaveBeenCalled();
      const manifestResult = loadManifest.mock.results[0];
      if (manifestResult?.type !== "return") throw new Error("AppShell did not start the manifest load");
      await manifestResult.value;
    });
    await vi.waitFor(() => expect(calls).toEqual([{ resource: "manifest", representationVersion: 3 }]));

    expect(navigationStore.getState().mode).toBe("v3");
    expect(calls).toEqual([{ resource: "manifest", representationVersion: 3 }]);
  } finally {
    connect.mockRestore();
    loadManifest.mockRestore();
  }
});

test("does not escape a navigation request before the test fake is installed", () => {
  expect(escapedFetches).toHaveLength(0);
});

test("renders the dev widget gallery at /dev/widgets", async () => {
  window.history.pushState({}, "", "/dev/widgets");
  render(<App />);
  // Route warmed in beforeAll, so this waits only on this render's own
  // work — no module transform, no Suspense reveal throttle.
  expect(await screen.findByText(/widget gallery/i)).toBeTruthy();
});

test("renders the dev harness at /dev/harness", async () => {
  window.history.pushState({}, "", "/dev/harness");
  render(<App />);
  // Route warmed in beforeAll, so this waits only on this render's own
  // work — no module transform, no Suspense reveal throttle.
  expect(await screen.findByText(/connection:/i)).toBeTruthy();
});
