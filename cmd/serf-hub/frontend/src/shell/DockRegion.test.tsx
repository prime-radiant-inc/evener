import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { connectionStore } from "../stores/connection";
import { resetTreeStoreForTests } from "../stores/tree";
import { AppShell } from "./AppShell";
import { DockRegion, resetDockChunkForTests } from "./DockRegion";
import { loadDockHost } from "./dockHostChunk";
import { resetWorkspaceStoreForTests } from "./workspace";

// The DockHost chunk is a separate network request from index.html (345kB of
// JS + 104kB of CSS), so a hub restarting mid-load, a slow link, or a deploy
// that replaced the hashed filename all land on a rejected import() - the
// browser's own "Failed to fetch dynamically imported module". Replacing the
// loader is that failure with no network involved; the real dockview module
// never loads here, which also keeps these tests off dockview's ResizeObserver
// (kata 1s47, reproduced live against the built bundle at 771b016ea).
vi.mock("./dockHostChunk", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./dockHostChunk")>();
  return { ...actual, loadDockHost: vi.fn() };
});

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/DockHost-a1b2c3.js";

function StubDockHost() {
  return <p>dock host mounted</p>;
}

const EMPTY_TREE = {
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

function jsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    json: () => Promise.resolve(body),
  } as Response;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetTreeStoreForTests();
  vi.mocked(loadDockHost).mockReset();
  // The chunk is one shared lazy() payload per page load, so each test needs
  // its own - a payload that resolved (or rejected) in the last test would
  // never call this test's loader at all.
  resetDockChunkForTests();
  vi.stubGlobal("fetch", (url: string) => Promise.resolve(jsonResponse(url === "/api/tree" ? EMPTY_TREE : {})));
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  vi.unstubAllGlobals();
});

test("a rejected DockHost chunk degrades the dock region, never the whole shell", async () => {
  vi.mocked(loadDockHost).mockRejectedValue(new Error(CHUNK_ERROR));

  render(<AppShell client={new FakeClient("ready")} />);

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
