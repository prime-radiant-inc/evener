import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { initNotifications, resetNotificationsForTests } from "../notifications";
import { AppwireClient } from "../protocol/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InitializeResponse, ThreadStartResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { resetTreeStoreForTests, treeStore } from "../stores/tree";
import { AppShell } from "./AppShell";
import { DockHost } from "./DockHost";
import { paletteStore } from "./palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// Matches DockHost.tsx's own LAYOUT_STORAGE_KEY exactly (not exported - a
// deliberately internal implementation detail; duplicated here the same
// way DockHost.test.tsx's own LAYOUT_KEY is).
const LAYOUT_KEY = "serf.workspace.layout.v2";

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
const TREE_RESPONSE_WITH_NESTED_SESSION = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  favorites: [],
  projects: [
    {
      key: "proj1",
      name: "prime-radiant",
      working_dir: "/home/user/prime-radiant",
      default_expanded: true,
      sessions: [TREE_SESSION],
    },
  ],
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

const TREE_RESPONSE_WITH_OWNER_AND_CHILD = {
  ...TREE_RESPONSE_WITH_NESTED_SESSION,
  projects: [
    {
      ...TREE_RESPONSE_WITH_NESTED_SESSION.projects[0],
      sessions: [
        {
          ...TREE_SESSION,
          row_id: "project:proj1:local:owner",
          ref: "local:owner",
          session_id: "owner",
          title: "Owner session",
          children: [
            {
              ...TREE_SESSION.children[0],
              row_id: "project:proj1:local:child",
              ref: "local:child",
              session_id: "child",
              title: "Child session",
            },
          ],
        },
      ],
    },
  ],
};

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
      serf: { ref, capabilities: THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    turn: { id: "turn_1", itemsView: "full", status: "idle" },
  };
}

const paneFor = (ref: string) =>
  workspaceStore.getState().panes.find((p) => (p.params as { ref?: string }).ref === ref);

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
  render(<AppShell client={new FakeClient("ready")} />);
  await findLandmark({ timeout: WARM_ROUTE_TRIPWIRE_MS });
  // Unmounting also clears DockHost's pending debounced layout save (its own
  // effect cleanup), so no warm render leaks a write into a later test.
  cleanup();
  resetWorkspaceStoreForTests();
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
  vi.stubGlobal("fetch", (url: string) => {
    if (url === "/api/tree") {
      return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
    }
    return Promise.resolve(jsonResponse({}));
  });
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
  await warmRoute("/new", (wait) => screen.findByRole("button", { name: "Spawn" }, wait));
  await warmRoute("/s/local:ref_warm", (wait) => screen.findByText(/loading transcript/i, undefined, wait));
  await warmRoute("/settings/general", (wait) => screen.findByRole("navigation", { name: "Settings sections" }, wait));
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  resetTreeStoreForTests();
  localStorage.clear();
  vi.stubGlobal("fetch", (url: string) => {
    if (url === "/api/tree") {
      return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
    }
    return Promise.resolve(jsonResponse({}));
  });
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // The command palette store is a module singleton; reset it so one test's
  // open palette never leaks into the next.
  paletteStore.setState({ open: false, query: "" });
  vi.unstubAllGlobals();
});

// Build the persisted fixture through the real AppShell/DockHost save path.
// The route tests below deliberately inspect the restored workspace state,
// not dockview's serialized representation, so a layout-format change cannot
// make these assertions tautological.
async function saveRealSessionLayout(): Promise<void> {
  window.history.pushState({}, "", "/s/local:session-a");
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
  // fallback is gone) - its Spawn button proves the pane mounted.
  // The spawn pane's own submit verb is "Spawn" - it lives in the prompt
  // card's corner the way Send does in the composer, both surfaces being the
  // same shared card.
  expect(await screen.findByRole("button", { name: "Spawn" })).toBeTruthy();
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
    serverInfo: { name: "serf-hub-test", version: "9.9.9" },
    protocolVersion: "1",
    sourceId: "src_test",
    features: ALL_FEATURES_OFF,
  };
  fake.scriptConnect(() => scripted);

  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  await waitFor(() => {
    expect(connectionStore.getState().serverInfo).toEqual({ name: "serf-hub-test", version: "9.9.9" });
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

test("opening /s/{ref} replaces unrelated main session instead of opening a secondary", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:existing" });
  const fetchMock = vi.fn((url: string) => {
    if (url === "/api/tree") return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);

  window.history.pushState({}, "", "/s/local:new_session");
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

test("navigating from one session deep link to another, post-mount, opens the new one", async () => {
  window.history.pushState({}, "", "/s/local:ref_first");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findAllByText("local:ref_first"); // tab + pane body (no thread name known), both settled

  act(() => {
    window.history.pushState({}, "", "/s/local:ref_second");
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
  render(<AppShell client={new FakeClient("ready")} />);
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
  const treeWithoutSession = {
    ...TREE_RESPONSE_WITH_NESTED_SESSION,
    projects: [{ ...TREE_RESPONSE_WITH_NESTED_SESSION.projects[0], sessions: [] }],
  };
  let treeBody: unknown = TREE_RESPONSE_WITH_NESTED_SESSION;
  vi.stubGlobal("fetch", (url: string) => {
    if (url === "/api/tree") return Promise.resolve(jsonResponse(treeBody));
    if (url === "/api/sessions/local%3As1/delete") {
      // The server deleted it: every later tree read is the one without it,
      // exactly what confirmDeleteSession's own awaited refresh sees.
      treeBody = treeWithoutSession;
      return Promise.resolve(jsonResponse({ deleted: ["s1"], skipped: [] }));
    }
    return Promise.resolve(jsonResponse({}));
  });

  const user = userEvent.setup();
  window.history.pushState({}, "", "/s/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);
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

  expect(await screen.findByRole("button", { name: "Spawn" })).toBeTruthy();
  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.type).toBe("spawn");
  });
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(workspaceStore.getState().mainPane()?.slot).toBe("main");
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
  expect(await screen.findByRole("button", { name: "Spawn" })).toBeTruthy();

  await waitFor(() => {
    expect(workspaceStore.getState().panes).toEqual([
      expect.objectContaining({ type: "spawn", params: {}, slot: "main" }),
    ]);
  });
});

test("repairs a nested session restored as main when the root route's tree arrives", async () => {
  await saveLegacyNestedMainLayout();

  const fetchMock = vi.fn((url: string) => {
    if (url === "/api/tree") return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_OWNER_AND_CHILD));
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.pushState({}, "", "/");
  render(<AppShell client={new FakeClient("ready")} />);

  await waitFor(() => expect(treeStore.getState().tree?.projects[0]?.sessions[0]?.ref).toBe("local:owner"));
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
    if (url === "/api/tree") return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_OWNER_AND_CHILD));
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

test("successful Spawn navigation replaces Spawn with the created session and clears old secondary panes", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => threadStartResponse("local:created"));
  window.history.pushState({}, "", "/new");
  render(<AppShell client={fake} />);
  expect(await screen.findByRole("button", { name: "Spawn" })).toBeTruthy();
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
  let resolveTree!: (value: Response) => void;
  const treePromise = new Promise<Response>((resolve) => {
    resolveTree = resolve;
  });
  const fetchMock = vi.fn((url: string) => {
    if (url === "/api/tree") return treePromise;
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(paneFor("local:sub1")).toBeUndefined();
  expect(paneFor("local:s1")).toBeUndefined();

  resolveTree(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
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

test("nested deep-link waits for successful tree refresh on same pathname after an initial fetch failure", async () => {
  let calls = 0;
  const fetchMock = vi.fn((url: string) => {
    if (url !== "/api/tree") return jsonResponse({});
    calls += 1;
    if (calls === 1) return Promise.resolve(jsonResponse({}, 500));
    return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
  });
  vi.stubGlobal("fetch", fetchMock);

  window.history.pushState({}, "", "/s/local:sub1");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(workspaceStore.getState().panes.find((pane) => pane.type === "session")).toBeUndefined();

  await act(async () => {
    await treeStore.getState().refresh();
  });

  await waitFor(() => {
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" });
  });
  expect(paneFor("local:sub1")?.slot).toBe("secondary");
  expect(fetchMock.mock.calls.length).toBeGreaterThan(1);
});

test("deep-linking to /settings replaces any existing main pane", async () => {
  workspaceStore.getState().openPane("session", { ref: "local:main_session" });
  workspaceStore.getState().openPane("settings", { section: "stale_credentials" }, { slot: "secondary" });
  const fetchMock = vi.fn((url: string) => {
    if (url === "/api/tree") return Promise.resolve(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));
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
  expect(contentRule![1]).toContain("gap: 0");
});

test("mobile: the shared shell follows the visible viewport while retaining a vh fallback", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const shellRule = css.match(/\.shell \{([^}]*)\}/);
  expect(shellRule).not.toBeNull();

  const fallback = shellRule![1]!.indexOf("height: 100vh");
  const dynamic = shellRule![1]!.indexOf("height: 100dvh");
  expect(fallback).toBeGreaterThanOrEqual(0);
  expect(dynamic).toBeGreaterThan(fallback);
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

// A /s/{ref} route cannot be placed until /api/tree says whether the ref is
// nested (openRouteAsPane defers it), and no fetch can resolve inside the
// first commit - so on mobile the shell always spends a beat with the deep
// link parsed but unplaced. StackHost fills an empty stack with welcome and
// publishes the focused pane's URL, which used to overwrite the address bar
// with "/" during exactly that beat: the deep link was gone before the tree
// it was waiting for ever landed, and no later serf/tree/changed push could
// name it again.
test("mobile: a /s/{ref} deep link still opens once the tree lands, instead of being overwritten by welcome", async () => {
  let resolveTree!: (value: Response) => void;
  const treePromise = new Promise<Response>((resolve) => {
    resolveTree = resolve;
  });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => (url === "/api/tree" ? treePromise : Promise.resolve(jsonResponse({})))),
  );
  installMobileViewport();

  window.history.pushState({}, "", "/s/local:s1");
  render(<AppShell client={new FakeClient("ready")} />);

  // The mobile host has settled on its own welcome fallback with the tree
  // still in flight - the whole window in which the deep link was lost.
  await screen.findByText("No session open");
  expect(window.location.pathname).toBe("/s/local:s1");

  resolveTree(jsonResponse(TREE_RESPONSE_WITH_NESTED_SESSION));

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  // And the address bar now names it in paneToURL's own canonical form.
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3As1"));
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

  await waitFor(() => expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" }));
  await waitFor(() => expect(window.location.pathname).toBe("/thread/local:s1"));
  expect(document.querySelector("[data-single-pane]")).not.toBeNull();
});

// --- kata p5w9: one boot, one GET /api/tree ------------------------------

// A desktop boot has TWO unconditional mount-time tree fetchers -
// initNotifications()'s baseline (run at AppShell.tsx's module evaluation, so
// it fires on every host, including the mobile one where no rail mounts) and
// the rail's own mount effect - and used to issue a full GET /api/tree from
// each, milliseconds apart, for the same snapshot. Plus a third: AppShell
// publishes serverInfo through connectionStore once its connect() resolves,
// which the reconnect subscriber read as a new connection.
//
// The notifications engine is a module singleton already initialized by
// AppShell.tsx's own import, so a REAL boot is modelled by resetting and
// re-initializing it here - and it is deliberately left initialized
// afterwards, exactly as module evaluation leaves it for every other test in
// this file.
test("kata p5w9: a desktop boot issues exactly one GET /api/tree", async () => {
  const fetchMock = vi.fn((url: string) =>
    Promise.resolve(jsonResponse(url === "/api/tree" ? TREE_RESPONSE_WITH_NESTED_SESSION : {})),
  );
  vi.stubGlobal("fetch", fetchMock);

  resetNotificationsForTests();
  initNotifications();
  render(<AppShell client={new FakeClient("ready")} />);

  await screen.findByText("No session open");
  await waitFor(() => expect(treeStore.getState().tree).not.toBeNull());
  await waitFor(() => expect(connectionStore.getState().serverInfo).toBeDefined());

  expect(fetchMock.mock.calls.filter(([url]) => url === "/api/tree")).toHaveLength(1);
});
