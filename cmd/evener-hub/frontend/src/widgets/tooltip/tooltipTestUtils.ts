import { act, fireEvent, screen } from "@testing-library/react";
import { vi } from "vitest";
import { SHOW_DELAY_MS } from "../hovercard/useFloatingLabel";

/**
 * Hovers `trigger` and returns the tooltip it shows. The show delay is a real
 * timer, and a findBy that waits it out races that delay against its own
 * ceiling, a race a starved host can lose. This crosses the delay on a fake
 * clock scoped to the hover instead, then hands the real timers back.
 *
 * Call it on real timers: installing its clock would replace a test's own
 * fake clock and drop that clock's pending timers, so it refuses instead.
 */
export function hoverForTooltip(trigger: Element): HTMLElement {
  if (vi.isFakeTimers()) throw new Error("hoverForTooltip installs its own fake clock; call it on real timers");
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
