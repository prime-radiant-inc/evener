import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { LivenessLine } from "./LivenessLine";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("renders nothing while the thread is not active, regardless of gap", () => {
  render(<LivenessLine lastFrameAt={0} now={999_999} active={false} />);
  expect(screen.queryByTestId("liveness-line")).toBeNull();
});

test("renders nothing for a fresh/active thread (gap below the quiet threshold)", () => {
  render(<LivenessLine lastFrameAt={1_000} now={1_500} active={true} />);
  expect(screen.queryByTestId("liveness-line")).toBeNull();
});

test("renders the quiet phrase once the gap crosses the quiet threshold", () => {
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} />);
  const el = screen.getByTestId("liveness-line");
  expect(el.textContent).toContain("~30s");
});

test("renders the stalled phrase once the gap crosses the stall threshold", () => {
  render(<LivenessLine lastFrameAt={0} now={185_000} active={true} />);
  const el = screen.getByTestId("liveness-line");
  expect(el.textContent!.toLowerCase()).toContain("stalled");
  expect(el.textContent).toContain("3m 5s");
});

// Cadence's own doc comment ("pure: no timers, no Date.now()") is the
// precedent this component follows for the same reason: the *caller* (which
// already owns a clock for Cadence - Session.tsx's useNowTick) controls
// decay, so a stalled/quiet thread shows honest, static text between
// renders rather than a self-ticking illusion of activity.
test("does not self-tick: the rendered text never changes without a new `now` prop, however much real time passes", () => {
  vi.useFakeTimers();
  render(<LivenessLine lastFrameAt={0} now={30_000} active={true} />);
  const before = screen.getByTestId("liveness-line").textContent;

  vi.advanceTimersByTime(10 * 60_000); // 10 real minutes, no re-render

  expect(screen.getByTestId("liveness-line").textContent).toBe(before);
});
