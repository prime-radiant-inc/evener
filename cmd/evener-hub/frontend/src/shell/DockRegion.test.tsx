import type { NavigationReadParams, NavigationReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { holdChunk } from "../lazyChunkTestUtils";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { connectionStore } from "../stores/connection";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { AppShell } from "./AppShell";
import { DockRegion, resetDockChunkForTests } from "./DockRegion";
import * as dockHostChunk from "./dockHostChunk";
import { type DockHostModule, resetDockHostLoaderForTests } from "./dockHostChunk";
import * as pageReload from "./pageReload";
import { resetWorkspaceStoreForTests } from "./workspace";

// The DockHost chunk is a separate network request from index.html (345kB of
// JS + 104kB of CSS), so a hub restarting mid-load, a slow link, or a deploy
// that replaced the hashed filename all land on a rejected import() - the
// browser's own "Failed to fetch dynamically imported module". Replacing the
// loader is that failure with no network involved; the real dockview module
// never loads here, which also keeps these tests off dockview's ResizeObserver
// (kata 1s47, reproduced live against the built bundle at 771b016ea).
//
// vi.spyOn on the namespace import replaces the loader in place: Vite's
// module-runner gives named imports a live getter into the dockHostChunk
// module object, so DockRegion.tsx's calls see the spy's current
// implementation. That lets each test script its own loader, and beforeEach
// points the spy back at the real one so no test's override reaches the next.
const realLoadDockHost = dockHostChunk.loadDockHost;
const loadDockHost = vi.spyOn(dockHostChunk, "loadDockHost");

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/DockHost-a1b2c3.js";

function StubDockHost() {
  return <p>dock host mounted</p>;
}

const EMPTY_NAVIGATION_MANIFEST = {
  generation_id: "test-generation",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

function scriptNavigationManifest(client: FakeClient): void {
  client.on("evener/navigation/read", (params: NavigationReadParams): NavigationReadResponse => {
    expect(params).toEqual({ resource: "manifest", representationVersion: 3 });
    return wireSnapshot(params, EMPTY_NAVIGATION_MANIFEST, '"test"', 1, "test-generation");
  });
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
  // notifications/index.ts keeps module state (its init guard, the
  // "reconnect" detector's sawReady, the attention baseline) for every test
  // in this file, and a test that connects a client straight to "ready" arms
  // that detector. Reset and re-init before each test's own fresh connect so
  // every test starts from the state a fresh module evaluation leaves:
  // engine started, seeded from the idle connection and reset navigation
  // store above, with nothing carried over from the previous test.
  resetNotificationsForTests();
  initNotifications();
  loadDockHost.mockReset();
  loadDockHost.mockImplementation(realLoadDockHost);
  // The chunk is one shared lazy() payload per page load, so each test needs
  // its own - a payload that resolved (or rejected) in the last test would
  // never call this test's loader at all.
  resetDockChunkForTests();
  resetDockHostLoaderForTests();
});

afterEach(() => {
  consoleErrorSpy.mockRestore();
  cleanup();
  window.history.pushState({}, "", "/");
  vi.unstubAllGlobals();
});

test("a rejected DockHost chunk degrades the dock region, never the whole shell", async () => {
  const chunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValue(chunk.promise);

  const client = new FakeClient("ready");
  scriptNavigationManifest(client);
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: "evener-appwire-v6",
    sourceId: "fake",
    features: {} as never,
    navigation: { version: 1, generationId: "test-generation", sequence: 0, readVersions: [3] },
  }));
  render(<AppShell client={client} />);
  await chunk.reject(new Error(CHUNK_ERROR));

  expect(screen.getByText("Couldn't load the workspace")).toBeTruthy();
  expect(screen.getByText(CHUNK_ERROR)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();

  // The rest of the shell is untouched: the rail (and with it every other
  // chrome element outside the dock) is still mounted, where before this
  // boundary the rethrown lazy() error emptied #root entirely. The failure
  // state also stands exactly where the workspace stood - a sibling of the
  // rail inside the workspace row - so the rail keeps its own width instead
  // of being that row's only child and stretching across the window.
  //
  // The rail is itself a lazy chunk behind a null-fallback Suspense
  // (rail/index.tsx), so it paints a tick after the dock failure state -
  // await its arrival rather than asserting it is already there.
  // Await the rail chunk first, THEN re-query the failure's parent: the
  // lazy rail's arrival commits above the failure, so a parent captured
  // before the await is stale and its contains() check flakes.
  await screen.findByTestId("rail-search");
  const failure = screen.getByText("Couldn't load the workspace").closest("[data-testid='empty-state']");
  // AppShell wraps DockRegion in a workspace column (host + status bar), so
  // ascend through both to the content row the rail stands in.
  const workspaceRow = failure?.closest("[data-testid='workspace-column']")?.parentElement;
  expect(workspaceRow?.contains(screen.getByTestId("rail-search"))).toBe(true);
});

test("mounts the host when its chunk arrives", async () => {
  const chunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValue(chunk.promise);

  render(<DockRegion />);
  await chunk.resolve({ DockHost: StubDockHost });

  expect(screen.getByText("dock host mounted")).toBeTruthy();
});

test("Retry fetches the chunk again and mounts the host on the second attempt", async () => {
  const firstChunk = holdChunk<DockHostModule>();
  const retryChunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValueOnce(firstChunk.promise).mockReturnValueOnce(retryChunk.promise);
  const user = userEvent.setup();

  render(<DockRegion />);
  await firstChunk.reject(new Error(CHUNK_ERROR));
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await retryChunk.resolve({ DockHost: StubDockHost });

  // Both halves of a retry, in one assertion each: the host it returns
  // replaces the failure state, and the second attempt asks the loader for
  // the cache-busted path proven to reach the network by the built-browser
  // probe. A second same-URL import does not reach Chrome's network stack.
  expect(screen.getByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
});

test("a successful retry is reused after DockRegion unmounts and remounts", async () => {
  const firstChunk = holdChunk<DockHostModule>();
  const retryChunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValueOnce(firstChunk.promise).mockReturnValueOnce(retryChunk.promise);
  const user = userEvent.setup();

  const first = render(<DockRegion />);
  await firstChunk.reject(new Error(CHUNK_ERROR));
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await retryChunk.resolve({ DockHost: StubDockHost });
  expect(screen.getByText("dock host mounted")).toBeTruthy();

  first.unmount();
  render(<DockRegion />);

  expect(screen.getByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});

test("a cache-busted retry that still names a stale hashed chunk offers a page reload", async () => {
  const firstChunk = holdChunk<DockHostModule>();
  const retryChunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValueOnce(firstChunk.promise).mockReturnValueOnce(retryChunk.promise);
  const reload = vi.spyOn(pageReload, "reloadPage").mockImplementation(() => {});
  onTestFinished(() => reload.mockRestore());
  const user = userEvent.setup();

  render(<DockRegion />);
  await firstChunk.reject(new Error(CHUNK_ERROR));
  expect(screen.getByText("Couldn't load the workspace")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await retryChunk.reject(new Error(CHUNK_ERROR));
  await user.click(screen.getByRole("button", { name: "Reload page" }));

  expect(reload).toHaveBeenCalledTimes(1);
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});

test("an ordinary retry failure does not prescribe a page reload", async () => {
  const firstChunk = holdChunk<DockHostModule>();
  const retryChunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost).mockReturnValueOnce(firstChunk.promise).mockReturnValueOnce(retryChunk.promise);
  const user = userEvent.setup();

  render(<DockRegion />);
  await firstChunk.reject(new Error("workspace module initialization failed"));
  expect(screen.getByText("Couldn't load the workspace")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await retryChunk.reject(new Error("workspace module initialization failed"));

  expect(screen.getByText("workspace module initialization failed")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();
});

test("a chunk still in flight leaves a visible workspace placeholder beside the rail", async () => {
  // Never settles: a request the hub never answers, with no wall clock in it.
  vi.mocked(loadDockHost).mockReturnValue(new Promise(() => {}));

  render(<AppShell client={new FakeClient("ready")} />);

  // Same lazy-rail race as the rejected-chunk test above: the rail chunk
  // arrives on its own tick, so await it instead of requiring it synchronously.
  // Await the rail chunk first, THEN re-query the loading placeholder's
  // parent: a parent captured before the await is stale and its contains()
  // check flakes.
  await screen.findByTestId("rail-search");
  const loading = screen.getByText("Loading the workspace…").closest("[data-testid='empty-state']");
  const workspaceRow = loading?.closest("[data-testid='workspace-column']")?.parentElement;
  expect(workspaceRow?.contains(screen.getByTestId("rail-search"))).toBe(true);
  // The rail chunk arrives with its own empty-state Retry (a bare
  // FakeClient("ready") scripts no navigation manifest, so the rail shows
  // "Couldn't load sessions" with a Retry beside the dock's own loading
  // placeholder), so scope the dock Retry to the loading empty-state rather
  // than querying the whole shell.
  // No jest-dom matchers in this tree (vite.config.ts setupFiles is empty),
  // so read the scoped button's text directly.
  expect(loading?.querySelector("button")?.textContent).toBe("Retry");
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
  // An unanswered request is not a failure and must not retry on its own.
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(1);
});

test("Retry abandons a chunk still in flight and mounts a fresh attempt", async () => {
  const retryChunk = holdChunk<DockHostModule>();
  vi.mocked(loadDockHost)
    .mockReturnValueOnce(new Promise(() => {}))
    .mockReturnValueOnce(retryChunk.promise);
  const user = userEvent.setup();

  render(<DockRegion />);
  expect(screen.getByText("Loading the workspace…")).toBeTruthy();
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await retryChunk.resolve({ DockHost: StubDockHost });

  expect(screen.getByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost).mock.calls).toEqual([[false], [true]]);
});
