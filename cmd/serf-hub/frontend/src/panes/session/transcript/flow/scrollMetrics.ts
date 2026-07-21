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

// Legacy renderer.js parity (docs/web-ui/parity/parity-m4-transcript.md
// §15): isNearBottom is "within 50px of true bottom".
export const AT_BOTTOM_THRESHOLD_PX = 50;

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

/** True when `scrollTop` is close enough to the top to trigger older-turn paging. */
export function isNearTop(scrollTop: number, thresholdPx: number = NEAR_TOP_THRESHOLD_PX): boolean {
  return scrollTop < thresholdPx;
}

/** The real-DOM default measurement - the seam useTranscriptScroll.ts's
 * `measure` param defaults to; tests inject their own instead. */
export function readScrollMetrics(el: HTMLElement): ScrollMetrics {
  return { scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight };
}
