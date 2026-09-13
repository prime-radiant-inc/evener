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

      // The open detail follows the clock...
      expect(detailRender.mock.calls.length).toBeGreaterThan(detailsAtRest);
      // ...while no watch ROW re-renders, collapsed or open.
      expect(rowRender.mock.calls.length).toBe(rowsAtRest);
    } finally {
      vi.useRealTimers();
    }
  });
});
