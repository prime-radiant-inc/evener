// @vitest-environment node

import { expect, test } from "vitest";
import {
  createTranscriptSettleTracker,
  describeTranscriptSettleBlocker,
  SETTLE_OVERFLOW_FACTOR,
  SETTLE_QUIESCENT_FRAMES,
  type TranscriptGeometry,
  type TranscriptSettleBlocker,
  type TranscriptSettleSample,
  type TranscriptSettleTracker,
} from "./transcriptSettle";

const TURNS = 42;
const PORT = 725;

/** A rendered, overflowing transcript sitting `bottomGap` px above the true bottom. */
function rendered(bottomGap = 0): TranscriptGeometry {
  const scrollHeight = 11_487;
  return { scrollHeight, clientHeight: PORT, scrollTop: scrollHeight - PORT - bottomGap };
}

function sample(geometry: TranscriptGeometry | null, turns = TURNS): TranscriptSettleSample {
  return { turns, geometry };
}

/** Feeds one sample repeatedly, returning the last verdict. */
function hold(tracker: TranscriptSettleTracker, one: TranscriptSettleSample, frames: number) {
  let blocker: TranscriptSettleBlocker | null = { kind: "unmounted" };
  for (let i = 0; i < frames; i++) blocker = tracker.observe(one);
  return blocker;
}

// The first frame has nothing to compare against, so a run of
// SETTLE_QUIESCENT_FRAMES unchanged frames needs one frame more than that.
const FRAMES_TO_READY = SETTLE_QUIESCENT_FRAMES + 1;

// --- ready ------------------------------------------------------------------

test("a rendered transcript that holds still becomes ready to drive", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  expect(hold(tracker, sample(rendered()), FRAMES_TO_READY)).toBeNull();
});

test("a transcript parked a few pixels above the true bottom is still ready to drive", () => {
  // A webfont that swaps in after the mount's scroll-to-end grows the content
  // below the fold with no scroll event and no item-count change, so nothing
  // re-anchors the landing. Where the mount landed is the runner's assertion to
  // make; withholding readiness for it strands the guard on a condition that
  // will never come true.
  const tracker = createTranscriptSettleTracker(TURNS);
  expect(hold(tracker, sample(rendered(21)), FRAMES_TO_READY)).toBeNull();
});

test("readiness needs the stillness run to be consecutive, not merely accumulated", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  hold(tracker, sample(rendered()), SETTLE_QUIESCENT_FRAMES);
  // One frame of movement resets the run, so the next frame cannot complete it.
  tracker.observe(sample({ ...rendered(), scrollHeight: 11_500, scrollTop: 11_500 - PORT }));
  expect(tracker.observe(sample(rendered()))).not.toBeNull();
  expect(hold(tracker, sample(rendered()), SETTLE_QUIESCENT_FRAMES)).toBeNull();
});

// --- withheld, and for the right reason -------------------------------------

test("an unmounted scroll element is named as such, not as a geometry failure", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  expect(hold(tracker, sample(null), FRAMES_TO_READY)).toEqual({ kind: "unmounted" });
});

test("a half-loaded thread names the turns it is missing", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  expect(hold(tracker, sample(rendered(), 17), FRAMES_TO_READY)).toEqual({ kind: "turns", turns: 17, expected: TURNS });
});

test("a transcript that never overflows never becomes ready, and the overflow is what is named", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  const short: TranscriptGeometry = { scrollHeight: PORT + 40, clientHeight: PORT, scrollTop: 40 };
  expect(hold(tracker, sample(short), 500)).toEqual({
    kind: "overflow",
    scrollHeight: PORT + 40,
    clientHeight: PORT,
    required: PORT * SETTLE_OVERFLOW_FACTOR,
  });
});

test("geometry that keeps moving names the movement, not the overflow it already has", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  let blocker: TranscriptSettleBlocker | null = null;
  for (let i = 0; i < 500; i++) {
    const grown = 11_487 + i;
    blocker = tracker.observe(sample({ scrollHeight: grown, clientHeight: PORT, scrollTop: grown - PORT }));
  }
  expect(blocker?.kind).toBe("moving");
});

test("a stillness run still short of the requirement names how far it got", () => {
  const tracker = createTranscriptSettleTracker(TURNS);
  expect(hold(tracker, sample(rendered()), SETTLE_QUIESCENT_FRAMES)).toEqual({
    kind: "quiescing",
    frames: SETTLE_QUIESCENT_FRAMES - 1,
    required: SETTLE_QUIESCENT_FRAMES,
  });
});

// --- the blocker's own account of itself -----------------------------------

test("every blocker carries the numbers that decided it into its description", () => {
  // The wording is free to change; what a reader has to be able to recover from
  // a tripwire message is the geometry that withheld readiness, since that is
  // the whole reason this reports a blocker instead of a ceiling.
  const evidence: [TranscriptSettleBlocker, string[]][] = [
    [{ kind: "unmounted" }, []],
    [{ kind: "turns", turns: 17, expected: 42 }, ["17", "42"]],
    [{ kind: "overflow", scrollHeight: 765, clientHeight: 725, required: 2900 }, ["765", "725", "2900"]],
    [{ kind: "moving", scrollHeight: 11487, scrollTop: 10741 }, ["11487", "10741"]],
    [{ kind: "quiescing", frames: 3, required: 20 }, ["3", "20"]],
  ];
  for (const [blocker, numbers] of evidence) {
    const described = describeTranscriptSettleBlocker(blocker);
    expect(described.length).toBeGreaterThan(0);
    for (const number of numbers) expect(described).toContain(number);
  }
});
