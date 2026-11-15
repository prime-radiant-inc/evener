// PaneScaffold's body carries an end-of-scroll inset so a body that sits on
// the pane's own bottom edge clears it (the phone's home-indicator band in
// particular). Any slot rendered below the body - the padded footer, the
// flush edgeFooter - owns that edge instead, so the inset there is a dead band
// between the last content row and the surface below it.
//
// The measured fact is geometric: the distance from the last row's bottom to
// whatever surface follows the body. The inset is read only to judge the
// footer-less control, never to derive a docked expectation. Shared by the
// desktop and phone cases, which differ only in the viewport and the tokens
// they declare; the fixtures differ only in which slots they render.
//
// Tolerance is 1px: the only legitimate band is sub-pixel rounding.
const TOLERANCE_PX = 1;

export default function assert({ viewportHeight, documentHeight, expectedInset, docked, edgeOnly, footerless, flush }) {
  const failures = [];
  const reasons = [];

  // A fixture clipped by the viewport would make every reading meaningless.
  if (documentHeight > viewportHeight + TOLERANCE_PX) {
    failures.push(`fixtures do not fit the ${viewportHeight}px viewport (${documentHeight}px document)`);
  }

  // A body with anything below it closes on that surface instead of padding.
  for (const [key, label, reading] of [
    ["docked", "docked footer", docked],
    ["edgeOnly", "flush edge footer", edgeOnly],
  ]) {
    if (Math.abs(reading.gapToFollowing) > TOLERANCE_PX) {
      failures.push(
        `the last content row leaves a ${reading.gapToFollowing.toFixed(1)}px empty band above the ${label} (body padding-bottom ${reading.bodyPaddingBottom}px) - the body must close on that surface's hairline`,
      );
    }
    if (!reading.atBottom) {
      failures.push(`the ${key} fixture's scroll port was not settled at its true bottom, so its gap is meaningless`);
    }
    reasons.push(`${label} gap ${reading.gapToFollowing.toFixed(1)}px`);
  }

  // The inset belongs to a body that IS the pane's bottom edge, and its size is
  // the breakpoint's own token. Two-sided, so a phone case cannot pass with the
  // desktop value leaked in; and NaN-safe, so a missing token fails rather than
  // silently skipping the bound.
  if (!Number.isFinite(expectedInset)) {
    failures.push(`the breakpoint's inset token is missing from tokens.css, so the footer-less clearance is unknown`);
  } else if (Math.abs(footerless.gapToFollowing - expectedInset) > TOLERANCE_PX) {
    failures.push(
      `a footer-less pane's end-of-scroll clearance is ${footerless.gapToFollowing.toFixed(1)}px where the breakpoint's token declares ${expectedInset}px - the inset must be that value, no more and no less`,
    );
  }
  if (!footerless.atBottom) {
    failures.push("the footer-less fixture's scroll port was not settled at its true bottom");
  }
  reasons.push(`footer-less inset ${footerless.gapToFollowing.toFixed(1)}px (expected ${expectedInset}px)`);

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
