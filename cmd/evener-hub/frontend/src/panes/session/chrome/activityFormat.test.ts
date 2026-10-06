import { expect, test } from "vitest";
import { activitySummary } from "../../../stores/sessionActivityTestUtils";
import {
  activityActionLabel,
  formatQuietAge,
  formatUsagePair,
  isFailedStatus,
  jobStatusDisplay,
  jobStatusDotState,
  quietAnchorMillis,
} from "./activityFormat";

test("Overview labels retain unknown activity instead of inventing a zero count", () => {
  const subagents = { known: true, total: 4, active: 3, failed: 0, completed: 1 };
  expect(activityActionLabel(undefined, subagents, "Overview")).toBe("Overview");
  expect(activityActionLabel(activitySummary(), null, "Overview")).toBe("Overview");
});

// Subagents count at every depth (the subtree count, useSubagentCounts); jobs
// count for the session itself. The session's own delegate count is ignored.
test("Overview labels count subagents at every depth and the session's jobs", () => {
  const summary = activitySummary();
  summary.delegates = { known: true, total: 1, active: 1, failed: 0, completed: 0 };
  const subagents = { known: true, total: 4, active: 3, failed: 0, completed: 1 };
  expect(activityActionLabel(summary, subagents, "Overview")).toBe("Overview · 5 active");
  expect(activityActionLabel(summary, subagents)).toBe("Activity · 5 active");
  subagents.active = 0;
  summary.jobs.active = 0;
  expect(activityActionLabel(summary, subagents, "Overview")).toBe("Overview · 0 active");
  summary.jobs.known = false;
  expect(activityActionLabel(summary, subagents, "Overview")).toBe("Overview");
});

test("formatUsagePair renders arrows with compact counts", () => {
  expect(formatUsagePair({ inputTokens: 41200, outputTokens: 6100 })).toBe("↑41.2K ↓6.1K");
  expect(formatUsagePair({ inputTokens: 900, outputTokens: 12 })).toBe("↑900 ↓12");
  expect(formatUsagePair(undefined)).toBeNull();
});

test("formatQuietAge buckets seconds, minutes, hours, days", () => {
  expect(formatQuietAge(0)).toBe("0s");
  expect(formatQuietAge(3_000)).toBe("3s");
  expect(formatQuietAge(59_999)).toBe("59s");
  expect(formatQuietAge(60_000)).toBe("1m");
  expect(formatQuietAge(13 * 3_600_000)).toBe("13h");
  expect(formatQuietAge(26 * 3_600_000)).toBe("1d");
  expect(formatQuietAge(-5)).toBe("0s"); // clock skew clamps, never negative
});

test("quietAnchorMillis prefers lastOutputAt, falls back to startedAt", () => {
  expect(quietAnchorMillis({ lastOutputAt: "2026-08-05T15:02:11Z", startedAt: "2026-08-05T15:00:00Z" })).toBe(
    Date.parse("2026-08-05T15:02:11Z"),
  );
  expect(quietAnchorMillis({ startedAt: "2026-08-05T15:00:00Z" })).toBe(Date.parse("2026-08-05T15:00:00Z"));
  expect(quietAnchorMillis({ startedAt: "not a date" })).toBe(0);
});

test("jobStatusDotState maps statuses onto StatusDot states", () => {
  expect(jobStatusDotState("running")).toBe("working");
  expect(jobStatusDotState("queued")).toBe("working");
  expect(jobStatusDotState("failed")).toBe("failed");
  expect(jobStatusDotState("exhausted")).toBe("failed");
  expect(jobStatusDotState("command_exited_nonzero", true)).toBe("failed");
  expect(jobStatusDotState("command_killed", true)).toBe("failed");
  expect(jobStatusDotState("blocked")).toBe("needs-you");
  expect(jobStatusDotState("completed", true)).toBe("ended");
  expect(jobStatusDotState("stopped")).toBe("ended");
  expect(jobStatusDotState("whatever")).toBe("idle");
});

test("isFailedStatus matches the danger set", () => {
  expect(isFailedStatus("failed")).toBe(true);
  expect(isFailedStatus("exhausted")).toBe(true);
  expect(isFailedStatus("command_exited_nonzero")).toBe(true);
  expect(isFailedStatus("command_killed")).toBe(true);
  expect(isFailedStatus("completed")).toBe(false);
});

test("jobStatusDisplay keeps raw machine words except the command outcomes", () => {
  expect(jobStatusDisplay("running")).toBe("running");
  expect(jobStatusDisplay("failed")).toBe("failed");
  expect(jobStatusDisplay("exhausted")).toBe("exhausted");
  expect(jobStatusDisplay("command_exited_nonzero")).toBe("Command failed");
  expect(jobStatusDisplay("command_killed")).toBe("Command killed");
});
