// @vitest-environment node

import { expect, test } from "vitest";
import { absoluteTime, relativeTime } from "./taskListTime";

const NOW = new Date("2026-08-09T13:02:17-07:00");

test("under a minute reads as now", () => {
  expect(relativeTime("2026-08-09T13:02:10-07:00", NOW)).toBe("now");
});

test("minutes, hours and days floor to the largest whole unit", () => {
  expect(relativeTime("2026-08-09T12:25:17-07:00", NOW)).toBe("37m ago");
  expect(relativeTime("2026-08-09T13:00:18-07:00", NOW)).toBe("1m ago");
  expect(relativeTime("2026-08-09T12:00:18-07:00", NOW)).toBe("1h ago");
  expect(relativeTime("2026-08-08T13:02:17-07:00", NOW)).toBe("1d ago");
  expect(relativeTime("2026-08-08T13:03:17-07:00", NOW)).toBe("23h ago");
});

test("hours up to a day", () => {
  expect(relativeTime("2026-08-09T11:02:17-07:00", NOW)).toBe("2h ago");
  expect(relativeTime("2026-08-08T22:03:48-07:00", NOW)).toBe("14h ago");
});

test("days past 24 hours", () => {
  expect(relativeTime("2026-08-07T13:02:17-07:00", NOW)).toBe("2d ago");
});

test("a future timestamp clamps to now", () => {
  expect(relativeTime("2026-08-09T14:00:00-07:00", NOW)).toBe("now");
});

test("invalid input falls back to the raw string", () => {
  expect(relativeTime("not-a-date", NOW)).toBe("not-a-date");
  expect(absoluteTime("not-a-date")).toBe("not-a-date");
});

test("absolute renders month-day and 24-hour time", () => {
  const localTimestamp = new Date(2026, 7, 8, 22, 3, 48).toISOString();
  expect(absoluteTime(localTimestamp)).toBe("Aug 8, 22:03");
});
