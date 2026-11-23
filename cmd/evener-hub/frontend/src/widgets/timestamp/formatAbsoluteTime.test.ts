// @vitest-environment node

import { expect, test } from "vitest";
import { formatAbsoluteTime } from "./formatAbsoluteTime";

// Expected values come from independently constructed Intl formatters over
// local wall-clock instants: the assertions are timezone-independent and pin
// no copy. Local Date constructors make "the same calendar day" mean the same
// thing in every timezone.

const TIME_WITH_SECONDS = new Intl.DateTimeFormat("en", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const DATE_TIME_WITH_SECONDS = new Intl.DateTimeFormat("en", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const DATE_TIME_WITHOUT_SECONDS = new Intl.DateTimeFormat("en", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

const INSTANT = new Date(2026, 7, 9, 13, 2, 10).getTime();
const LATER_THE_SAME_DAY = new Date(2026, 7, 9, 23, 5, 0).getTime();
const THE_DAY_BEFORE = new Date(2026, 7, 8, 22, 3, 48).getTime();

test("adaptive stays time-only when the instant shares the reference day", () => {
  expect(formatAbsoluteTime(INSTANT, { now: LATER_THE_SAME_DAY })).toBe(TIME_WITH_SECONDS.format(INSTANT));
});

test("adaptive carries the date when the reference is a different day", () => {
  expect(formatAbsoluteTime(INSTANT, { now: THE_DAY_BEFORE })).toBe(DATE_TIME_WITH_SECONDS.format(INSTANT));
});

test("adaptive follows the calendar boundary, not elapsed time", () => {
  const lateNight = new Date(2026, 7, 9, 23, 59, 30).getTime();
  const justAfterMidnight = new Date(2026, 7, 10, 0, 1, 0).getTime();
  expect(formatAbsoluteTime(lateNight, { now: justAfterMidnight })).toBe(DATE_TIME_WITH_SECONDS.format(lateNight));

  const earlyMorning = new Date(2026, 7, 9, 0, 1, 0).getTime();
  const lateThatNight = new Date(2026, 7, 9, 23, 59, 30).getTime();
  expect(formatAbsoluteTime(earlyMorning, { now: lateThatNight })).toBe(TIME_WITH_SECONDS.format(earlyMorning));
});

test("dateTime keeps the date and minute precision", () => {
  expect(formatAbsoluteTime(INSTANT, { format: "dateTime" })).toBe(DATE_TIME_WITHOUT_SECONDS.format(INSTANT));
});

test("locale uses the reader's locale for the date and time", () => {
  expect(formatAbsoluteTime(INSTANT, { format: "locale" })).toBe(new Date(INSTANT).toLocaleString());
});

test("localeTime uses the reader's locale for the clock time alone", () => {
  expect(formatAbsoluteTime(INSTANT, { format: "localeTime" })).toBe(new Date(INSTANT).toLocaleTimeString());
});
