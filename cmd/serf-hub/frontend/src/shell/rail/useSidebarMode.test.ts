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

const WIDE_QUERY = "(min-width: 1200px)";

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

test("auto mode collapses below the 1200px breakpoint", () => {
  installMatchMediaStub(false); // narrow: below 1200px
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current).toEqual({ mode: "auto", collapsed: true });
});

test("auto mode expands at or above the 1200px breakpoint", () => {
  installMatchMediaStub(true); // wide: >=1200px
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

test("queries the exact 1200px desktop breakpoint (distinct from the 900px mobile one)", () => {
  const { matchMedia } = installMatchMediaStub(true);
  renderHook(() => useSidebarMode());
  expect(matchMedia).toHaveBeenCalledWith(WIDE_QUERY);
});

test("auto mode reacts when the viewport crosses the breakpoint", () => {
  const { lists } = installMatchMediaStub(true); // starts wide
  prefsStore.setState({ sidebarMode: "auto" });
  const { result } = renderHook(() => useSidebarMode());
  expect(result.current.collapsed).toBe(false);

  act(() => lists.get(WIDE_QUERY)!.emit(false)); // shrink below 1200px

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
