import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { activityDelegate, activityJob } from "../../stores/sessionActivityTestUtils";
import styles from "./activitybar.module.css";
import { AgentRow, JobRow } from "./activityRows";

afterEach(() => {
  vi.useRealTimers();
});

test("a fresh running delegate shows the alive dot", () => {
  render(<AgentRow sub={activityDelegate({ latestActivityAt: new Date(Date.now() - 5_000).toISOString() })} />);
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
});

test("a running delegate's dot dims at the quiet threshold", () => {
  vi.useFakeTimers();
  const now = Date.now();
  render(
    <AgentRow
      sub={activityDelegate({
        runStartedAt: new Date(now).toISOString(),
        latestActivityAt: new Date(now).toISOString(),
      })}
    />,
  );
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  act(() => {
    vi.advanceTimersByTime(20_001);
  });
  expect(screen.getByRole("img", { name: "Quiet" })).toBeTruthy();
});

test("a delegate awaiting input keeps the attention dot", () => {
  render(
    <AgentRow
      sub={activityDelegate({ terminal: false, phase: "awaiting_input", latestActivityAt: new Date().toISOString() })}
    />,
  );
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test("a failed delegate shows the danger dot", () => {
  render(
    <AgentRow sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })} />,
  );
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("a settled delegate shows no dot at all", () => {
  render(<AgentRow sub={activityDelegate({ terminal: true, outcome: "completed" })} />);
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
  expect(view.container.querySelector('[aria-hidden="true"]')?.classList.contains(styles.glyphQuiet ?? "")).toBe(true);
});

test.each(["awaiting_input", "awaiting_approval"])("live $phase delegates keep attention", (phase) => {
  render(<AgentRow sub={activityDelegate({ terminal: false, phase })} />);
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test("live shell work keeps its alive glyph", () => {
  const view = render(<JobRow job={activityJob({ terminal: false, status: "running" })} />);
  expect(view.container.querySelector('[aria-hidden="true"]')?.classList.contains(styles.glyphAlive ?? "")).toBe(true);
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
