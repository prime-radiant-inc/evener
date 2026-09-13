// DOM assertions for the watch rows the ActivityTree renders: row header,
// accessible name, expand/collapse through a real button, and the expanded
// note/facts/no-schedule detail. Real props, real component - no mocks of the
// subject and no snapshot-only assertions.
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { ActivityTree as ActivityTreeData } from "../../../protocol/activityData";
import { formatClockTime } from "../../../protocol/displayFormat";
import type { NavigationWatchSummary } from "../../../protocol/types.gen";
import { watchDurationLabel } from "../../../shell/rail/RailRow";
import { ActivityTree } from "./ActivityTree";

const NOW = Date.parse("2026-08-05T15:00:12.000Z");
const CREATED = "2026-08-05T12:48:00Z";
const NO_SCHEDULE =
  "There is no schedule to draw here — this one fires when the job says the word, not when a clock says so.";

function watch(overrides: Partial<NavigationWatchSummary> = {}): NavigationWatchSummary {
  return {
    id: "watch_1",
    source: "sess_root",
    deliveries: 0,
    created_at: CREATED,
    active: true,
    ...overrides,
  };
}

function armedLabel(): string {
  return watchDurationLabel((NOW - Date.parse(CREATED)) / 1000);
}

// An empty root so watch rows are the only rows unless a test adds one.
const EMPTY_TREE: ActivityTreeData = {
  revision: 1,
  root: {
    kind: "session",
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root",
    aggregate: "idle",
    counts: { active: 0, failed: 0, completed: 0, complete: true },
    entries: [],
    branch: {},
  },
};

function renderTree(watches?: NavigationWatchSummary[], tree: ActivityTreeData = EMPTY_TREE) {
  return render(<ActivityTree tree={tree} watches={watches} now={NOW} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("ActivityTree watch rows", () => {
  test("a scheduled watch renders a named row with its cadence and delivery count, under a Watches group", () => {
    renderTree([
      watch({
        id: "watch_poll",
        note: "Poll the queue depth",
        cadence: [{ kind: "every", seconds: 600 }],
        deliveries: 3,
      }),
      watch({
        id: "watch_idle",
        note: "Deploy rollback check",
        cadence: [{ kind: "every", seconds: 600 }],
        deliveries: 0,
      }),
    ]);

    const row = screen.getByRole("treeitem", { name: "Watch: Poll the queue depth" });
    expect(row.textContent).toContain("every 10m");
    expect(row.textContent).toContain("3 deliveries");
    expect(within(row).getByText("Watch:")).toBeTruthy();
    expect(row.querySelector('[data-testid="watch-glyph"]')).not.toBeNull();

    expect(screen.getByTestId("watch-group")).toBeTruthy();
    expect(screen.getByText("Watches")).toBeTruthy();
    expect(screen.getByText("2 armed")).toBeTruthy();
  });

  test("a watch with no note falls back to its id, exactly like the rail", () => {
    renderTree([watch({ id: "watch_bare", note: undefined })]);
    expect(screen.getByRole("treeitem", { name: "Watch: watch_bare" })).toBeTruthy();
    expect(screen.queryByTestId("watch-note")).toBeNull();
  });

  test("the row defaults open and its toggle button is a real aria-expanded disclosure", async () => {
    const user = userEvent.setup();
    renderTree([watch({ note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }], deliveries: 0 })]);

    const row = screen.getByRole("treeitem", { name: "Watch: Poll the queue depth" });
    expect(row.getAttribute("aria-expanded")).toBe("true");
    // The row's own name already shows this short note in full; the detail must
    // not repeat it as a lead paragraph.
    expect(screen.queryByTestId("watch-note")).toBeNull();
    expect(screen.getByTestId("watch-facts").textContent).toBe(
      `Fires every 10m · armed ${armedLabel()} ago · no deliveries yet`,
    );

    await user.click(within(row).getByRole("button", { name: "Hide details for Watch: Poll the queue depth" }));
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("watch-note")).toBeNull();
    expect(screen.queryByTestId("watch-facts")).toBeNull();
  });

  test("watch rows lead the existing activity rows", () => {
    const tree: ActivityTreeData = {
      ...EMPTY_TREE,
      root: {
        ...EMPTY_TREE.root,
        entries: [
          {
            kind: "shell",
            job: {
              jobId: "job_1",
              ownerSessionId: "sess_root",
              ownerRef: "ref_root",
              type: "shell",
              status: "running",
              terminal: false,
              background: false,
              hasOutput: false,
              description: "run tests",
              startedAt: "2026-08-05T15:00:00Z",
              outputBytes: 0,
            },
          },
        ],
      },
    };
    renderTree([watch({ note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }] })], tree);
    const items = screen.getAllByRole("treeitem");
    expect(items[0]?.getAttribute("aria-label")).toBe("Watch: Poll the queue depth");
    expect(items[1]?.getAttribute("aria-label")).toBe("run tests");
  });

  test("an output watch renders the target/match facts and the no-schedule line instead of a timeline", () => {
    renderTree([
      watch({
        id: "watch_out",
        note: "Migration prints DONE",
        target: "job_ab12cd",
        output_match: "/DONE/",
        cadence: [{ kind: "output" }],
        deliveries: 0,
      }),
    ]);
    expect(screen.getByTestId("watch-facts").textContent).toBe(
      `Waiting on job_ab12cd, matching /DONE/ · armed ${armedLabel()} ago · no deliveries yet`,
    );
    expect(screen.getByTestId("watch-no-schedule").textContent).toBe(NO_SCHEDULE);
    expect(screen.queryByTestId("watch-timeline")).toBeNull();
  });

  test("an event watch renders the joined event names and the no-schedule line", () => {
    renderTree([
      watch({
        id: "watch_evt",
        note: "Watch the job lifecycle",
        events: ["job.completed", "job.failed"],
        cadence: [{ kind: "events" }],
        deliveries: 2,
      }),
    ]);
    expect(screen.getByTestId("watch-facts").textContent).toBe(
      `Waiting on job.completed, job.failed · armed ${armedLabel()} ago · 2 deliveries`,
    );
    expect(screen.getByTestId("watch-no-schedule").textContent).toBe(NO_SCHEDULE);
  });

  test("a wildcard event watch reads as session events", () => {
    renderTree([
      watch({
        id: "watch_wild",
        note: "Any session event",
        wildcard_events: true,
        events: [],
        cadence: [{ kind: "events" }],
        deliveries: 0,
      }),
    ]);
    expect(screen.getByTestId("watch-facts").textContent).toBe(
      `Waiting on session events · armed ${armedLabel()} ago · no deliveries yet`,
    );
  });

  test("a scheduled watch's newest delivery instant renders as local HH:MM", () => {
    renderTree([
      watch({
        id: "watch_sched",
        note: "Check the deploy log",
        cadence: [{ kind: "every", seconds: 600 }],
        deliveries: 3,
        delivery_times: ["2026-08-05T14:50:00Z", "2026-08-05T14:58:00Z"],
      }),
    ]);
    const facts = screen.getByTestId("watch-facts").textContent ?? "";
    expect(facts).toContain(`last at ${formatClockTime("2026-08-05T14:58:00Z")}`);
  });

  test("a scheduled watch with retained instants draws its timeline inside the tree", () => {
    renderTree([
      watch({
        id: "watch_sched",
        note: "Check the deploy log",
        cadence: [{ kind: "every", seconds: 600 }],
        deliveries: 3,
        delivery_times: ["2026-08-05T13:00:00Z", "2026-08-05T14:00:00Z", "2026-08-05T15:00:00Z"],
      }),
    ]);
    expect(screen.getByTestId("watch-timeline")).toBeTruthy();
    expect(screen.getAllByTestId("watch-timeline-dot")).toHaveLength(3);
    expect(screen.getByTestId("watch-timeline-caption").textContent).toBe("Delivered to this session");
    expect(screen.queryByTestId("watch-no-schedule")).toBeNull();
  });

  test("no watch surface mentions drops, delivery completeness, next fire, or a countdown", () => {
    renderTree([
      watch({
        id: "watch_a",
        note: "Poll the queue depth",
        cadence: [{ kind: "every", seconds: 600 }],
        deliveries: 3,
        delivery_times: ["2026-08-05T14:58:00Z"],
      }),
      watch({ id: "watch_b", note: "Migration prints DONE", output_match: "/DONE/", target: "j", deliveries: 0 }),
      watch({ id: "watch_c", note: "Watch events", events: ["a"], deliveries: 0 }),
    ]);
    const text = document.body.textContent ?? "";
    expect(text).not.toMatch(/dropped/i);
    expect(text).not.toMatch(/all delivered/i);
    expect(text).not.toMatch(/\bnext\b/i);
    expect(text).not.toMatch(/countdown/i);
  });

  test("an absent watch list renders exactly as an empty session did before", () => {
    renderTree(undefined);
    expect(screen.queryByTestId("watch-group")).toBeNull();
    expect(screen.queryAllByRole("treeitem")).toHaveLength(0);
    expect(screen.getByRole("tree")).toBeTruthy();
  });
});
