import { afterEach, test, expect, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { requireClass } from "../internal/requireClass";
import { Cadence, type CadenceState } from "./index";
import rawStyles from "./cadence.module.css";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// requireClass asserts the class this test is about to assert on actually
// exists, rather than letting a typo'd class name compare `undefined` to
// itself (see src/widgets/internal/requireClass.ts).
const styles = {
  age0: requireClass(rawStyles.age0, "cadence.module.css", "age0"),
  age1: requireClass(rawStyles.age1, "cadence.module.css", "age1"),
  age2: requireClass(rawStyles.age2, "cadence.module.css", "age2"),
  age3: requireClass(rawStyles.age3, "cadence.module.css", "age3"),
  alive: requireClass(rawStyles.alive, "cadence.module.css", "alive"),
  attention: requireClass(rawStyles.attention, "cadence.module.css", "attention"),
  danger: requireClass(rawStyles.danger, "cadence.module.css", "danger"),
  neutral: requireClass(rawStyles.neutral, "cadence.module.css", "neutral"),
};

test("renders just the dot with no ticks when frameTimes is empty", () => {
  render(<Cadence state="idle" frameTimes={[]} now={0} />);
  expect(document.querySelectorAll("rect")).toHaveLength(0);
  expect(screen.getByTestId("cadence-dot")).toBeTruthy();
});

test("renders a tick per in-window frame, one rect per frame time", () => {
  const now = 1_700_000_000_000;
  render(<Cadence state="working" frameTimes={[now, now - 1_000, now - 2_000]} now={now} />);
  expect(document.querySelectorAll("rect")).toHaveLength(3);
});

test("age buckets: a tick's opacity class reflects how long ago it arrived", () => {
  const now = 1_700_000_000_000;
  // 60s window / 4 buckets = 15s per bucket.
  const frameTimes = [
    now - 0, // bucket 0
    now - 20_000, // bucket 1
    now - 40_000, // bucket 2
    now - 59_000, // bucket 3 (oldest, still inside the ~60s window)
  ];
  render(<Cadence state="working" frameTimes={frameTimes} now={now} />);
  const ticks = Array.from(document.querySelectorAll("rect"));
  expect(ticks).toHaveLength(4);
  const bucketClasses = [styles.age0, styles.age1, styles.age2, styles.age3];
  const foundBuckets = ticks
    .map((tick) => bucketClasses.findIndex((c) => tick.classList.contains(c)))
    .sort((a, b) => a - b);
  expect(foundBuckets).toEqual([0, 1, 2, 3]);
});

test("a tick exactly on a bucket boundary falls into the next (older) bucket", () => {
  const now = 1_700_000_000_000;
  // Half-open intervals: [0,15000) is bucket 0, so age===15000 is already
  // bucket 1, not 0 - and so on for the 30000 and 45000 edges. This is a
  // property of Math.floor(age / BUCKET_MS), not of any specific sample,
  // so it's asserted at all three internal boundaries directly rather than
  // only inferred from the interior samples above.
  const bucketClasses = [styles.age0, styles.age1, styles.age2, styles.age3];
  const frameTimes = [now - 15_000, now - 30_000, now - 45_000];
  render(<Cadence state="working" frameTimes={frameTimes} now={now} />);
  const ticks = Array.from(document.querySelectorAll("rect"));
  expect(ticks).toHaveLength(3);
  const foundBuckets = ticks
    .map((tick) => bucketClasses.findIndex((c) => tick.classList.contains(c)))
    .sort((a, b) => a - b);
  expect(foundBuckets).toEqual([1, 2, 3]);
});

test("excludes frames older than the ~60s trace window", () => {
  const now = 1_700_000_000_000;
  render(<Cadence state="working" frameTimes={[now - 61_000]} now={now} />);
  expect(document.querySelectorAll("rect")).toHaveLength(0);
});

test("excludes frames timestamped after now (clock-skew guard)", () => {
  const now = 1_700_000_000_000;
  render(<Cadence state="working" frameTimes={[now + 5_000]} now={now} />);
  expect(document.querySelectorAll("rect")).toHaveLength(0);
});

test("the now prop deterministically controls how far a frame has decayed", () => {
  const frameTime = 1_700_000_000_000;
  const { rerender } = render(<Cadence state="working" frameTimes={[frameTime]} now={frameTime} />);
  expect(document.querySelector("rect")!.classList.contains(styles.age0)).toBe(true);

  rerender(<Cadence state="working" frameTimes={[frameTime]} now={frameTime + 50_000} />);
  expect(document.querySelector("rect")!.classList.contains(styles.age3)).toBe(true);
});

// state -> token family: working=alive, needs-you=attention, failed=danger,
// idle/ended=neutral (Direction only spells out the first three plus
// "quiet" ~ idle explicitly; ended is treated the same as idle - a
// finished session has nothing left to attend to).
const STATE_FAMILIES: [CadenceState, string][] = [
  ["idle", styles.neutral],
  ["working", styles.alive],
  ["needs-you", styles.attention],
  ["failed", styles.danger],
  ["ended", styles.neutral],
];

for (const [state, familyClass] of STATE_FAMILIES) {
  test(`state ${state} maps the dot to its token family class`, () => {
    render(<Cadence state={state} frameTimes={[]} now={0} />);
    expect(screen.getByTestId("cadence-dot").classList.contains(familyClass)).toBe(true);
  });
}

test("needs-you also tints the fresh ticks with the attention family (trailing edge)", () => {
  const now = 1_700_000_000_000;
  render(<Cadence state="needs-you" frameTimes={[now]} now={now} />);
  expect(document.querySelector("rect")!.classList.contains(styles.attention)).toBe(true);
});

test("renders an SVG trace with a <=64x10 viewBox", () => {
  render(<Cadence state="working" frameTimes={[]} now={0} />);
  const svg = document.querySelector("svg")!;
  expect(svg.getAttribute("viewBox")).toBe("0 0 64 10");
});

test("provides an accessible title naming the state", () => {
  render(<Cadence state="needs-you" frameTimes={[]} now={0} />);
  expect(screen.getByTitle("Needs you")).toBeTruthy();
});

test("never re-renders on its own - no internal timers (now is fully prop-driven)", () => {
  vi.useFakeTimers();
  const now = 1_700_000_000_000;
  render(<Cadence state="working" frameTimes={[now - 1_000]} now={now} />);
  const before = document.querySelector("svg")!.outerHTML;
  vi.advanceTimersByTime(120_000); // 2 minutes of virtual time; nothing is scheduled
  const after = document.querySelector("svg")!.outerHTML;
  expect(after).toEqual(before);
});
