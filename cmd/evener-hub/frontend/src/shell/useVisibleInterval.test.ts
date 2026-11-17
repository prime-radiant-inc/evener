// @vitest-environment jsdom

// Pins the rendering clocks' visibility gate: a redraw timer buys nothing in a
// hidden tab, and a paused clock that does not tick on resume shows stale
// relative ages. The tests count ticks and timers rather than reading any
// label, so they survive copy changes.
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { appendPopout } from "../widgets/internal/popoutTestUtils";
import { useVisibleInterval } from "./useVisibleInterval";

const originalVisibility = Object.getOwnPropertyDescriptor(Document.prototype, "visibilityState");

function setVisibility(state: "visible" | "hidden"): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
}

/** Pin one document's visibilityState; jsdom reports "visible" for both the
 * opener and an iframe otherwise. */
function setDocumentVisibility(doc: Document, state: "visible" | "hidden"): void {
  Object.defineProperty(doc, "visibilityState", { configurable: true, get: () => state });
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

// A Dockview popout is a separate window whose visibility is its own: the user
// can hide the opener's tab while the popout stays on screen. A pane moved
// there must keep redrawing, so the gate watches the document it is handed,
// never the module global.
test("a popout document's clock keeps ticking while the opener's document is hidden", () => {
  vi.useFakeTimers();
  const { frame, doc } = appendPopout();
  try {
    setVisibility("visible");
    setDocumentVisibility(doc, "visible");
    const tick = vi.fn();
    renderHook(() => useVisibleInterval(tick, 1_000, true, doc));

    act(() => vi.advanceTimersByTime(2_000));
    expect(tick).toHaveBeenCalledTimes(2);

    // The reader switched away from the opener tab; the popout is still visible.
    setVisibility("hidden");
    dispatchVisibilityChange();
    expect(vi.getTimerCount()).toBe(1);
    act(() => vi.advanceTimersByTime(3_000));
    expect(tick).toHaveBeenCalledTimes(5);
  } finally {
    frame.remove();
  }
});

test("the opener's document hiding does not pause a clock watching the popout document", () => {
  vi.useFakeTimers();
  const { frame, doc } = appendPopout();
  try {
    // Only the opener is hidden; the popout never fires a visibilitychange of
    // its own, which is exactly the regression this guards: the old gate
    // listened on the opener and stopped the popout's clock for events that
    // were not about the popout at all.
    setVisibility("hidden");
    setDocumentVisibility(doc, "visible");
    const tick = vi.fn();
    renderHook(() => useVisibleInterval(tick, 1_000, true, doc));

    expect(vi.getTimerCount()).toBe(1);
    act(() => vi.advanceTimersByTime(4_000));
    expect(tick).toHaveBeenCalledTimes(4);
  } finally {
    frame.remove();
  }
});

test("the popout document hiding pauses only its own clock, and its resume ticks", () => {
  vi.useFakeTimers();
  const { frame, doc } = appendPopout();
  try {
    setVisibility("visible");
    setDocumentVisibility(doc, "visible");
    const tick = vi.fn();
    renderHook(() => useVisibleInterval(tick, 1_000, true, doc));
    act(() => vi.advanceTimersByTime(1_000));
    expect(tick).toHaveBeenCalledTimes(1);

    setDocumentVisibility(doc, "hidden");
    act(() => doc.dispatchEvent(new Event("visibilitychange")));
    expect(vi.getTimerCount()).toBe(0);
    act(() => vi.advanceTimersByTime(5_000));
    expect(tick).toHaveBeenCalledTimes(1);

    // The resume tick fires for the document that became visible again.
    setDocumentVisibility(doc, "visible");
    act(() => doc.dispatchEvent(new Event("visibilitychange")));
    expect(tick).toHaveBeenCalledTimes(2);
    act(() => vi.advanceTimersByTime(1_000));
    expect(tick).toHaveBeenCalledTimes(3);
  } finally {
    frame.remove();
  }
});
