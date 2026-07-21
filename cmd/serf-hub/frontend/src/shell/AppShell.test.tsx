import { afterEach, beforeAll, beforeEach, test, expect, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AppwireClient } from "../protocol/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InitializeResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { resetWorkspaceStoreForTests } from "./workspace";
import { AppShell } from "./AppShell";

// Matches DockHost.tsx's own LAYOUT_STORAGE_KEY exactly (not exported - a
// deliberately internal implementation detail; duplicated here the same
// way DockHost.test.tsx's own LAYOUT_KEY is).
const LAYOUT_KEY = "serf.workspace.layout.v1";

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
  await import("../panes/welcome/Welcome");
  await import("../panes/session/Session");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path) - pre-warmed for the same
  // reason as the two panes above.
  await import("./DockHost");
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetWorkspaceStoreForTests();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

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

test('clicking "New session" navigates to /new and the welcome pane shows a note', async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  const button = await screen.findByRole("button", { name: "New session" });

  await user.click(button);

  expect(window.location.pathname).toBe("/new");
  expect(await screen.findByText(/starting a new session isn't available yet/i)).toBeTruthy();
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
  window.history.pushState({}, "", "/s/ref_abc123");
  render(<AppShell client={new FakeClient("ready")} />);

  // The session placeholder pane (Wave 4 builds the real transcript view)
  // shows the ref it was opened with - proving the deep link actually
  // threaded through openPane into a real dockview panel, not just that
  // urlToPane() parsed the URL correctly in isolation (routing.test.ts
  // already covers that). The tab title renders synchronously (addPanel's
  // own title option) but the pane's own content is a lazy-loaded
  // component behind Suspense - findAllByText returns as soon as it finds
  // ANY match, not once a specific count stabilizes, so this waits for the
  // pane body's own text FIRST (it exists only once Suspense resolves),
  // THEN checks the ref appears twice (tab + pane body, no thread name
  // known so both fall back to the raw ref - see Session.tsx).
  expect(await screen.findByText("Transcript arrives in wave 4")).toBeTruthy();
  expect(screen.getAllByText("ref_abc123")).toHaveLength(2);
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
  window.history.pushState({}, "", "/s/ref_first");
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findAllByText("ref_first"); // tab + pane body (no thread name known), both settled

  act(() => {
    window.history.pushState({}, "", "/s/ref_second");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await screen.findAllByText("ref_second");
  // Both panes stay open as separate tabs - navigating to a second deep
  // link doesn't close the first one, it opens (and focuses) another.
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["ref_first", "ref_second"]);
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
    window.history.pushState({}, "", "/s/ref_from_404");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await screen.findByText("Transcript arrives in wave 4");
  const tabs = document.querySelectorAll(".dv-tab");
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["ref_from_404"]);
});

test("a saved layout from a previous session merges with a fresh deep link, which lands focused", async () => {
  // Phase 1: generate a REAL saved layout at the default route (the
  // welcome pane) - real timers throughout; the welcome pane's own
  // addPanel() already schedules the debounced save (addPanel fires
  // onDidLayoutChange - see DockHost.test.tsx's own probe-verified
  // comment on that), so waitFor polling for the actual write to land is
  // enough, no fake-timer juggling needed for this test's own concern.
  window.history.pushState({}, "", "/");
  const { unmount } = render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  await waitFor(() => {
    expect(localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
  });
  unmount();
  resetWorkspaceStoreForTests(); // simulates a fresh page load: in-memory workspace state resets

  // Phase 2: fresh mount at a NEW deep link. Deliberately NOT calling
  // localStorage.clear() here, unlike every other test in this file (see
  // beforeEach above, which blinds the rest of the suite to this path) -
  // the whole point is proving a stale saved layout from phase 1 merges
  // WITH this freshly-routed pane (DockHost.tsx's own merge-restore boot
  // sequence) rather than suppressing it, or being suppressed by it.
  window.history.pushState({}, "", "/s/ref_new_session");
  render(<AppShell client={new FakeClient("ready")} />);

  expect(await screen.findByText("Transcript arrives in wave 4")).toBeTruthy();
  const tabs = document.querySelectorAll(".dv-tab");
  // The restored "Welcome" tab (phase 1's whole saved layout) is still
  // there - a merge, not a replacement - with the routed deep link
  // appended after it and focused/active.
  expect(Array.from(tabs).map((t) => t.textContent)).toEqual(["Welcome", "ref_new_session"]);
  expect(document.querySelector(".dv-tab.dv-active-tab")?.textContent).toBe("ref_new_session");
});
