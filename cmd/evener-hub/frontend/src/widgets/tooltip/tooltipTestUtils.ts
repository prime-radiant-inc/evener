import { act, fireEvent, screen } from "@testing-library/react";
import { vi } from "vitest";
import { SHOW_DELAY_MS } from "../hovercard/useFloatingLabel";

/**
 * Hovers `trigger` and returns the tooltip it shows. The show delay is a real
 * timer, and a findBy that waits it out races that delay against its own
 * ceiling, a race a starved host can lose. This crosses the delay on a faked
 * clock instead, then hands the real timers back, so the rest of the test runs
 * on them as before.
 */
export function hoverForTooltip(trigger: Element): HTMLElement {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    fireEvent.mouseEnter(trigger);
    act(() => {
      vi.advanceTimersByTime(SHOW_DELAY_MS);
    });
  } finally {
    vi.useRealTimers();
  }
  return screen.getByRole("tooltip");
}
