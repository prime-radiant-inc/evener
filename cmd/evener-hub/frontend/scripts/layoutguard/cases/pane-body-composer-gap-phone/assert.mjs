// The phone session composer floats over the transcript
// (docs/web-ui/design-system.md section 12): its footer is positioned chrome
// anchored to the pane's bottom, so the padded body runs to that same bottom
// edge - content scrolls under the card instead of stopping above a boxed
// footer - and its end-of-scroll padding is gone. A footer-less pane instead
// keeps its end-of-scroll inset at the phone's own token, and a flush body
// (bodyScroll={false}) stays edge to edge.
//
// The in-flow slot shapes are the desktop case's contract: the phone session
// passes no edge footer and floats its composer, so this case reproduces only
// the shapes a phone renders.
//
// Tolerance is 1px: the only legitimate band is sub-pixel rounding.
const TOLERANCE_PX = 1;

export default function assert({ viewportHeight, documentHeight, expectedInset, floating, footerless, flush }) {
  const failures = [];
  const reasons = [];

  // A fixture clipped by the viewport would make every reading meaningless.
  if (documentHeight > viewportHeight + TOLERANCE_PX) {
    failures.push(`fixtures do not fit the ${viewportHeight}px viewport (${documentHeight}px document)`);
  }

  // Floating chrome is not a flow slot: the body runs to the pane's bottom
  // edge and closes with no padding of its own.
  if (floating.bodyToPaneBottom > TOLERANCE_PX) {
    failures.push(
      `the session body stops ${floating.bodyToPaneBottom.toFixed(1)}px above the pane's bottom edge - content must scroll under the floating composer, not above a boxed footer`,
    );
  }
  if (Math.abs(floating.gapToBodyEnd) > TOLERANCE_PX) {
    failures.push(
      `the last content row leaves a ${floating.gapToBodyEnd.toFixed(1)}px empty band at the session body's end (body padding-bottom ${floating.bodyPaddingBottom}px) - the floating composer must not add one`,
    );
  }
  if (!floating.atBottom) {
    failures.push("the session fixture's scroll port was not settled at its true bottom, so its gap is meaningless");
  }
  reasons.push(
    `session body ends ${floating.bodyToPaneBottom.toFixed(1)}px above the pane bottom, end band ${floating.gapToBodyEnd.toFixed(1)}px`,
  );

  // The inset belongs to a body that IS the pane's bottom edge, and its size is
  // the breakpoint's own token. Two-sided, so a phone case cannot pass with the
  // desktop value leaked in; and NaN-safe, so a missing token fails rather than
  // silently skipping the bound.
  if (!Number.isFinite(expectedInset)) {
    failures.push(`the breakpoint's inset token is missing from tokens.css, so the footer-less clearance is unknown`);
  } else if (Math.abs(footerless.gapToBodyEnd - expectedInset) > TOLERANCE_PX) {
    failures.push(
      `a footer-less pane's end-of-scroll clearance is ${footerless.gapToBodyEnd.toFixed(1)}px where the breakpoint's token declares ${expectedInset}px - the inset must be that value, no more and no less`,
    );
  }
  if (!footerless.atBottom) {
    failures.push("the footer-less fixture's scroll port was not settled at its true bottom");
  }
  reasons.push(`footer-less inset ${footerless.gapToBodyEnd.toFixed(1)}px (expected ${expectedInset}px)`);

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

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join(", ") };
}
