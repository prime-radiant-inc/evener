// Invariants both pane-body-composer-gap cases own, checked here once: the
// fixtures fit the viewport, a footer-less pane's end-of-scroll inset is the
// breakpoint's own token (never a leaked desktop value), and a body that
// scrolls itself (bodyScroll={false}) stays edge to edge.
//
// What follows the body is each case's own geometry: the desktop case's
// in-flow footer/edge-footer hairline, the phone case's floating composer.
// Tolerance is 1px: the only legitimate band is sub-pixel rounding.
export const TOLERANCE_PX = 1;

/**
 * @param {{ viewportHeight: number, documentHeight: number,
 *   expectedInset: number, footerlessGap: number,
 *   footerlessAtBottom: boolean, flush: object }} readings
 */
export function checkShared({ viewportHeight, documentHeight, expectedInset, footerlessGap, footerlessAtBottom, flush }) {
  const failures = [];
  const reasons = [];

  // A fixture clipped by the viewport would make every reading meaningless.
  if (documentHeight > viewportHeight + TOLERANCE_PX) {
    failures.push(`fixtures do not fit the ${viewportHeight}px viewport (${documentHeight}px document)`);
  }

  // The inset belongs to a body that IS the pane's bottom edge, and its size is
  // the breakpoint's own token. Two-sided, so a phone case cannot pass with the
  // desktop value leaked in; and NaN-safe, so a missing token fails rather than
  // silently skipping the bound.
  if (!Number.isFinite(expectedInset)) {
    failures.push(`the breakpoint's inset token is missing from tokens.css, so the footer-less clearance is unknown`);
  } else if (Math.abs(footerlessGap - expectedInset) > TOLERANCE_PX) {
    failures.push(
      `a footer-less pane's end-of-scroll clearance is ${footerlessGap.toFixed(1)}px where the breakpoint's token declares ${expectedInset}px - the inset must be that value, no more and no less`,
    );
  }
  if (!footerlessAtBottom) {
    failures.push("the footer-less fixture's scroll port was not settled at its true bottom");
  }
  reasons.push(`footer-less inset ${footerlessGap.toFixed(1)}px (expected ${expectedInset}px)`);

  // A body that scrolls itself (bodyScroll={false}) is padded by nothing: the
  // flush allocation is the whole point, and an inset here would shrink the
  // surface the pane's own columns scroll inside. Measured as geometry, since
  // the computed padding is the value under test.
  if (flush.bodyToScrollportGap > TOLERANCE_PX) {
    failures.push(
      `a flush body (bodyScroll={false}) leaves ${flush.bodyToScrollportGap.toFixed(1)}px below its scroll surface - it must render edge to edge`,
    );
  }
  if (flush.bodyOverflowY !== "clip" && flush.bodyOverflowY !== "hidden") {
    failures.push(`a flush body scrolls itself (overflow-y: ${flush.bodyOverflowY}) - it must be a plain layout box`);
  }
  reasons.push(`flush body gap ${flush.bodyToScrollportGap.toFixed(1)}px, overflow-y ${flush.bodyOverflowY}`);

  return { failures, reasons };
}
