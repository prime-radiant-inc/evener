// The session clock is shared: the transcript's relative timestamps, the
// rail's ages, and Cadence's decay all read it. Its cadence is a product
// promise to the reader — a timestamp that rewrites itself too often reads
// as churn, so the tick must not fire more often than every 5s.
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { PaneDocumentContext } from "../../shell/paneDocument";
import { appendPopout } from "../../widgets/internal/popoutTestUtils";
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

// A session moved into a Dockview popout is in a window the user is still
// looking at when the opener's tab goes away. Its clock must follow it there:
// the pane's own document governs, not the module global.
test("the session clock follows its pane into a popout document", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const { frame, doc } = appendPopout();
  try {
    Object.defineProperty(doc, "visibilityState", { configurable: true, get: () => "visible" });
    setVisibility("hidden");
    const { result } = renderHook(() => useNowTick(NOW_TICK_MS), {
      wrapper: ({ children }) => <PaneDocumentContext.Provider value={doc}>{children}</PaneDocumentContext.Provider>,
    });
    const start = result.current;

    // The opener is hidden and never becomes visible here; only the popout
    // document matters, and it stays visible.
    act(() => vi.advanceTimersByTime(NOW_TICK_MS * 3));
    expect(result.current).toBeGreaterThan(start);
  } finally {
    frame.remove();
  }
});
