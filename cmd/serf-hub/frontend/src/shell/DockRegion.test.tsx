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
vi.mock("./dockHostChunk", () => ({ loadDockHost: vi.fn() }));

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/DockHost-a1b2c3.js";

function StubDockHost() {
  return <p>dock host mounted</p>;
}

const EMPTY_TREE = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  favorites: [],
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

  // Both halves of a retry, in one assertion each: the chunk really is
  // requested a second time (React.lazy would otherwise rethrow its cached
  // rejection without going near the network), and the host it returns
  // replaces the failure state.
  expect(await screen.findByText("dock host mounted")).toBeTruthy();
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(2);
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
});

test("a chunk still in flight holds the Suspense fallback, and is not treated as a failure", () => {
  // Never settles: a request the hub never answers, with no wall clock in it.
  vi.mocked(loadDockHost).mockReturnValue(new Promise(() => {}));

  const { container } = render(<DockRegion />);

  // fallback={null}, so the region renders nothing at all while its chunk is
  // in flight. That silent blank is the stall half of kata 1s47 and is a
  // deliberately open design question (deadline? progress? retry?) - what is
  // pinned here is only containment: an unanswered request is not a failure,
  // must not trip the failure state, and must not fetch again on its own.
  expect(container.innerHTML).toBe("");
  expect(screen.queryByText("Couldn't load the workspace")).toBeNull();
  expect(vi.mocked(loadDockHost)).toHaveBeenCalledTimes(1);
});
