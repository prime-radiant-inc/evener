// Pure scroll-geometry helpers shared by useTranscriptScroll.ts. Kept
// side-effect-free and DOM-shape-only (never taking an HTMLElement directly
// in isAtBottom/isNearTop) so the "is the reader at the bottom" decision the
// wave's own binding constraints call out as needing an injectable
// measurement seam (jsdom performs no real layout - see VirtualList's own
// test suite doc comment) can be tested honestly, without pretending jsdom's
// always-zero scrollTop/scrollHeight/clientHeight are real geometry.

export interface ScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
}

// "At the bottom" means the reader is at the TRUE end of the scrollable
// content, not merely near it. A threshold of a few pixels tolerates the
// sub-pixel rounding browsers apply to scrollTop/scrollHeight without ever
// treating a reader who has scrolled back by even one line of text (~20px)
// as "at the bottom" - the previous 50px legacy value let autoscroll engage
// while the reader was comfortably scrolled back into history, yanking the
// transcript underneath them. One line of text is far larger than 4px, so
// this is effectively "truly at the bottom" while remaining rounding-safe.
export const AT_BOTTOM_THRESHOLD_PX = 4;

// Legacy renderer.js parity (same doc, §15): isNearTop is "scrollTop < 200".
export const NEAR_TOP_THRESHOLD_PX = 200;

/**
 * True when the reader is within `thresholdPx` of the true bottom - or the
 * content doesn't scroll at all (scrollHeight <= clientHeight), which reads
 * as "already at the bottom" rather than "can't be near a bottom that
 * doesn't exist".
 */
export function isAtBottom(metrics: ScrollMetrics, thresholdPx: number = AT_BOTTOM_THRESHOLD_PX): boolean {
  const gap = metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight;
  return gap <= thresholdPx;
}

/**
 * True when `current` shows content measured in BELOW a transcript that was
 * already at the bottom, in the SAME scroll port, with the offset never moving
 * backwards: the virtualizer correcting its own estimates, not the reader
 * leaving. The geometry half of the scroll listener's bottom-hold correction,
 * where a scroll event may be the reader's own and the change has to be shown
 * not to be theirs.
 *
 * The caller supplies the "was at bottom" and "no reader gesture" clauses,
 * which are hook state rather than geometry.
 */
export function contentGrewBelowViewport(previous: ScrollMetrics, current: ScrollMetrics): boolean {
  return (
    !isAtBottom(current) &&
    current.clientHeight === previous.clientHeight &&
    current.scrollHeight > previous.scrollHeight &&
    current.scrollTop >= previous.scrollTop
  );
}

/**
 * True when any of the content's true end sits below the fold - the exact
 * bottom, with none of isAtBottom's rounding tolerance. The no-scroll-event
 * re-anchor's condition (useTranscriptScroll's reanchorIfEndLeftView): a reader
 * following the bottom is pinned to the true end, so a shortfall of even a few
 * pixels (a pane header growing 4px over the transcript) is one to correct.
 */
export function isEndBelowFold(metrics: ScrollMetrics): boolean {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight > 0;
}

/** True when `scrollTop` is close enough to the top to trigger older-turn paging. */
export function isNearTop(scrollTop: number, thresholdPx: number = NEAR_TOP_THRESHOLD_PX): boolean {
  return scrollTop < thresholdPx;
}

/** The real-DOM default measurement - the seam useTranscriptScroll.ts's
 * `measure` param defaults to; tests inject their own instead. */
export function readScrollMetrics(el: HTMLElement): ScrollMetrics {
  return { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight };
}
