import { act, type RenderResult, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { activityDelegate, activityJob } from "../../stores/sessionActivityTestUtils";
import { AgentRow, JobRow } from "./activityRows";

afterEach(() => {
  vi.useRealTimers();
});

/** The rendered dot, by the state and shape the widget writes: construction,
 * not copy - a dot that broke its hue or spinner must fail, not pass. */
const dot = (view: RenderResult) => {
  const node = view.container.querySelector<HTMLElement>("[data-state]");
  if (!node) throw new Error("row renders no dot");
  return node;
};

test("a fresh running delegate shows the alive dot", () => {
  const view = render(
    <AgentRow sub={activityDelegate({ latestActivityAt: new Date(Date.now() - 5_000).toISOString() })} />,
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

test("a running delegate's dot dims at the quiet threshold", () => {
  vi.useFakeTimers();
  const now = Date.now();
  const view = render(
    <AgentRow
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
  const quiet = dot(view);
  expect(quiet.getAttribute("data-state")).toBe("quiet");
  expect(quiet.className).toContain("neutral");
  // The spinner stops once the row goes quiet.
  expect(quiet.className).not.toContain("working");
});

test("a running delegate's dot still flips when the daemon's clock runs ahead", () => {
  vi.useFakeTimers();
  const now = Date.now();
  const view = render(<AgentRow sub={activityDelegate({ latestActivityAt: new Date(now + 30_000).toISOString() })} />);
  expect(dot(view).getAttribute("data-state")).toBe("fresh");
  act(() => {
    vi.advanceTimersByTime(20_001);
  });
  // The anchor is still in the future, so the measured silence reads zero:
  // the wake re-arms instead of leaving the dot fresh forever.
  expect(dot(view).getAttribute("data-state")).toBe("fresh");
  act(() => {
    vi.advanceTimersByTime(35_000);
  });
  expect(dot(view).getAttribute("data-state")).toBe("quiet");
});

test("a delegate that crosses the threshold between its render and its effect still flips", () => {
  vi.useFakeTimers();
  const anchor = Date.now() - 20_000;
  // The render measures under the threshold; the effect measures it crossed -
  // and a quiet row gets no publish that would ever re-render it again. Only
  // the effect's own forced retell can move the dot, and with it the row is
  // quiet by the time render returns.
  const nowSpy = vi
    .spyOn(Date, "now")
    .mockReturnValueOnce(anchor + 19_999)
    .mockReturnValue(anchor + 20_001);
  const view = render(<AgentRow sub={activityDelegate({ latestActivityAt: new Date(anchor).toISOString() })} />);
  expect(dot(view).getAttribute("data-state")).toBe("quiet");
  nowSpy.mockRestore();
});

test("a delegate awaiting input keeps the attention dot", () => {
  const view = render(
    <AgentRow
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
    <AgentRow sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })} />,
  );
  const failed = dot(view);
  expect(failed.getAttribute("data-state")).toBe("failed");
  // The diamond shape, so failure never depends on hue alone.
  expect(failed.getAttribute("data-shape")).toBe("diamond");
  expect(failed.className).toContain("danger");
});

test("a settled delegate shows no dot at all", () => {
  const view = render(<AgentRow sub={activityDelegate({ terminal: true, outcome: "completed" })} />);
  expect(view.container.querySelector("[data-state]")).toBeNull();
  expect(screen.queryByRole("img")).toBeNull();
});

test("stable delegate row uses authoritative phase and lifecycle without fabricated rollup counts", () => {
  render(<AgentRow sub={activityDelegate({ lifecycle: "idle", phase: "waiting", status: "idle", terminal: false })} />);
  expect(screen.getByText("inspect")).toBeTruthy();
  expect(screen.queryByText(/agents|jobs/)).toBeNull();
});
test("ended delegate preserves the supplied failed outcome", () => {
  render(
    <AgentRow sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })} />,
  );
  expect(screen.getByText(/failed/)).toBeTruthy();
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test.each([
  { status: "completed", text: "completed" },
  { status: "command_exited_nonzero", text: "Command failed" },
  { status: "command_killed", text: "Command killed" },
  { status: "cancelled", text: "cancelled" },
  { status: "stopped", text: "stopped" },
  { status: "unknown", text: "unknown" },
  { status: "failed", reason: "runtime_timeout", text: "failed" },
  { status: "running", text: "running" },
])("terminal $status jobs retain their wording with a quiet glyph", ({ status, reason, text }) => {
  const view = render(<JobRow job={activityJob({ status, reason, terminal: true })} />);
  expect(screen.getByText(text)).toBeTruthy();
  expect(view.container.querySelector("svg")?.hasAttribute("data-tone")).toBe(false);
});

test.each(["awaiting_input", "awaiting_approval"])("live $phase delegates keep attention", (phase) => {
  render(<AgentRow sub={activityDelegate({ terminal: false, phase })} />);
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test("live shell work keeps its alive glyph", () => {
  const view = render(<JobRow job={activityJob({ terminal: false, status: "running" })} />);
  expect(view.container.querySelector("svg")?.getAttribute("data-tone")).toBe("alive");
});

test("job rows identify the work by its description while retaining the exact command", () => {
  const command = "while ! test -e release-monitor; do sleep 2; done";
  const view = render(<JobRow job={activityJob({ description: "Release monitor", command })} />);
  expect(screen.getByText("Release monitor").getAttribute("title")).toBe(command);
  view.rerender(<JobRow job={activityJob({ description: "", command })} />);
  expect(screen.getByText(command)).toBeTruthy();
  view.rerender(<JobRow job={activityJob({ description: "", command: "", jobId: "job-no-label" })} />);
  expect(screen.getByText("job-no-label")).toBeTruthy();
});

test("delegate row prefers compact name while unnamed rows retain their prompt fallback", () => {
  const { rerender } = render(
    <AgentRow
      sub={{ ...activityDelegate({ description: "Inspect the complete cache ownership" }), name: "inspect-cache" }}
    />,
  );
  expect(screen.getByText("inspect-cache")).toBeTruthy();
  expect(screen.queryByText("Inspect the complete cache ownership")).toBeNull();
  rerender(
    <AgentRow sub={{ ...activityDelegate({ description: "Inspect the complete cache ownership" }), name: " " }} />,
  );
  expect(screen.getByText("Inspect the complete cache ownership")).toBeTruthy();
});

test("a working agent row shows its latest intent in the meta line", () => {
  render(
    <AgentRow sub={activityDelegate({ status: "running", terminal: false, latestIntent: "Reading the board." })} />,
  );
  expect(screen.getByText("Reading the board.")).toBeTruthy();
  expect(screen.queryByText("running")).toBeNull();
});

test("a terminal agent row keeps its status word over a stale intent", () => {
  render(
    <AgentRow
      sub={activityDelegate({ terminal: true, outcome: "completed", status: "completed", latestIntent: "Stale line." })}
    />,
  );
  expect(screen.getByText("completed")).toBeTruthy();
  expect(screen.queryByText("Stale line.")).toBeNull();
});

test("an agent row carries its runtime status in the row's title", () => {
  render(
    <AgentRow sub={activityDelegate({ status: "running", terminal: false, latestIntent: "Reading the board." })} />,
  );
  // JobRow's pattern: the name span's title carries what the meta line replaced.
  expect(screen.getByTitle("running")).toBeTruthy();
});
