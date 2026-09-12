import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Timestamp } from "./index";

afterEach(() => cleanup());

// Expected absolutes are computed through the SAME Intl formatters the
// widget uses, on the SAME epoch values — so the suite is timezone-
// independent by construction (the widget pins "en"; the test mirrors it).

const NOW = 1_700_000_000_000;
const SAME_DAY_OPTS = { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false } as const;
const OTHER_DAY_OPTS = {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
} as const;

function absSameDay(value: number): string {
  return new Intl.DateTimeFormat("en", SAME_DAY_OPTS).format(value);
}
function absOtherDay(value: number): string {
  return new Intl.DateTimeFormat("en", OTHER_DAY_OPTS).format(value);
}

test("renders a <time> element with a dateTime ISO attribute", () => {
  render(<Timestamp value={NOW} now={NOW} />);
  const t = screen.getByText("now");
  expect(t.tagName).toBe("TIME");
  expect(t.getAttribute("dateTime")).toBe(new Date(NOW).toISOString());
});

test("'now' for an instant within 10s of the reference", () => {
  render(<Timestamp value={NOW - 9_000} now={NOW} />);
  expect(screen.getByText("now")).toBeTruthy();
});

test("future / clock-skew values collapse to 'now', not 'in N seconds'", () => {
  render(<Timestamp value={NOW + 4_000} now={NOW} />);
  expect(screen.getByText("now")).toBeTruthy();
});

test("seconds: 30s ago", () => {
  render(<Timestamp value={NOW - 30_000} now={NOW} />);
  expect(screen.getByText("30s ago")).toBeTruthy();
});

test("minutes round up: 90s reads '2m ago', not '90s ago'", () => {
  render(<Timestamp value={NOW - 90_000} now={NOW} />);
  expect(screen.getByText("2m ago")).toBeTruthy();
});

test("hours: 3h ago", () => {
  render(<Timestamp value={NOW - 3 * 3_600_000} now={NOW} />);
  expect(screen.getByText("3h ago")).toBeTruthy();
});

test("days: 2d ago", () => {
  render(<Timestamp value={NOW - 2 * 86_400_000} now={NOW} />);
  expect(screen.getByText("2d ago")).toBeTruthy();
});

test("weeks: 14d ago reads '2w ago' at the week threshold", () => {
  render(<Timestamp value={NOW - 14 * 86_400_000} now={NOW} />);
  expect(screen.getByText("2w ago")).toBeTruthy();
});

test("same-day absolute is time-only and on the title (hover), not the visible text", () => {
  const value = NOW - 30_000;
  render(<Timestamp value={value} now={NOW} />);
  const t = screen.getByText("30s ago");
  expect(t.getAttribute("title")).toBe(absSameDay(value));
  expect(t.textContent).toBe("30s ago");
});

test("other-day absolute carries the date (adaptive format) on the title", () => {
  const value = NOW - 2 * 86_400_000; // two days earlier → different calendar day
  render(<Timestamp value={value} now={NOW} />);
  const t = screen.getByText("2d ago");
  expect(t.getAttribute("title")).toBe(absOtherDay(value));
  // The other-day format begins with a month abbreviation, proving it is not
  // the time-only same-day shape.
  expect(t.getAttribute("title")).toMatch(/^[A-Z][a-z]{2} \d/);
});

test("a NaN value renders nothing", () => {
  const { container } = render(<Timestamp value={Number.NaN} now={NOW} />);
  expect(container.querySelector("time")).toBeNull();
});

test("a NaN now renders nothing", () => {
  const { container } = render(<Timestamp value={NOW} now={Number.NaN} />);
  expect(container.querySelector("time")).toBeNull();
});

test("never re-renders on its own - no internal timers (now is fully prop-driven)", () => {
  vi.useFakeTimers();
  render(<Timestamp value={NOW - 30_000} now={NOW} />);
  const before = screen.getByText("30s ago").textContent;
  vi.advanceTimersByTime(120_000); // 2 minutes of virtual time; nothing scheduled
  const after = screen.getByText("30s ago").textContent;
  expect(after).toEqual(before);
  vi.useRealTimers();
});
