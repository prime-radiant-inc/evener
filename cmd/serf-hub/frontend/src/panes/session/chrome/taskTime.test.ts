import { expect, test } from "vitest";
import { absoluteTime, relativeTime } from "./taskTime";

const NOW = new Date("2026-08-09T13:02:17-07:00");

test("under a minute reads as now", () => {
  expect(relativeTime("2026-08-09T13:02:10-07:00", NOW)).toBe("now");
});

test("minutes round to the nearest minute", () => {
  expect(relativeTime("2026-08-09T12:25:17-07:00", NOW)).toBe("37m ago");
});

test("hours up to a day", () => {
  expect(relativeTime("2026-08-09T11:02:17-07:00", NOW)).toBe("2h ago");
  expect(relativeTime("2026-08-08T22:03:48-07:00", NOW)).toBe("15h ago");
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
  expect(absoluteTime("2026-08-08T22:03:48-07:00")).toBe("Aug 8, 22:03");
});
