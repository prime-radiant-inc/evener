import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useInlineStopReady } from "./useInlineStopReady";

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test.each(["send", null] as const)("leaving Stop for %s cancels its pending activation", (next) => {
  const { result, rerender, unmount } = renderHook(
    ({ slot }: { slot: "send" | "stop" | null }) => useInlineStopReady("session-a", slot),
    { initialProps: { slot: "send" } },
  );
  rerender({ slot: "stop" });
  expect(result.current).toBe(false);
  expect(vi.getTimerCount()).toBe(1);
  rerender({ slot: next });
  expect(vi.getTimerCount()).toBe(0);
  rerender({ slot: "send" });
  rerender({ slot: "stop" });
  expect(result.current).toBe(false);
  expect(vi.getTimerCount()).toBe(1);
  unmount();
  expect(vi.getTimerCount()).toBe(0);
});

test("a hidden composer opens on Stop without inheriting an activation delay", () => {
  const { result, rerender } = renderHook<boolean, { slot: "send" | "stop" | null }>(
    ({ slot }: { slot: "send" | "stop" | null }) => useInlineStopReady("session-a", slot),
    { initialProps: { slot: null } },
  );
  rerender({ slot: "stop" });
  expect(result.current).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
  act(() => vi.advanceTimersByTime(500));
  expect(result.current).toBe(true);
});
