// The watch row's tick contract: only an OPEN watch detail may re-render on the
// tree's one-second clock. The row itself carries snapshot data (cadence,
// armed age anchor, delivery count), so a collapsed row that subscribed to the
// clock would re-render every second with identical output - pure waste. This
// pins the subscription boundary by counting calls into the row-only and
// detail-only formatters on a tick.
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { ActivityTree as ActivityTreeData } from "../../../protocol/activityData";
// The row and detail components import their formatters from the package root,
// so the spies have to name the same module: a spy hung on the protocol/ shim
// would sit on a different module object and never see a call.
import * as activityRows from "@evener/appwire-client";
import type { NavigationWatchSummary } from "../../../protocol/types.gen";
import { ActivityTree } from "./ActivityTree";

const NOW = Date.parse("2026-08-05T15:00:12.000Z");
const CREATED = "2026-08-05T12:48:00Z";

function watch(overrides: Partial<NavigationWatchSummary> = {}): NavigationWatchSummary {
  return {
    id: "watch_1",
    source: "sess_root",
    deliveries: 0,
    created_at: CREATED,
    active: true,
    cadence: [{ kind: "every", seconds: 60 }],
    ...overrides,
  };
}

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

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("ActivityTree watch row ticks", () => {
  test("a collapsed watch row does not re-render on a tick while an open one still updates", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    try {
      // watchMeta is called by WatchRowView on every row render; watchFacts
      // only by ActivityWatchDetail on every detail render. A spy on the
      // module export sees both through the same live bindings the components
      // import.
      const rowRender = vi.spyOn(activityRows, "watchMeta");
      const detailRender = vi.spyOn(activityRows, "watchFacts");

      render(
        <ActivityTree
          tree={EMPTY_TREE}
          watches={[
            watch({ id: "watch_open", note: "Hourly sweep" }),
            watch({ id: "watch_closed", note: "Deploy rollback check" }),
          ]}
          expandedFoldIDs={[]}
          onToggleFold={vi.fn()}
        />,
      );

      // Both rows default open. Collapse the second one.
      const closedRow = screen.getByRole("treeitem", { name: "Watch: Deploy rollback check" });
      fireEvent.click(within(closedRow).getByRole("button", { name: "Hide details for Watch: Deploy rollback check" }));
      expect(closedRow.getAttribute("aria-expanded")).toBe("false");
      expect(screen.queryAllByTestId("watch-facts")).toHaveLength(1);

      // Counts as of the settle after the collapse interaction.
      const rowsAtRest = rowRender.mock.calls.length;
      const detailsAtRest = detailRender.mock.calls.length;

      act(() => {
        vi.advanceTimersByTime(1000);
      });

      // The open row's countdown meta and detail follow the clock...
      expect(detailRender.mock.calls.length).toBeGreaterThan(detailsAtRest);
      // ...while the collapsed row stays asleep: its formatter is never called
      // again, and only the open row's meta re-renders through the tree clock.
      const rerendered = rowRender.mock.calls.slice(rowsAtRest).map((call) => call[0].id);
      expect(rerendered).toEqual(["watch_open"]);
    } finally {
      vi.useRealTimers();
    }
  });

  // The pane chrome passes its own ticking clock down as `now`, which changes
  // every second. The tree re-renders with it, but a collapsed watch row must
  // stay asleep: it carries no clock-derived parts. An open row does carry them
  // (the countdown in its meta, the detail's ages), so it must keep following
  // the clock.
  test("a ticking panel clock re-renders the open watch row and leaves the collapsed one alone", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    try {
      const rowRender = vi.spyOn(activityRows, "watchMeta");
      const detailRender = vi.spyOn(activityRows, "watchFacts");
      // Four minutes after NOW, so each tick visibly shortens the countdown.
      const nextFire = "2026-08-05T15:04:12Z";
      const openWatch = watch({
        id: "watch_open",
        note: "Hourly sweep",
        cadence: [{ kind: "every", seconds: 600, derived_next_fire_at: nextFire }],
      });
      const closedWatch = watch({
        id: "watch_closed",
        note: "Deploy rollback check",
        cadence: [{ kind: "every", seconds: 600, derived_next_fire_at: nextFire }],
      });
      // One array, the way the panel holds the session's wire list: a clock
      // tick re-renders the tree, it does not mint new watch rows.
      const watches = [openWatch, closedWatch];
      const expandedFoldIDs: string[] = [];
      const onToggleFold = vi.fn();
      const tree = (now: number) => (
        <ActivityTree
          tree={EMPTY_TREE}
          watches={watches}
          now={now}
          expandedFoldIDs={expandedFoldIDs}
          onToggleFold={onToggleFold}
        />
      );
      const { rerender } = render(tree(NOW));

      // Both rows default open. Collapse the second one.
      const closedRow = screen.getByRole("treeitem", { name: "Watch: Deploy rollback check" });
      fireEvent.click(within(closedRow).getByRole("button", { name: "Hide details for Watch: Deploy rollback check" }));
      expect(closedRow.getAttribute("aria-expanded")).toBe("false");
      // A collapsed row renders no clock-derived countdown...
      expect(closedRow.textContent).not.toContain("next");
      const closedText = closedRow.textContent;
      // ...while the open row still counts down.
      const openRow = screen.getByRole("treeitem", { name: "Watch: Hourly sweep" });
      expect(openRow.textContent).toContain("next ~4m");

      const rowsAtRest = rowRender.mock.calls.length;
      const detailsAtRest = detailRender.mock.calls.length;

      act(() => {
        // The panel re-rendered with its next second, exactly as the chrome's
        // useNowTick does.
        rerender(tree(NOW + 1000));
      });

      // The open row followed the clock...
      expect(openRow.textContent).toContain("next ~3m59s");
      expect(detailRender.mock.calls.length).toBeGreaterThan(detailsAtRest);
      // ...and the collapsed row never re-rendered: its formatter was called
      // again only for the open row.
      const rerendered = rowRender.mock.calls.slice(rowsAtRest).map((call) => call[0].id);
      expect(rerendered).toEqual(["watch_open"]);
      expect(closedRow.textContent).toBe(closedText);
    } finally {
      vi.useRealTimers();
    }
  });
});
