import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { activityDelegate, activityJob } from "../../stores/sessionActivityTestUtils";
import styles from "./activitybar.module.css";
import { AgentRow, JobRow } from "./activityRows";

test("stable delegate row uses authoritative phase and lifecycle without fabricated rollup counts", () => {
  render(<AgentRow sub={activityDelegate({ lifecycle: "idle", phase: "waiting", status: "idle", terminal: false })} />);
  expect(screen.getByText("inspect")).toBeTruthy();
  expect(screen.queryByText(/agents|jobs/)).toBeNull();
});
test("ended delegate preserves the supplied failed outcome", () => {
  const view = render(
    <AgentRow sub={activityDelegate({ terminal: true, outcome: "failed", status: "failed", error: "read error" })} />,
  );
  expect(screen.getByText(/failed/)).toBeTruthy();
  expect(view.container.querySelector('[aria-hidden="true"]')?.classList.contains(styles.glyphQuiet ?? "")).toBe(true);
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
  const view = render(<AgentRow sub={activityDelegate({ terminal: false, phase })} />);
  expect(view.container.querySelector('[aria-hidden="true"]')?.classList.contains(styles.glyphAttention ?? "")).toBe(
    true,
  );
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
