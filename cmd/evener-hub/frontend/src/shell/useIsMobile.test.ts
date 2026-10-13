import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "../stores/transcriptDisplay";
import { isMobileViewport, resetMobileViewportForTests, subscribeMobileViewport, useIsMobile } from "./useIsMobile";

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
  resetTranscriptDisplayStoreForTests();
  resetMobileViewportForTests();
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

test("uses one shared live media query for direct readers and React subscribers", () => {
  const { lists, matchMedia } = installMatchMediaStub(false);
  const { result } = renderHook(() => useIsMobile());
  expect(isMobileViewport()).toBe(false);
  expect(matchMedia).toHaveBeenCalledTimes(1);

  act(() => lists.get("(max-width: 899px)")!.emit(true));
  expect(result.current).toBe(true);
  expect(isMobileViewport()).toBe(true);
});

test.each([
  { direction: "mobile to desktop", initial: true, next: false, before: "mobile", after: "desktop" },
  { direction: "desktop to mobile", initial: false, next: true, before: "desktop", after: "mobile" },
] as const)(
  "publishes $direction after another hook reads the live value before its event",
  ({ initial, next, before, after }) => {
    const { lists, matchMedia } = installMatchMediaStub(initial);
    initTranscriptDisplay();
    const primary = renderHook(() => useIsMobile());
    const secondary = renderHook(() => useIsMobile());
    const firstDirectListener = vi.fn();
    const secondDirectListener = vi.fn();
    const stopFirst = subscribeMobileViewport(firstDirectListener);
    const stopSecond = subscribeMobileViewport(secondDirectListener);
    const list = lists.get("(max-width: 899px)")!;

    expect(primary.result.current).toBe(initial);
    expect(secondary.result.current).toBe(initial);
    expect(transcriptDisplayStore.getState().viewport).toBe(before);
    expect(list.listenerCount).toBe(1);
    expect(matchMedia).toHaveBeenCalledTimes(1);

    // A MediaQueryList is live before its asynchronous change event arrives.
    // Change only that external live value, then make one existing React
    // consumer take the intervening snapshot that reproduced the browser race.
    list.matches = next;
    secondary.rerender();

    expect(secondary.result.current).toBe(next);
    expect(primary.result.current).toBe(initial);
    expect(transcriptDisplayStore.getState().viewport).toBe(before);
    expect(firstDirectListener).not.toHaveBeenCalled();
    expect(secondDirectListener).not.toHaveBeenCalled();

    act(() => list.emit(next));

    expect(primary.result.current).toBe(next);
    expect(secondary.result.current).toBe(next);
    expect(transcriptDisplayStore.getState().viewport).toBe(after);
    expect(firstDirectListener).toHaveBeenCalledTimes(1);
    expect(secondDirectListener).toHaveBeenCalledTimes(1);
    expect(list.listenerCount).toBe(1);
    expect(matchMedia).toHaveBeenCalledTimes(1);

    stopFirst();
    stopSecond();
    primary.unmount();
    secondary.unmount();
    expect(list.listenerCount).toBe(1);
    resetTranscriptDisplayStoreForTests();
    expect(list.listenerCount).toBe(0);
  },
);

test("ordinary events publish both directions once without duplicate notifications", () => {
  const { lists, matchMedia } = installMatchMediaStub(false);
  initTranscriptDisplay();
  const { result, unmount } = renderHook(() => useIsMobile());
  const directListener = vi.fn();
  const stop = subscribeMobileViewport(directListener);
  const list = lists.get("(max-width: 899px)")!;

  act(() => list.emit(true));
  expect(result.current).toBe(true);
  expect(transcriptDisplayStore.getState().viewport).toBe("mobile");
  expect(directListener).toHaveBeenCalledTimes(1);

  expect(isMobileViewport()).toBe(true);
  expect(isMobileViewport()).toBe(true);
  act(() => list.emit(true));
  expect(directListener).toHaveBeenCalledTimes(1);

  act(() => list.emit(false));
  expect(result.current).toBe(false);
  expect(transcriptDisplayStore.getState().viewport).toBe("desktop");
  expect(directListener).toHaveBeenCalledTimes(2);
  expect(list.listenerCount).toBe(1);
  expect(matchMedia).toHaveBeenCalledTimes(1);

  stop();
  unmount();
  resetTranscriptDisplayStoreForTests();
  expect(list.listenerCount).toBe(0);
});

test("preserves a viewport change across the snapshot-to-subscribe gap", () => {
  const { lists, matchMedia } = installMatchMediaStub(false);
  expect(isMobileViewport()).toBe(false);
  const list = lists.get("(max-width: 899px)")!;

  list.matches = true;
  const directListener = vi.fn();
  const stop = subscribeMobileViewport(directListener);
  expect(isMobileViewport()).toBe(true);

  act(() => list.emit(true));
  expect(directListener).toHaveBeenCalledTimes(1);
  expect(list.listenerCount).toBe(1);
  expect(matchMedia).toHaveBeenCalledTimes(1);

  stop();
  expect(list.listenerCount).toBe(0);
});

test("replaces the shared media source without losing existing subscribers", () => {
  const first = installMatchMediaStub(false);
  const primary = renderHook(() => useIsMobile());
  const firstList = first.lists.get("(max-width: 899px)")!;
  expect(primary.result.current).toBe(false);
  expect(firstList.listenerCount).toBe(1);

  const replacement = installMatchMediaStub(true);
  const secondary = renderHook(() => useIsMobile());
  const replacementList = replacement.lists.get("(max-width: 899px)")!;
  expect(firstList.listenerCount).toBe(0);
  expect(replacementList.listenerCount).toBe(1);
  expect(primary.result.current).toBe(false);
  expect(secondary.result.current).toBe(true);

  act(() => replacementList.emit(false));
  expect(primary.result.current).toBe(false);
  expect(secondary.result.current).toBe(false);
  act(() => replacementList.emit(true));
  expect(primary.result.current).toBe(true);
  expect(secondary.result.current).toBe(true);
  expect(first.matchMedia).toHaveBeenCalledTimes(1);
  expect(replacement.matchMedia).toHaveBeenCalledTimes(1);

  primary.unmount();
  secondary.unmount();
  expect(replacementList.listenerCount).toBe(0);
});

test("does not throw and returns false when window.matchMedia is unavailable", () => {
  // @ts-expect-error simulates jsdom's own honest default (no stub
  // installed at all) - the SSR-safe guard's actual target, not a
  // contrived case.
  delete window.matchMedia;
  const { result } = renderHook(() => useIsMobile());
  expect(result.current).toBe(false);
});

test("isMobileViewport directly returns false without matchMedia", () => {
  // @ts-expect-error simulates jsdom's honest no-matchMedia baseline.
  delete window.matchMedia;
  expect(isMobileViewport()).toBe(false);
});

test("isMobileViewport directly reads the current media query", () => {
  window.matchMedia = vi.fn(() => ({ matches: true })) as unknown as typeof window.matchMedia;
  expect(isMobileViewport()).toBe(true);
  expect(window.matchMedia).toHaveBeenCalledWith("(max-width: 899px)");
});
