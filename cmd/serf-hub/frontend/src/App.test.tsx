import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { App } from "./App";
import { resetWorkspaceStoreForTests } from "./shell/workspace";

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
async function warmRoute(path: string, text: string | RegExp): Promise<void> {
  window.history.pushState({}, "", path);
  render(<App />);
  await screen.findByText(text);
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
  await import("./dev/WidgetGallery");
  await import("./dev/DevHarness");
  await import("./panes/welcome/Welcome");
  // AppShell.tsx now React.lazy()s DockHost itself (Task 7's bundle split -
  // dockview is dead weight on the mobile path); the default-route test
  // below renders through AppShell -> DockHost, same reasoning as the
  // three imports above.
  await import("./shell/DockHost");

  // Then render each route once, for the React.lazy half of the cost — see
  // warmRoute above. Awaiting real completion, in a hook whose ceiling is a
  // tripwire, rather than spending it inside a test's assertion window.
  await warmRoute("/", "No session open");
  await warmRoute("/dev/widgets", /widget gallery/i);
  await warmRoute("/dev/harness", /connection:/i);
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("renders the app shell (welcome pane) at the default route", async () => {
  render(<App />);
  expect(await screen.findByText("No session open")).toBeTruthy();
  // DevHarness moved out of the default route onto its own /dev/harness
  // route (see the next test) - it must not also render here.
  expect(screen.queryByText(/connection:/i)).toBeNull();
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
