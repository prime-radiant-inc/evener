// DOM assertions for the watch detail's delivery timeline: real props, real
// component, positions derived only from the supplied instants and `now`.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test } from "vitest";
import { type ActivityWatchRow, watchRowID } from "../../../protocol/activityRows";
import { formatClockTime } from "../../../protocol/displayFormat";
import type { NavigationWatchSummary } from "../../../protocol/types.gen";
import { ActivityWatchDetail, WATCH_NO_SCHEDULE_LINE } from "./ActivityRowDetail";

const NOW = Date.parse("2026-08-05T15:00:12.000Z");
const CREATED = "2026-08-05T12:48:00Z";

function row(overrides: Partial<NavigationWatchSummary> = {}): ActivityWatchRow {
  const watch: NavigationWatchSummary = {
    id: "watch_1",
    source: "sess_root",
    deliveries: 0,
    created_at: CREATED,
    active: true,
    ...overrides,
  };
  return { kind: "watch", id: watchRowID(watch.id), level: 1, watch, defaultDetailOpen: true };
}

function leftOf(element: HTMLElement): number {
  const value = element.style.left;
  return Number.parseFloat(value);
}

afterEach(() => {
  cleanup();
});

describe("ActivityWatchDetail timeline", () => {
  const INSTANTS = ["2026-08-05T13:00:00Z", "2026-08-05T14:00:00Z", "2026-08-05T15:00:00Z"];

  test("draws one dot per supplied instant with non-decreasing positions inside the rail", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    const dots = screen.getAllByTestId("watch-timeline-dot");
    expect(dots).toHaveLength(3);
    const positions = dots.map((dot) => leftOf(dot));
    for (const position of positions) {
      expect(position).toBeGreaterThanOrEqual(0);
      expect(position).toBeLessThanOrEqual(100);
    }
    expect(positions[0]).toBeLessThan(positions[1] as number);
    expect(positions[1]).toBeLessThan(positions[2] as number);
  });

  test("draws a now marker at the right end and hides the marks from assistive tech", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    const marker = screen.getByTestId("watch-timeline-now");
    expect(leftOf(marker)).toBe(100);
    expect(marker.getAttribute("aria-hidden")).toBe("true");
    for (const dot of screen.getAllByTestId("watch-timeline-dot")) {
      expect(dot.getAttribute("aria-hidden")).toBe("true");
    }
    const rail = screen.getByTestId("watch-timeline-rail");
    expect(rail.getAttribute("role")).toBe("img");
    expect(rail.getAttribute("aria-label")).toBeTruthy();
  });

  test("labels the rail with the earliest retained instant and now, both local HH:MM", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    expect(screen.getByTestId("watch-timeline-start").textContent).toBe(formatClockTime(INSTANTS[0]));
    expect(screen.getByTestId("watch-timeline-end").textContent).toBe(
      `now ${formatClockTime(new Date(NOW).toISOString())}`,
    );
  });

  test("caps the caption when deliveries exceed the retained instants", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 214, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    expect(screen.getByTestId("watch-timeline-caption").textContent).toBe("Last 3 of 214 deliveries");
  });

  test("says delivered to this session when the count fits inside the ring", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    expect(screen.getByTestId("watch-timeline-caption").textContent).toBe("Delivered to this session");
  });

  test("an output watch gets the no-schedule line and no timeline", () => {
    render(
      <ActivityWatchDetail
        row={row({ target: "job_ab12cd", output_match: "/DONE/", cadence: [{ kind: "output" }], deliveries: 4 })}
        now={NOW}
      />,
    );
    expect(screen.queryByTestId("watch-timeline")).toBeNull();
    expect(screen.queryAllByTestId("watch-timeline-dot")).toHaveLength(0);
    expect(screen.getByTestId("watch-no-schedule").textContent).toBe(WATCH_NO_SCHEDULE_LINE);
  });

  test("an event watch gets the no-schedule line and no timeline", () => {
    render(
      <ActivityWatchDetail
        row={row({ events: ["job.completed"], cadence: [{ kind: "events" }], deliveries: 1 })}
        now={NOW}
      />,
    );
    expect(screen.queryByTestId("watch-timeline")).toBeNull();
    expect(screen.getByTestId("watch-no-schedule").textContent).toBe(WATCH_NO_SCHEDULE_LINE);
  });

  test("a scheduled watch with no instants draws neither timeline nor no-schedule line", () => {
    render(<ActivityWatchDetail row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 0 })} now={NOW} />);
    expect(screen.queryByTestId("watch-timeline")).toBeNull();
    expect(screen.queryByTestId("watch-no-schedule")).toBeNull();
    expect(screen.getByTestId("watch-facts").textContent).toContain("no deliveries yet");
  });

  test("the timeline text never mentions drops or a next fire", () => {
    render(
      <ActivityWatchDetail
        row={row({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 214, delivery_times: INSTANTS })}
        now={NOW}
      />,
    );
    const text = document.body.textContent ?? "";
    expect(text).not.toMatch(/dropped/i);
    expect(text).not.toMatch(/\bnext\b/i);
    expect(text).not.toMatch(/countdown/i);
  });
});
