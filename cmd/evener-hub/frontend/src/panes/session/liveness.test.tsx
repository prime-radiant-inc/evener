// The session clock is shared: the transcript's relative timestamps, the
// rail's ages, and Cadence's decay all read it. Its cadence is a product
// promise to the reader — a timestamp that rewrites itself too often reads
// as churn, so the tick must not fire more often than every 5s.
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { NOW_TICK_MS, useNowTick } from "./liveness";

afterEach(() => {
  vi.useRealTimers();
});

const originalVisibility = Object.getOwnPropertyDescriptor(Document.prototype, "visibilityState");

function setVisibility(state: "visible" | "hidden"): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
}

afterEach(() => {
  Reflect.deleteProperty(document, "visibilityState");
  if (originalVisibility) Object.defineProperty(Document.prototype, "visibilityState", originalVisibility);
});

test("the session clock advances no more often than every 5s", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const { result } = renderHook(() => useNowTick(NOW_TICK_MS));
  const start = result.current;

  act(() => {
    vi.advanceTimersByTime(4_999);
  });
  expect(result.current).toBe(start);

  act(() => {
    vi.advanceTimersByTime(NOW_TICK_MS - 4_999);
  });
  expect(result.current).toBeGreaterThan(start);
});

// The clock above is a rendering clock, so it must not tick in a hidden tab
// and must show the true time on the first visible frame (see
// shell/useVisibleInterval.ts).
test("the session clock pauses while hidden and refreshes on becoming visible", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  setVisibility("visible");
  const { result } = renderHook(() => useNowTick(NOW_TICK_MS));
  const start = result.current;

  setVisibility("hidden");
  act(() => document.dispatchEvent(new Event("visibilitychange")));
  act(() => {
    vi.advanceTimersByTime(NOW_TICK_MS * 4);
  });
  expect(result.current).toBe(start);

  setVisibility("visible");
  act(() => document.dispatchEvent(new Event("visibilitychange")));
  expect(result.current).toBeGreaterThan(start);
});
