// When is the transcriptscrollguard harness ready to be DRIVEN? This is the
// readiness decision transcriptscrollguard-entry.tsx spins on before its first
// probe, kept here as a pure frame-by-frame tracker so both the ready verdict
// and the reason it is being withheld can be tested without a browser.
//
// Readiness is exactly two things:
//
//   the fixture rendered   - the scripted turns are all in the model and the
//     scroll container overflows its port several times over. A harness that
//     rendered nothing then fails HERE, naming the harness, instead of as a
//     geometry mystery in a downstream pill assertion
//     (docs/developing-evener/testing.md's unfalsifiable-fixture trap).
//
//   the mount stopped moving it - scrollHeight AND scrollTop unchanged for
//     SETTLE_QUIESCENT_FRAMES consecutive frames. This half is load-bearing:
//     the mount's own scrollToIndex(count-1, {align:"end"}) reconcile loop
//     stays active while post-mount measurement corrections keep moving its
//     target, and while it is active it claims ANY scroll-offset change
//     (including the guard's scroll-away) as part of its programmatic scroll
//     and yanks back - a spurious failure the runner cannot tell from a
//     product regression.
//
// DELIBERATELY NOT a readiness condition: where the mount landed. Opening a
// session scrolls to the end from the virtualizer's estimates and measures
// once (useTranscriptScroll's mount effect); nothing re-anchors that landing
// afterwards unless a scroll event or an item-count mutation arrives. A
// webfont that swaps in AFTER that landing grows the content below the fold
// without either, so the transcript sits a few pixels short of the true
// bottom with no pill - measured at 21px on a cold profile, scrollHeight
// 11466 -> 11487 at document.fonts.ready with scrollTop pinned at 10741. That
// is a real product property worth asserting, but asserting it HERE, inside a
// wait, turns it into a timeout that reports whichever condition was true all
// along. The runner asserts the mount's own visible contract (no pill at
// mount) on the sample this tracker returns; where the jump lands is what
// clickPillAndSettle measures, to 1px, which is the property this guard exists
// for.

/** The transcript scroll container's live geometry. */
export interface TranscriptGeometry {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
}

/** One frame's observation of the mounting transcript. */
export interface TranscriptSettleSample {
  /** Turns currently in the thread model. */
  turns: number;
  /** The scroll container's geometry, or null while VirtualList is unmounted. */
  geometry: TranscriptGeometry | null;
}

/** Why readiness is still being withheld, carrying the numbers that decided it. */
export type TranscriptSettleBlocker =
  | { kind: "unmounted" }
  | { kind: "turns"; turns: number; expected: number }
  | { kind: "overflow"; scrollHeight: number; clientHeight: number; required: number }
  | { kind: "moving"; scrollHeight: number; scrollTop: number }
  | { kind: "quiescing"; frames: number; required: number };

/**
 * Consecutive frames of unchanged geometry that mean the mount's reconcile
 * loop has stopped. Twenty at a 60Hz vsync is a third of a second of stillness;
 * the loop's own corrections land within a handful of frames of each other, so
 * a run this long cannot span two of them.
 */
export const SETTLE_QUIESCENT_FRAMES = 20;

/**
 * How far the transcript must overflow its port to count as rendered. The
 * scripted thread overflows ~16x (measured 11487px of content in a 725px
 * port), so this is a floor no healthy fixture approaches, not a threshold.
 *
 * scripts/transcriptscrollguard/run.mjs re-asserts the same overflow on the
 * measurement it is handed, and reads the floor from that measurement's
 * `overflowRequired` rather than keeping a second copy of this factor.
 */
export const SETTLE_OVERFLOW_FACTOR = 4;

export interface TranscriptSettleTracker {
  /** Records one frame. Returns null once the transcript is ready to drive. */
  observe(sample: TranscriptSettleSample): TranscriptSettleBlocker | null;
}

/** Human-readable form of a blocker, for the harness's tripwire message. */
export function describeTranscriptSettleBlocker(blocker: TranscriptSettleBlocker): string {
  switch (blocker.kind) {
    case "unmounted":
      return "the transcript VirtualList scroll element never mounted";
    case "turns":
      return `only ${blocker.turns} of ${blocker.expected} scripted turns reached the model`;
    case "overflow":
      return (
        `the transcript never overflowed its scroll port (scrollHeight ${blocker.scrollHeight}, ` +
        `clientHeight ${blocker.clientHeight}, needs more than ${blocker.required})`
      );
    case "moving":
      return (
        `the transcript's geometry never stopped moving (last scrollHeight ${blocker.scrollHeight}, ` +
        `scrollTop ${blocker.scrollTop})`
      );
    case "quiescing":
      return `the transcript held still for only ${blocker.frames} of ${blocker.required} consecutive frames`;
  }
}

/**
 * Tracks readiness across frames. `expectedTurns` is the scripted thread's turn
 * count: the fixture is not rendered until every one of them is in the model.
 */
export function createTranscriptSettleTracker(expectedTurns: number): TranscriptSettleTracker {
  let quiescentFrames = 0;
  // No frame has been seen yet, so the first one can never read as "unchanged".
  let lastScrollHeight = Number.NaN;
  let lastScrollTop = Number.NaN;

  return {
    observe({ turns, geometry }) {
      if (geometry === null) {
        quiescentFrames = 0;
        return { kind: "unmounted" };
      }
      if (turns !== expectedTurns) {
        quiescentFrames = 0;
        return { kind: "turns", turns, expected: expectedTurns };
      }

      const { scrollHeight, clientHeight, scrollTop } = geometry;
      const stillStanding = scrollHeight === lastScrollHeight && scrollTop === lastScrollTop;
      lastScrollHeight = scrollHeight;
      lastScrollTop = scrollTop;

      const required = clientHeight * SETTLE_OVERFLOW_FACTOR;
      if (scrollHeight <= required) {
        quiescentFrames = 0;
        return { kind: "overflow", scrollHeight, clientHeight, required };
      }
      if (!stillStanding) {
        quiescentFrames = 0;
        return { kind: "moving", scrollHeight, scrollTop };
      }

      quiescentFrames++;
      if (quiescentFrames >= SETTLE_QUIESCENT_FRAMES) return null;
      return { kind: "quiescing", frames: quiescentFrames, required: SETTLE_QUIESCENT_FRAMES };
    },
  };
}
