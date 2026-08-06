import { expect, test } from "vitest";
import {
  formatQuietAge,
  formatUsagePair,
  isFailedStatus,
  jobStatusDotState,
  quietAnchorMillis,
} from "./activityFormat";

test("formatUsagePair renders arrows with compact counts", () => {
  expect(formatUsagePair({ inputTokens: 41200, outputTokens: 6100 })).toBe("↑41k ↓6k");
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
  expect(jobStatusDotState("blocked")).toBe("needs-you");
  expect(jobStatusDotState("completed", true)).toBe("ended");
  expect(jobStatusDotState("stopped")).toBe("ended");
  expect(jobStatusDotState("whatever")).toBe("idle");
});

test("isFailedStatus matches the danger set", () => {
  expect(isFailedStatus("failed")).toBe(true);
  expect(isFailedStatus("exhausted")).toBe(true);
  expect(isFailedStatus("completed")).toBe(false);
});
