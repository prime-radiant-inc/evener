import { cleanup, render, screen } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "./notifications";
import { AppwireClient } from "./protocol/client";
import { resetWorkspaceStoreForTests } from "./shell/workspace";
import { connectionStore } from "./stores/connection";
import { resetThreadsStoreForTests } from "./stores/threads";
import { resetTreeStoreForTests } from "./stores/tree";

// AppShell's default createClient constructs a REAL AppwireClient (no test
// client is injected anywhere in this file), which dials a real jsdom
// WebSocket and, once "closed", runs its own real setTimeout-based
// exponential-backoff reconnect loop (protocol/client.ts) that close() is
// the only thing that cancels. Every route render in this file (including
// beforeAll's own warmRoute calls, which mount <App/> multiple times with
// no close() in between) constructs its own such client; connectionStore
// only ever mirrors the MOST RECENT one, so closing just "whatever's
// current" leaves every earlier route's client orphaned with a still-armed
// reconnect timer for the rest of the worker's life under isolate:false.
// This subscription records every distinct real client this file ever
// sees, so allCreatedClients (closed in the afterAll below) can close them
// all, not just the last.
const allCreatedClients = new Set<AppwireClient>();
const unsubscribeClientTracker = connectionStore.subscribe((state) => {
  if (state.client instanceof AppwireClient) allCreatedClients.add(state.client);
});

// closeStaleClient closes whatever's currently wired into connectionStore -
// for the one call below that runs before this file's own client tracker
// (allCreatedClients) has seen anything, i.e. a client left over from an
// earlier file in the shared isolate:false worker. Nulls connectionStore's
// own reference to it FIRST: connection.ts's client->store state mirror
// only republishes while `connectionStore.getState().client === client`
// still holds (its own guard), and close() synchronously fires that
// client's "closed" state change. Closing while the reference is still
// current lets that mirror republish state through it, re-triggering
// threads.ts's connectionStore.subscribe -> rewireClient(client) and
// re-wiring notification/ready handlers onto a client this store is about
// to discard - clearing the reference first makes the mirror's own guard
// skip republishing, so close() cannot re-arm rewireClient.
function closeStaleClient(): void {
  const client = connectionStore.getState().client;
  if (!(client instanceof AppwireClient)) return;
  connectionStore.setState({ client: null });
  client.close();
}

// closeAllCreatedClients closes every real client THIS file's own routes
// ever constructed (see allCreatedClients above), not just whichever one
// connectionStore currently mirrors - same null-before-close ordering as
// closeStaleClient, for the same reason.
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

// The default route mounts AppShell -> DockHost -> real dockview-react, which
// needs a ResizeObserver (jsdom has none) and localStorage (Node 26's own
// global `localStorage` accessor shadows jsdom's real one without
// --localstorage-file) - both verified via a live probe; see
// shell/DockHost.test.tsx's own comments for the full detail on each.
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

const EMPTY_TREE_RESPONSE = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  pin_sections: [],
  projects: [],
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

function stubTreeFetch(): void {
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    if (input !== "/api/tree" || (init?.method ?? "GET") !== "GET" || init?.credentials !== "same-origin") {
      throw new Error(`unexpected fetch in App.test: ${String(input)}`);
    }
    return Promise.resolve(
      new Response(JSON.stringify(EMPTY_TREE_RESPONSE), { headers: { "Content-Type": "application/json" } }),
    );
  });
}

// Renders a route to completion so both halves of its lazy-loading cost are
// already paid by the time a test measures it. The module cache is only the
// first half: React.lazy keeps a payload of its own that stays uninitialized
// until React first RENDERS the component, so a warm module cache still
// leaves the first render suspending, committing its Suspense fallback, and
// then waiting out react-dom's FALLBACK_THROTTLE_MS (300ms, react-dom 19.2)
// before it will commit the revealed content — a flicker guard that is pure
// wall clock and does not shrink on a fast machine. The default route
// crosses two nested boundaries (AppShell's lazy DockHost, then PaneHost's
// lazy Welcome), so ~600ms of that throttle would otherwise land inside a
// findBy budget that defaults to 1000ms, leaving each assertion racing the
// machine for what's left. Measured here: a route costs ~635ms the first
// time it renders and ~20ms every time after.
// The landmark wait gets WARM_ROUTE_TRIPWIRE_MS rather than the 1000ms default
// named above. Moving the throttle out of the per-test assertion windows was
// only half the job: the warm-up's own findBy still raced that same default,
// with ~635ms of it already spent. A warm-up has no responsiveness bar to hold,
// and the throttle publishes no completion signal to await, so the deadline
// here is a tripwire for a hung render.
const WARM_ROUTE_TRIPWIRE_MS = 10_000;

async function warmRoute(path: string, text: string | RegExp): Promise<void> {
  window.history.pushState({}, "", path);
  render(<App />);
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
  globalThis.ResizeObserver = StubResizeObserver;
  // @ts-expect-error MemoryStorage deliberately implements only the Storage
  // methods DockHost.tsx actually calls (getItem/setItem/removeItem/clear),
  // not length/key() - see DockHost.test.tsx's own MemoryStorage comment.
  globalThis.localStorage = new MemoryStorage();
  // connectionStore has no resetXForTests helper (see this file's other
  // stores) - every other file that touches it resets it inline in its own
  // beforeEach/beforeAll instead; this warm-up render needs a clean slate too,
  // or a client left "ready"/"closed" by an earlier file in the shared
  // isolate:false worker renders something other than the welcome pane's
  // "No session open" the warmRoute below asserts on.
  closeStaleClient();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  stubTreeFetch();
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
  resetTreeStoreForTests();
  closeAllCreatedClients();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  localStorage.clear();
  stubTreeFetch();
});

afterEach(() => {
  cleanup();
  resetTreeStoreForTests();
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
  // AppShell.tsx calls initNotifications() at MODULE SCOPE (guarded by its
  // own `if (initialized) return`), so the FIRST render anywhere in this
  // worker that imports AppShell - unavoidably, every test in this file -
  // installs notifications/index.ts's own connectionStore.subscribe (its
  // "reconnect" detector, `sawReady`) for the rest of the worker's life.
  // Left un-reset, a LATER file's own client connecting straight to "ready"
  // (e.g. `new FakeClient("ready")`) reads as a "reconnect" against this
  // leftover `sawReady=true`, firing an extra, unexpected
  // treeStore.refresh() into that file's own fetch-call assertions.
  //
  // AppShell.tsx's module-scope initNotifications() call only ever fires
  // once per worker (its own "only once" guard), so leaving it reset would
  // leave the engine permanently uninitialized for the rest of this
  // isolate:false worker - so it is re-run immediately below, restoring the
  // same state a fresh module evaluation would have left (kata p5w9's
  // identical pattern in AppShell.test.tsx; see notifications/index.ts's own
  // reset comment). This pair runs LAST, after connectionStore and treeStore
  // are already back to idle/null above: initNotifications() seeds its
  // `sawReady`/baseline snapshot from whatever those stores hold AT THIS
  // MOMENT, and seeding from a still-"ready" connectionStore (as this test's
  // own render left it moments ago) would wrongly arm the very "reconnect"
  // detector this reset exists to neutralize - reading the NEXT file's first
  // real connect as a reconnect and firing a spurious treeStore.refresh()
  // into ITS fetch-call assertions instead.
  resetNotificationsForTests();
  initNotifications();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

afterAll(() => {
  closeAllCreatedClients();
  unsubscribeClientTracker();
});

test("renders the app shell (welcome pane) at the default route", async () => {
  render(<App />);
  expect(await screen.findByText("No session open")).toBeTruthy();
  // DevHarness moved out of the default route onto its own /dev/harness
  // route (see the next test) - it must not also render here.
  expect(screen.queryByText(/connection:/i)).toBeNull();
});

test("does not show a tree load error while rendering the welcome pane", async () => {
  render(<App />);
  await screen.findByText("No session open");
  // Await the sidebar's SETTLED success state before asserting the error is
  // absent - the welcome pane renders independently of the tree fetch, so
  // asserting straight away could pass while the refresh was still in
  // flight and about to fail.
  await screen.findByText("No sessions yet");
  expect(screen.queryByText("Couldn't load sessions")).toBeNull();
});

test("does not escape a tree fetch before the test fake is installed", () => {
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
