import { afterEach, expect, test, vi } from "vitest";
import { cleanup, renderHook, act } from "@testing-library/react";
import { useIsMobile } from "./useIsMobile";

// jsdom does not implement window.matchMedia at all (verified directly:
// `typeof window.matchMedia === "undefined"` under this project's vitest+
// jsdom setup, same kind of gap DockHost.test.tsx documents for
// ResizeObserver/localStorage) - every test in this file installs this
// stub itself rather than assuming a real implementation. FakeMediaQueryList
// mimics the modern EventTarget-shaped MediaQueryList (addEventListener/
// removeEventListener for the "change" event) - the only surface
// useIsMobile.ts itself calls - and shares one mutable `matches` value
// across every MediaQueryList a single stubbed matchMedia() call returns,
// same as a real browser: multiple callers querying the same media feature
// see the same live state and all of their own "change" listeners fire
// together on a real viewport change, not just whichever object happened
// to be queried most recently.
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

  // Test-only helper (not part of the real MediaQueryList API): flips
  // `matches` and notifies every listener still registered, exactly like a
  // real viewport crossing the breakpoint would.
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

afterEach(() => {
  cleanup();
  // @ts-expect-error restores jsdom's own absence of matchMedia between
  // tests, matching the honest baseline this file's own header comment
  // describes - never leaves one test's stub visible to the next.
  delete window.matchMedia;
});

test("returns false when the media query does not match at mount", () => {
  installMatchMediaStub(false);
  const { result } = renderHook(() => useIsMobile());
  expect(result.current).toBe(false);
});

test("returns true when the media query matches at mount", () => {
  installMatchMediaStub(true);
  const { result } = renderHook(() => useIsMobile());
  expect(result.current).toBe(true);
});

test("queries with the exact <900px breakpoint string", () => {
  const { matchMedia } = installMatchMediaStub(false);
  renderHook(() => useIsMobile());
  expect(matchMedia).toHaveBeenCalledWith("(max-width: 899px)");
});

test("updates reactively when the media query's change event fires", () => {
  const { lists } = installMatchMediaStub(false);
  const { result } = renderHook(() => useIsMobile());
  expect(result.current).toBe(false);

  act(() => {
    lists.get("(max-width: 899px)")!.emit(true);
  });

  expect(result.current).toBe(true);
});

test("removes its change listener on unmount", () => {
  const { lists } = installMatchMediaStub(false);
  const { unmount } = renderHook(() => useIsMobile());
  const list = lists.get("(max-width: 899px)")!;
  expect(list.listenerCount).toBe(1);

  unmount();

  expect(list.listenerCount).toBe(0);
});

test("does not throw and returns false when window.matchMedia is unavailable", () => {
  // @ts-expect-error simulates jsdom's own honest default (no stub
  // installed at all) - the SSR-safe guard's actual target, not a
  // contrived case.
  delete window.matchMedia;
  const { result } = renderHook(() => useIsMobile());
  expect(result.current).toBe(false);
});
