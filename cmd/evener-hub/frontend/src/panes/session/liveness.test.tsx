// The session clock is shared: the transcript's relative timestamps, the
// rail's ages, and Cadence's decay all read it. Its cadence is a product
// promise to the reader — a timestamp that rewrites itself too often reads
// as churn, so the tick must not fire more often than every 5s.
import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { NOW_TICK_MS, useNowTick } from "./liveness";

function ClockProbe() {
  const now = useNowTick(NOW_TICK_MS);
  return <output data-testid="clock">{now}</output>;
}

afterEach(() => {
  vi.useRealTimers();
});

test("the session clock advances no more often than every 5s", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  render(<ClockProbe />);
  const start = screen.getByTestId("clock").textContent;

  act(() => {
    vi.advanceTimersByTime(4_999);
  });
  expect(screen.getByTestId("clock").textContent).toBe(start);

  act(() => {
    vi.advanceTimersByTime(1);
  });
  expect(screen.getByTestId("clock").textContent).not.toBe(start);
});
