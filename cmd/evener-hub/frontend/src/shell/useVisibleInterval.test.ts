// @vitest-environment jsdom

// Pins the rendering clocks' visibility gate: a redraw timer buys nothing in a
// hidden tab, and a paused clock that does not tick on resume shows stale
// relative ages. The tests count ticks and timers rather than reading any
// label, so they survive copy changes.
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useVisibleInterval } from "./useVisibleInterval";

const originalVisibility = Object.getOwnPropertyDescriptor(Document.prototype, "visibilityState");

function setVisibility(state: "visible" | "hidden"): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
}

function dispatchVisibilityChange(): void {
  act(() => document.dispatchEvent(new Event("visibilitychange")));
}

afterEach(() => {
  vi.useRealTimers();
  Reflect.deleteProperty(document, "visibilityState");
  if (originalVisibility) Object.defineProperty(Document.prototype, "visibilityState", originalVisibility);
});

test("ticks on the cadence while the document is visible", () => {
  vi.useFakeTimers();
  setVisibility("visible");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000));

  expect(tick).not.toHaveBeenCalled();
  act(() => vi.advanceTimersByTime(3_000));
  expect(tick).toHaveBeenCalledTimes(3);
});

test("arms no timer at all while the document starts hidden", () => {
  vi.useFakeTimers();
  setVisibility("hidden");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000));

  expect(vi.getTimerCount()).toBe(0);
  act(() => vi.advanceTimersByTime(10_000));
  expect(tick).not.toHaveBeenCalled();
});

test("becoming visible ticks once immediately and then resumes the cadence", () => {
  vi.useFakeTimers();
  setVisibility("hidden");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000));
  act(() => vi.advanceTimersByTime(5_000));
  expect(tick).not.toHaveBeenCalled();

  setVisibility("visible");
  dispatchVisibilityChange();
  expect(tick).toHaveBeenCalledTimes(1);

  // The resumed cadence is one interval from the resume, not from a stall.
  act(() => vi.advanceTimersByTime(999));
  expect(tick).toHaveBeenCalledTimes(1);
  act(() => vi.advanceTimersByTime(1));
  expect(tick).toHaveBeenCalledTimes(2);
});

test("going hidden stops the timer, and its ticks stay stopped", () => {
  vi.useFakeTimers();
  setVisibility("visible");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000));
  act(() => vi.advanceTimersByTime(1_000));
  expect(tick).toHaveBeenCalledTimes(1);

  setVisibility("hidden");
  dispatchVisibilityChange();
  expect(vi.getTimerCount()).toBe(0);
  act(() => vi.advanceTimersByTime(10_000));
  expect(tick).toHaveBeenCalledTimes(1);

  setVisibility("visible");
  dispatchVisibilityChange();
  expect(tick).toHaveBeenCalledTimes(2);
});

test("a visibilitychange while already visible does not add an unscheduled tick", () => {
  vi.useFakeTimers();
  setVisibility("visible");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000));

  dispatchVisibilityChange();
  expect(tick).not.toHaveBeenCalled();
  act(() => vi.advanceTimersByTime(1_000));
  expect(tick).toHaveBeenCalledTimes(1);
});

test("a disabled clock arms nothing and a resume does not tick it", () => {
  vi.useFakeTimers();
  setVisibility("visible");
  const tick = vi.fn();
  renderHook(() => useVisibleInterval(tick, 1_000, false));

  expect(vi.getTimerCount()).toBe(0);
  dispatchVisibilityChange();
  act(() => vi.advanceTimersByTime(5_000));
  expect(tick).not.toHaveBeenCalled();
});

test("unmount clears the timer and the visibility listener", () => {
  vi.useFakeTimers();
  setVisibility("visible");
  const tick = vi.fn();
  const { unmount } = renderHook(() => useVisibleInterval(tick, 1_000));

  unmount();
  expect(vi.getTimerCount()).toBe(0);
  act(() => vi.advanceTimersByTime(5_000));
  setVisibility("hidden");
  dispatchVisibilityChange();
  setVisibility("visible");
  dispatchVisibilityChange();
  expect(tick).not.toHaveBeenCalled();
});
