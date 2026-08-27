import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { FakeClient } from "../protocol/testing/fakeClient";
import { connectionStore } from "../stores/connection";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { AppShell } from "./AppShell";
import { DockRegion, resetDockChunkForTests } from "./DockRegion";
import * as dockHostChunk from "./dockHostChunk";
import { resetDockHostLoaderForTests } from "./dockHostChunk";
import { resetWorkspaceStoreForTests } from "./workspace";

// The DockHost chunk is a separate network request from index.html (345kB of
// JS + 104kB of CSS), so a hub restarting mid-load, a slow link, or a deploy
// that replaced the hashed filename all land on a rejected import() - the
// browser's own "Failed to fetch dynamically imported module". Replacing the
// loader is that failure with no network involved; the real dockview module
// never loads here, which also keeps these tests off dockview's ResizeObserver
// (kata 1s47, reproduced live against the built bundle at 771b016ea).
//
// A hoisted vi.mock("./dockHostChunk", ...) here would swap the module in the
// shared registry - under isolate:false that registry is shared by every file
// in the worker, and whichever file (this one, or AppShell.test.tsx via
// AppShell.tsx -> DockRegion.tsx -> dockHostChunk) happens to instantiate
// DockRegion.tsx FIRST in the whole worker's lifetime permanently fixes its
// closure over loadDockHost to whatever was in effect at that moment - a
// vi.mock registered afterward cannot retroactively change an
// already-instantiated module's own binding, so the leak direction flips
// unpredictably depending on unrelated ordering (confirmed empirically:
// swapping which file ran first flipped which one failed). vi.spyOn on the
// namespace import below instead MUTATES the shared dockHostChunk module
// object's own `loadDockHost` property in place - Vite's module-runner gives
// named imports a live getter into that same object, so DockRegion.tsx's
// calls see the spy's current implementation regardless of when it was
// instantiated, and mockRestore() in afterEach cleanly hands the real
// function back for whatever file runs next.
const realLoadDockHost = dockHostChunk.loadDockHost;
const loadDockHost = vi.spyOn(dockHostChunk, "loadDockHost");

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/DockHost-a1b2c3.js";

function StubDockHost() {
  return <p>dock host mounted</p>;
}

const EMPTY_NAV_RESPONSE = {
  generation_id: "test-generation",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function jsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    json: () => Promise.resolve(body),
  } as Response;
}

// Suppress console.error noise from React's error-boundary logging during
// tests that deliberately trigger chunk-load failures. The errors are
// expected; the boundary catches them. Without this, the test output is
// polluted with React's "The above error occurred in one of your React
// components" stack traces. Matched on React's own stable
// componentDidCatch format string plus its fixed boundary-recovery
// sentence, rather than blanket-silenced, so any *other* console.error a
// regression here might produce still reaches real console.error and
// stays visible in test output.
const REACT_ERROR_BOUNDARY_FORMAT = "%o\n\n%s\n\n%s\n";
const REACT_ERROR_BOUNDARY_PREFACE = "The above error occurred in one of your React components.";
const realConsoleError = console.error.bind(console);
let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  consoleErrorSpy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
    if (args[0] === REACT_ERROR_BOUNDARY_FORMAT && args[2] === REACT_ERROR_BOUNDARY_PREFACE) {
      return;
    }
    realConsoleError(...args);
  });
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  loadDockHost.mockReset();
  loadDockHost.mockImplementation(realLoadDockHost);
  // The chunk is one shared lazy() payload per page load, so each test needs
  // its own - a payload that resolved (or rejected) in the last test would
  // never call this test's loader at all.
  resetDockChunkForTests();
  resetDockHostLoaderForTests();
  vi.stubGlobal("fetch", (url: string) => {
    if (url === "/api/navigation") {
      return Promise.resolve(
        new Response(JSON.stringify(EMPTY_NAV_RESPONSE), {
          headers: {
            "Content-Type": "application/json",
            etag: '"test"',
            "X-Evener-Navigation-Generation": "test-generation",
            "X-Evener-Navigation-Revision": "1",
          },
        }),
      );
    }
    return Promise.resolve(jsonResponse({}));
  });
});

afterEach(() => {
  consoleErrorSpy.mockRestore();
  cleanup();
  window.history.pushState({}, "", "/");
  // Whichever override the LAST test set (mockRejectedValue/mockResolvedValue/
  // mockResolvedValueOnce...) would otherwise still be armed on this shared
  // spy for the next file in the worker that calls the real loadDockHost -
  // see this file's own comment on the vi.spyOn call above.
  loadDockHost.mockReset();
  loadDockHost.mockImplementation(realLoadDockHost);
  // Rendering <AppShell/> above calls notifications/index.ts's
  // initNotifications() at module scope (guarded by its own "only once"
  // flag), wiring its reconnect detector to whichever FakeClient this file
  // connected. Left unreset, that detector's stale "sawReady" flag makes a
  // later file's own fresh ready-client connect read as a spurious
  // reconnect - see App.test.tsx's identical reset and its own comment on
  // stores/tree.test.ts's dependent assertion.
  //
  // AppShell.tsx's module-scope initNotifications() call only ever fires
  // once per worker (its own "only once" guard), so leaving it reset would
  // leave the engine permanently uninitialized for the rest of this
  // isolate:false worker - so it is re-run immediately below, restoring the
  // same state a fresh module evaluation would have left (kata p5w9's
  // identical pattern in AppShell.test.tsx). initNotifications() seeds its
  // `sawReady`/baseline snapshot from whatever connectionStore/navigationStore
  // hold AT THIS MOMENT, so both are forced back to their neutral
  // pre-render values FIRST (this file's own beforeEach does the same for
  // the NEXT test in this file; nothing else does it for the NEXT FILE) -
  // seeding from a still-"ready" connectionStore (as this test's own render
  // left it moments ago) would wrongly arm the "reconnect" detector this
  // reset exists to neutralize, exactly the failure mode App.test.tsx's own
  // comment above describes. Run before vi.unstubAllGlobals() below so
  // initNotifications()'s baseline ensureLoaded() fetch (navigationStore's manifest is
  // null again below) still hits this file's own beforeEach fetch stub
  // instead of a real network call.
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  resetNotificationsForTests();
  initNotifications();
  vi.unstubAllGlobals();
});

test("a rejected DockHost chunk degrades the dock region, never the whole shell", async () => {
  vi.mocked(loadDockHost).mockRejectedValue(new Error(CHUNK_ERROR));

  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "test-generation", sequence: 0 },
  }));
  render(<AppShell client={client} />);

  expect(await screen.findByText("Couldn't load the workspace")).toBeTruthy();
  expect(screen.getByText(CHUNK_ERROR)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();

  // The rest of the shell is untouched: the rail (and with it every other
  // chrome element outside the dock) is still mounted, where before this
  // boundary the rethrown lazy() error emptied #root entirely. The failure
  // state also stands exactly where the workspace stood - a sibling of the
  // rail inside the workspace row - so the rail keeps its own width instead
  // of being that row's only child and stretching across the window.
  const failure = screen.getByText("Couldn't load the workspace").closest("[data-testid='empty-state']");
  const workspaceRow = failure?.parentElement;
  expect(workspaceRow?.contains(screen.getByTestId("rail-search"))).toBe(true);
});

test("mounts the host when its chunk arrives", async () => {
  vi.mocked(loadDockHost).mockResolvedValue({ DockHost: StubDockHost });

  render(<DockRegion />);

  expect(await screen.findByText("dock host mounted")).toBeTruthy();
});

test("Retry fetches the chunk again and mounts the host on the second attempt", async () => {
  vi.mocked(loadDockHost)
    .mockRejectedValueOnce(new Error(CHUNK_ERROR))
    .mockResolvedValueOnce({ DockHost: StubDockHost });
  const user = userEvent.setup();

  render(<DockRegion />);
  await screen.findByText("Couldn't load the workspace");
  await user.click(screen.getByRole("button", { name: "Retry" }));

  // Both halves of a retry, in one assertion each: the host it returns
  // replaces the failure state, and the second attempt asks the loader for
  // the cache-busted path proven to reach the network by the built-browser
  // probe. A second same-URL import does not reach Chrome's network stack.
  expect(await screen.findByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
});

test("a successful retry is reused after DockRegion unmounts and remounts", async () => {
  vi.mocked(loadDockHost)
    .mockRejectedValueOnce(new Error(CHUNK_ERROR))
    .mockResolvedValueOnce({ DockHost: StubDockHost });
  const user = userEvent.setup();

  const first = render(<DockRegion />);
  await screen.findByText("Couldn't load the workspace");
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("dock host mounted")).toBeTruthy();

  first.unmount();
  render(<DockRegion />);

  expect(await screen.findByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});

test("a cache-busted retry that still names a stale hashed chunk offers a page reload", async () => {
  vi.mocked(loadDockHost).mockRejectedValue(new Error(CHUNK_ERROR));
  const reload = vi.fn();
  vi.stubGlobal("location", { ...window.location, reload });
  const user = userEvent.setup();

  render(<DockRegion />);
  await screen.findByText("Couldn't load the workspace");
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await user.click(await screen.findByRole("button", { name: "Reload page" }));

  expect(reload).toHaveBeenCalledTimes(1);
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});

test("an ordinary retry failure does not prescribe a page reload", async () => {
  vi.mocked(loadDockHost).mockRejectedValue(new Error("workspace module initialization failed"));
  const user = userEvent.setup();

  render(<DockRegion />);
  await screen.findByText("Couldn't load the workspace");
  await user.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("workspace module initialization failed")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();
});

test("a chunk still in flight leaves a visible workspace placeholder beside the rail", () => {
  // Never settles: a request the hub never answers, with no wall clock in it.
  vi.mocked(loadDockHost).mockReturnValue(new Promise(() => {}));

  render(<AppShell client={new FakeClient("ready")} />);

  const loading = screen.getByText("Loading the workspace…").closest("[data-testid='empty-state']");
  const workspaceRow = loading?.parentElement;
  expect(workspaceRow?.contains(screen.getByTestId("rail-search"))).toBe(true);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
  // An unanswered request is not a failure and must not retry on its own.
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(1);
});

test("Retry abandons a chunk still in flight and mounts a fresh attempt", async () => {
  vi.mocked(loadDockHost)
    .mockReturnValueOnce(new Promise(() => {}))
    .mockResolvedValueOnce({ DockHost: StubDockHost });
  const user = userEvent.setup();

  render(<DockRegion />);
  expect(screen.getByText("Loading the workspace…")).toBeTruthy();
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});
