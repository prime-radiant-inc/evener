import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { prefsStore } from "../../stores/prefs";
import { useSidebarMode } from "./useSidebarMode";

// jsdom does not implement window.matchMedia (see shell/useIsMobile.test.ts's
// own header note); every test here installs this stub itself. Same
// FakeMediaQueryList shape useIsMobile.test.ts uses - the modern
// addEventListener/removeEventListener("change") surface useSidebarMode calls,
// one shared live `matches` per queried feature.
class FakeMediaQueryList {
  matches: boolean;
  media: string;
  private listeners = new Set<(event: MediaQueryListEvent) => void>();

  constructor(media: string, matches: boolean) {
    this.media = media;
    this.matches = matches;
  }

  addEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
    if (type === "change") this.listeners.add(listener);
  }

  removeEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
    if (type === "change") this.listeners.delete(listener);
  }

  emit(matches: boolean): void {
    this.matches = matches;
    for (const listener of this.listeners) listener({ matches } as MediaQueryListEvent);
  }

  get listenerCount(): number {
    return this.listeners.size;
  }
}

function installMatchMediaStub(initialMatches: boolean) {
  const lists = new Map<string, FakeMediaQueryList>();
  const matchMedia = vi.fn((query: string) => {
    let list = lists.get(query);
    if (!list) {
      list = new FakeMediaQueryList(query, initialMatches);
      lists.set(query, list);
    }
    return list;
  });
  window.matchMedia = matchMedia as unknown as typeof window.matchMedia;
  return { matchMedia, lists };
}

// Width-parametrized matchMedia stub: unlike installMatchMediaStub (which
// forces one fixed match state for every query), this evaluates each
// "(min-width: Npx)" query the hook issues against a concrete viewport width,
// so a test can assert the resolved dock behavior at a specific px WITHOUT
// hardcoding which breakpoint the code queries. That makes "docks at 1000px" a
// genuine breakpoint-value test: it resolves not-collapsed only because the
// code queries a threshold at or below 1000px.
function installViewportWidth(width: number) {
  const lists = new Map<string, FakeMediaQueryList>();
  const matchMedia = vi.fn((query: string) => {
    let list = lists.get(query);
    if (!list) {
      const min = /min-width:\s*(\d+)px/.exec(query);
      list = new FakeMediaQueryList(query, min ? width >= Number(min[1]) : false);
      lists.set(query, list);
    }
    return list;
  });
  window.matchMedia = matchMedia as unknown as typeof window.matchMedia;
  return { matchMedia, lists };
}

const WIDE_QUERY = "(min-width: 900px)";

beforeEach(() => {
  // Drive the store directly (bypassing setSidebarMode's localStorage write,
  // which the Node-shadowed jsdom localStorage can't service under vitest).
  prefsStore.setState({ sidebarMode: "auto" });
});

afterEach(() => {
  cleanup();
  prefsStore.setState({ sidebarMode: "auto" });
  // @ts-expect-error restore jsdom's honest absence of matchMedia between tests
  delete window.matchMedia;
});

test("pane mode is never collapsed, above or below the breakpoint", () => {
  installMatchMediaStub(false); // narrow
  prefsStore.setState({ sidebarMode: "pane" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "pane", collapsed: false });
});

test("auto mode collapses below the 900px breakpoint", () => {
  installMatchMediaStub(false); // narrow: below 900px
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "auto", collapsed: true });
});

test("auto mode expands at or above the 900px breakpoint", () => {
  installMatchMediaStub(true); // wide: >=900px
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "auto", collapsed: false });
});

// The 3w2p fix: auto docks across the whole desktop range, so a mid-desktop
// 1000px viewport (which used to collapse under the old 1200px threshold) now
// stays expanded. Uses the width-parametrized stub so this passes only because
// the queried threshold is at or below 1000px, not because a constant was
// renamed.
test("auto mode docks (not collapsed) at 1000px, the old collapse zone", () => {
  installViewportWidth(1000);
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "auto", collapsed: false });
});

test("rail (Collapsed) mode is always collapsed, even on a wide viewport", () => {
  installMatchMediaStub(true); // wide
  prefsStore.setState({ sidebarMode: "rail" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "rail", collapsed: true });
});

test("queries the 900px desktop dock breakpoint (the first non-mobile pixel)", () => {
  const { matchMedia } = installMatchMediaStub(true);
  renderHook(() => useSidebarMode());
  expect(matchMedia).toHaveBeenCalledWith(WIDE_QUERY);
});

test("auto mode reacts when the viewport crosses the breakpoint", () => {
  const { lists } = installMatchMediaStub(true); // starts wide
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current.collapsed).toBe(false);

  act(() => lists.get(WIDE_QUERY)!.emit(false)); // shrink below 900px

  expect(result.current.collapsed).toBe(true);
});

test("reacts when the sidebar mode preference changes", () => {
  installMatchMediaStub(true); // wide
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current.collapsed).toBe(false);

  act(() => prefsStore.setState({ sidebarMode: "rail" }));

  expect(result.current).toEqual({ mode: "rail", collapsed: true });
});

test("removes its change listener on unmount", () => {
  const { lists } = installMatchMediaStub(true);
  const { unmount } = renderHook(() => useSidebarMode());
  const list = lists.get(WIDE_QUERY)!;
  expect(list.listenerCount).toBe(1);

  unmount();

  expect(list.listenerCount).toBe(0);
});

test("defaults to expanded in auto mode when matchMedia is unavailable", () => {
  // @ts-expect-error simulates jsdom's honest default (no stub installed)
  delete window.matchMedia;
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current.collapsed).toBe(false);
});
