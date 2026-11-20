import { buildWatchRows } from "@evener/appwire-client";
import { act, cleanup, fireEvent, type RenderResult, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { activityDelegate, activityJob, activityWatch } from "../../stores/sessionActivityTestUtils";
import { Chevron, IconButton } from "../../widgets";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { AgentRow, JobRow, WatchRow } from "./activityRows";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetDisclosureStoreForTests();
});

const NOW = Date.parse("2026-10-05T00:00:00Z");

function timestamp(view: RenderResult): HTMLTimeElement {
  const element = view.container.querySelector<HTMLTimeElement>("time[data-activity-time]");
  if (!element) throw new Error("row has no activity timestamp");
  return element;
}

/** The rendered dot, by the state and shape the widget writes: construction,
 * not copy - a dot that broke its hue or spinner must fail, not pass. */
const dot = (view: RenderResult) => {
  const node = view.container.querySelector<HTMLElement>("[data-state]");
  if (!node) throw new Error("row renders no dot");
  return node;
};

test("a fresh running delegate shows the alive dot", () => {
  const view = render(
    <AgentRow now={NOW} sub={activityDelegate({ latestActivityAt: new Date(Date.now() - 5_000).toISOString() })} />,
  );
  const fresh = dot(view);
  expect(fresh.getAttribute("data-state")).toBe("fresh");
  expect(fresh.getAttribute("data-shape")).toBe("dot");
  expect(fresh.className).toContain("alive");
  // The working spinner, so an active row reads as active.
  expect(fresh.className).toContain("working");
  // The dot stays the one labeled image; its wording is not this suite's
  // oracle, but its name must not be empty.
  expect(screen.getByRole("img").getAttribute("aria-label")).toMatch(/\S/);
});

// The web keeps an open run's working ring until terminal settlement. A >20s
// tool call, reasoning turn or child wait is still active work, so the shared
// quiet state renders as fresh here instead of dimming the row.
test("an open delegate past the quiet threshold keeps its working dot", () => {
  const view = render(
    <AgentRow now={NOW} sub={activityDelegate({ latestActivityAt: new Date(NOW - 5 * 60_000).toISOString() })} />,
  );
  const alive = dot(view);
  expect(alive.getAttribute("data-state")).toBe("fresh");
  expect(alive.getAttribute("data-shape")).toBe("dot");
  expect(alive.className).toContain("alive");
  expect(alive.className).toContain("working");
});

test("an open delegate's working dot survives timer advances past the threshold", () => {
  vi.useFakeTimers();
  const now = Date.now();
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({
        runStartedAt: new Date(now).toISOString(),
        latestActivityAt: new Date(now).toISOString(),
      })}
    />,
  );
  expect(dot(view).getAttribute("data-state")).toBe("fresh");
  act(() => {
    vi.advanceTimersByTime(20_001);
  });
  const alive = dot(view);
  expect(alive.getAttribute("data-state")).toBe("fresh");
  expect(alive.getAttribute("data-shape")).toBe("dot");
  expect(alive.className).toContain("alive");
  expect(alive.className).toContain("working");
  act(() => {
    vi.advanceTimersByTime(60_000);
  });
  expect(dot(view).getAttribute("data-state")).toBe("fresh");
  expect(dot(view).className).toContain("working");
});

test("a delegate whose activity anchor runs ahead of the clock keeps its working dot", () => {
  vi.useFakeTimers();
  const now = Date.now();
  const view = render(
    <AgentRow now={NOW} sub={activityDelegate({ latestActivityAt: new Date(now + 30_000).toISOString() })} />,
  );
  expect(dot(view).getAttribute("data-state")).toBe("fresh");
  act(() => {
    vi.advanceTimersByTime(55_000);
  });
  const alive = dot(view);
  expect(alive.getAttribute("data-state")).toBe("fresh");
  expect(alive.getAttribute("data-shape")).toBe("dot");
  expect(alive.className).toContain("alive");
  expect(alive.className).toContain("working");
});

test("a delegate awaiting input keeps the attention dot", () => {
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: false, phase: "awaiting_input", latestActivityAt: new Date().toISOString() })}
    />,
  );
  const attention = dot(view);
  expect(attention.getAttribute("data-state")).toBe("attention");
  expect(attention.getAttribute("data-shape")).toBe("dot");
  expect(attention.className).toContain("attention");
});

test("a failed delegate shows the danger dot", () => {
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })}
    />,
  );
  const failed = dot(view);
  expect(failed.getAttribute("data-state")).toBe("failed");
  // The diamond shape, so failure never depends on hue alone.
  expect(failed.getAttribute("data-shape")).toBe("diamond");
  expect(failed.className).toContain("danger");
});

test("a settled delegate shows no dot at all", () => {
  const view = render(<AgentRow now={NOW} sub={activityDelegate({ terminal: true, outcome: "completed" })} />);
  expect(view.container.querySelector("[data-state]")).toBeNull();
  expect(screen.queryByRole("img")).toBeNull();
});

test("stable delegate row uses authoritative phase and lifecycle without fabricated rollup counts", () => {
  render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ lifecycle: "idle", phase: "waiting", status: "idle", terminal: false })}
    />,
  );
  expect(screen.getByText("inspect")).toBeTruthy();
  expect(screen.queryByText(/agents|jobs/)).toBeNull();
});
test("ended delegate preserves the supplied failed outcome", () => {
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })}
    />,
  );
  const row = screen.getByRole("img", { name: "Failed" }).closest("[aria-description]");
  expect(row?.getAttribute("aria-description")).toBe("failed");
  expect(view.container.querySelector("[data-activity-meta]")?.getAttribute("data-failed")).toBe("true");
});

test.each([
  { status: "completed", outcome: "success", compact: true },
  { status: "command_exited_nonzero", outcome: "failure", compact: false },
  { status: "command_killed", outcome: "failure", compact: false },
  { status: "cancelled", outcome: undefined, compact: false },
  { status: "stopped", outcome: undefined, compact: false },
  { status: "unknown", outcome: undefined, compact: false },
  { status: "failed", reason: "runtime_timeout", outcome: "failure", compact: false },
  { status: "running", outcome: undefined, compact: false },
])("terminal $status jobs retain their outcome with a quiet glyph", ({ status, reason, outcome, compact }) => {
  const view = render(<JobRow now={NOW} job={activityJob({ status, reason, outcome, terminal: true })} />);
  const row = view.container.querySelector("[aria-description]");
  expect(row?.getAttribute("aria-description")?.length).toBeGreaterThan(0);
  expect(view.container.querySelector("[data-activity-status]")?.getAttribute("data-status")).toBe(status);
  expect(view.container.querySelector("[data-activity-meta]") !== null).toBe(!compact);
  expect(view.container.querySelector("svg")?.hasAttribute("data-tone")).toBe(false);
});

test.each(["awaiting_input", "awaiting_approval"])("live $phase delegates keep attention", (phase) => {
  render(<AgentRow now={NOW} sub={activityDelegate({ terminal: false, phase })} />);
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test("live shell work keeps its alive glyph", () => {
  const view = render(<JobRow now={NOW} job={activityJob({ terminal: false, status: "running" })} />);
  expect(view.container.querySelector("svg")?.getAttribute("data-tone")).toBe("alive");
});

test("job rows identify the work by its description while retaining the exact command", () => {
  const command = "while ! test -e release-monitor; do sleep 2; done";
  const view = render(<JobRow now={NOW} job={activityJob({ description: "Release monitor", command })} />);
  expect(screen.getByText("Release monitor").getAttribute("title")).toBe(command);
  view.rerender(<JobRow now={NOW} job={activityJob({ description: "", command })} />);
  expect(screen.getByText(command)).toBeTruthy();
  view.rerender(<JobRow now={NOW} job={activityJob({ description: "", command: "", jobId: "job-no-label" })} />);
  expect(screen.getByText("job-no-label")).toBeTruthy();
});

test("delegate row prefers compact name while unnamed rows retain their prompt fallback", () => {
  const { rerender } = render(
    <AgentRow
      now={NOW}
      sub={{ ...activityDelegate({ description: "Inspect the complete cache ownership" }), name: "inspect-cache" }}
    />,
  );
  expect(screen.getByText("inspect-cache")).toBeTruthy();
  expect(screen.queryByText("Inspect the complete cache ownership")).toBeNull();
  rerender(
    <AgentRow
      now={NOW}
      sub={{ ...activityDelegate({ description: "Inspect the complete cache ownership" }), name: " " }}
    />,
  );
  expect(screen.getByText("Inspect the complete cache ownership")).toBeTruthy();
});

test("a working agent row shows its latest intent in the meta line", () => {
  render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ status: "running", terminal: false, latestIntent: "Reading the board." })}
    />,
  );
  expect(screen.getByText("Reading the board.")).toBeTruthy();
  expect(document.querySelector("[data-activity-meta]")?.textContent).not.toBe("running");
});

test("a live agent waiting in idle keeps its current status visible", () => {
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: false, lifecycle: "running", phase: "idle", status: "idle" })}
    />,
  );
  expect(view.container.querySelector("[data-activity-meta]")?.textContent).toBe("idle");
});

test.each(["cancelled", "stopped", "exhausted", "unknown"])(
  "a terminal agent retains its non-success outcome meta: %s",
  (outcome) => {
    const view = render(
      <AgentRow
        now={NOW}
        sub={activityDelegate({ terminal: true, status: "idle", outcome, latestIntent: "Stale intent." })}
      />,
    );
    const meta = view.container.querySelector("[data-activity-meta]");
    expect(meta?.textContent).toBe(outcome);
    expect(meta?.textContent).not.toBe("Stale intent.");
  },
);

test("a terminal agent row keeps its status accessible over a stale intent", () => {
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: true, outcome: "completed", status: "completed", latestIntent: "Stale line." })}
    />,
  );
  expect(view.container.querySelector("[aria-description]")?.getAttribute("aria-description")).toBe("completed");
  expect(screen.queryByText("Stale line.")).toBeNull();
});

test("an agent row carries its runtime status in the row's title", () => {
  render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ status: "running", terminal: false, latestIntent: "Reading the board." })}
    />,
  );
  // JobRow's pattern: the name span's title carries what the meta line replaced.
  expect(screen.getByTitle("inspect · running")).toBeTruthy();
});

test("a resumed running agent timestamps the current run, not its previous activity", () => {
  const startedAt = "2026-10-04T23:55:00Z";
  const latestActivityAt = "2026-10-04T23:50:00Z";
  const view = render(<AgentRow now={NOW} sub={activityDelegate({ runStartedAt: startedAt, latestActivityAt })} />);
  const time = timestamp(view);
  expect(time.dateTime).toBe(startedAt);
  expect(time.dataset.meaning).toBe("started");
  expect(time.textContent?.trim().length).toBeGreaterThan(0);
});

test("a settled agent removes successful status repetition but keeps its completion time", () => {
  const endedAt = "2026-10-04T23:30:00Z";
  const view = render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ terminal: true, status: "completed", outcome: "completed", runEndedAt: endedAt })}
    />,
  );
  expect(timestamp(view).dateTime).toBe(endedAt);
  expect(view.container.querySelector("[data-activity-meta]")).toBeNull();
  expect(view.container.querySelector("[data-activity-status]")?.getAttribute("data-status")).toBe("completed");
  expect(view.container.querySelector("[data-state]")).toBeNull();
});

test.each(["completed", "command_exited_nonzero", "cancelled", "stopped", "unknown"])(
  "a terminal job keeps its outcome semantics while adding its end time: %s",
  (status) => {
    const endedAt = "2026-10-04T23:45:00Z";
    const view = render(
      <JobRow
        now={NOW}
        job={activityJob({ terminal: true, status, outcome: status === "completed" ? "success" : undefined, endedAt })}
      />,
    );
    const time = timestamp(view);
    expect(time.dateTime).toBe(endedAt);
    expect(time.dataset.meaning).toBe("ended");
    expect(view.container.querySelector("[data-activity-status]")?.getAttribute("data-status")).toBe(status);
    expect(view.container.querySelector("[data-activity-meta]") !== null).toBe(status !== "completed");
  },
);

test("a running job uses latest output and advances when the supplied clock advances", () => {
  const startedAt = "2026-10-04T23:00:00Z";
  const lastOutputAt = "2026-10-04T23:50:00Z";
  const view = render(<JobRow now={NOW} job={activityJob({ startedAt, lastOutputAt })} />);
  expect(timestamp(view).dateTime).toBe(lastOutputAt);
  expect(timestamp(view).dataset.meaning).toBe("output");
  const beforeTime = timestamp(view).textContent;
  view.rerender(<JobRow now={NOW + 60_000} job={activityJob({ startedAt, lastOutputAt })} />);
  expect(timestamp(view).dateTime).toBe(lastOutputAt);
  expect(timestamp(view).textContent).not.toBe(beforeTime);
});

test("a running job without output timestamps its start", () => {
  const startedAt = "2026-10-04T23:40:00Z";
  const view = render(<JobRow now={NOW} job={activityJob({ startedAt, lastOutputAt: undefined })} />);
  expect(timestamp(view).dateTime).toBe(startedAt);
  expect(timestamp(view).dataset.meaning).toBe("started");
});

test("an agent without timing evidence does not invent a timestamp", () => {
  const view = render(<AgentRow now={NOW} sub={activityDelegate()} />);
  expect(view.container.querySelector("time")).toBeNull();
});

test("a delivered watch uses its latest delivery and retains its disclosure", async () => {
  const deliveredAt = "2026-10-04T23:55:00Z";
  const watch = activityWatch({ deliveries: 2, deliveryTimes: ["2026-10-04T23:50:00Z", deliveredAt] });
  const row = buildWatchRows([watch])[0];
  if (!row) throw new Error("watch fixture missing");
  const view = render(<WatchRow row={row} now={NOW} />);
  expect(timestamp(view).dateTime).toBe(deliveredAt);
  expect(timestamp(view).dataset.meaning).toBe("delivered");
  await userEvent.click(view.container.querySelector("summary") as HTMLElement);
  expect(view.container.querySelector("details")?.open).toBe(true);
});

test("an armed watch without retained delivery times keeps its creation-time meaning", () => {
  const createdAt = "2026-10-04T23:40:00Z";
  const row = buildWatchRows([activityWatch({ createdAt, deliveries: 4, deliveryTimes: undefined })])[0];
  if (!row) throw new Error("watch fixture missing");
  const view = render(<WatchRow row={row} now={NOW} />);
  expect(timestamp(view).dateTime).toBe(createdAt);
  expect(timestamp(view).dataset.meaning).toBe("armed");
});

test.each(["0", "2026-02-30T00:00:00Z"])("invalid primary time %s uses the specified fallback", (invalid) => {
  const fallback = "2026-10-04T23:40:00Z";
  const job = render(<JobRow now={NOW} job={activityJob({ startedAt: fallback, lastOutputAt: invalid })} />);
  expect(timestamp(job).dateTime).toBe(fallback);
  expect(timestamp(job).dataset.meaning).toBe("started");
  job.unmount();

  const agent = render(
    <AgentRow now={NOW} sub={activityDelegate({ runStartedAt: fallback, latestActivityAt: invalid })} />,
  );
  expect(timestamp(agent).dateTime).toBe(fallback);
  expect(timestamp(agent).dataset.meaning).toBe("started");
  agent.unmount();

  const watch = buildWatchRows([activityWatch({ createdAt: fallback, deliveryTimes: [invalid] })])[0];
  if (!watch) throw new Error("watch fixture missing");
  const delivered = render(<WatchRow row={watch} now={NOW} />);
  expect(timestamp(delivered).dateTime).toBe(fallback);
});

test("an invalid settled time stays absent", () => {
  const agent = render(
    <AgentRow now={NOW} sub={activityDelegate({ terminal: true, runEndedAt: "2026-02-30T00:00:00Z" })} />,
  );
  expect(agent.container.querySelector("time")).toBeNull();
  const job = render(<JobRow now={NOW} job={activityJob({ terminal: true, endedAt: "2026-02-30T00:00:00Z" })} />);
  expect(job.container.querySelector("time")).toBeNull();
});

test("a fractional timestamp with a timezone offset retains its original wire value", () => {
  const at = "2026-10-04T23:50:00.123456789-05:30";
  const view = render(<AgentRow now={NOW} sub={activityDelegate({ latestActivityAt: at })} />);
  expect(timestamp(view).dateTime).toBe(at);
  expect(timestamp(view).dataset.meaning).toBe("activity");
});

test("omitSubagentWait suppresses subagent-only drain hold meta while preserving job wait and suppressing frozen intent", () => {
  const heldBySubagent = activityDelegate({
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 1,
    latestIntent: "FROZEN_COORD_INTENT",
  });
  const view = render(<AgentRow now={NOW} sub={heldBySubagent} omitSubagentWait />);
  expect(view.container.querySelector("[data-activity-meta]")).toBeNull();
  expect(view.container.textContent).not.toContain("FROZEN_COORD_INTENT");

  view.rerender(<AgentRow now={NOW} sub={heldBySubagent} omitSubagentWait={false} />);
  expect(view.container.querySelector("[data-activity-meta]")?.textContent).toBe("Waiting on 1 subagent");
  expect(view.container.textContent).not.toContain("FROZEN_COORD_INTENT");

  view.rerender(
    <AgentRow now={NOW} sub={{ ...heldBySubagent, drainHeldByJobs: true, runningJobCount: 2 }} omitSubagentWait />,
  );
  expect(view.container.querySelector("[data-activity-meta]")?.textContent).toBe("Waiting on 2 running jobs");
});

test("AgentRow with trailingAction splits row selection from the trailing toggle", () => {
  const onDrill = vi.fn();
  const onToggle = vi.fn();
  render(
    <AgentRow
      now={NOW}
      sub={activityDelegate({ name: "coord" })}
      onDrill={onDrill}
      trailingAction={
        <IconButton
          size="xs"
          variant="quiet"
          label="Collapse coord"
          aria-expanded={true}
          onClick={onToggle}
          icon={<Chevron direction="down" size={12} />}
        />
      }
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Collapse coord" }));
  expect(onToggle).toHaveBeenCalledOnce();
  expect(onDrill).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("coord").closest("button")!);
  expect(onDrill).toHaveBeenCalledOnce();
});
