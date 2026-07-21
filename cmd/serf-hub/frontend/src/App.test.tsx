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
  // Module pre-awaited in beforeAll, so this only waits out React's own
  // lazy/Suspense commit cycle — deterministic within findBy's default.
  expect(await screen.findByText(/widget gallery/i)).toBeTruthy();
});

test("renders the dev harness at /dev/harness", async () => {
  window.history.pushState({}, "", "/dev/harness");
  render(<App />);
  // Module pre-awaited in beforeAll, so this only waits out React's own
  // lazy/Suspense commit cycle — deterministic within findBy's default.
  expect(await screen.findByText(/connection:/i)).toBeTruthy();
});
