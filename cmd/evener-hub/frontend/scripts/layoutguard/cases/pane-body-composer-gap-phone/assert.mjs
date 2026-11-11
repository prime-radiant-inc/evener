// PaneScaffold's body carries an end-of-scroll inset so a footer-less pane's
// last row clears the pane's bottom edge. A pane that docks a footer hands
// that edge to the footer's top hairline, so the inset there is a dead band
// between the last content row and the footer - the gap this case guards.
//
// The measured fact is geometric: the distance from the last row's bottom to
// whatever surface follows the body. The inset is read only to judge the
// footer-less control, never to derive the docked expectation.
//
// Tolerance is 1px: the only legitimate band is sub-pixel rounding.
const TOLERANCE_PX = 1;

export default function assert({ viewportHeight, documentHeight, expectedInset, docked, footerless }) {
  const failures = [];
  const reasons = [];

  // A fixture clipped by the viewport would make every reading meaningless.
  if (documentHeight > viewportHeight + TOLERANCE_PX) {
    failures.push(`fixtures do not fit the ${viewportHeight}px viewport (${documentHeight}px document)`);
  }

  if (Math.abs(docked.gapToFollowing) > TOLERANCE_PX) {
    failures.push(
      `the last content row leaves a ${docked.gapToFollowing.toFixed(1)}px empty band above the docked footer (body padding-bottom ${docked.bodyPaddingBottom}px) - the body must close on the footer's hairline`,
    );
  }
  if (!docked.atBottom) {
    failures.push("the docked fixture's scroll port was not settled at its true bottom, so its gap reading is meaningless");
  }

  // The inset the pane owns only when nothing docks below it. The tokens
  // declare the clearance for this breakpoint; geometry must show it applied.
  if (footerless.gapToFollowing < expectedInset - TOLERANCE_PX) {
    failures.push(
      `a footer-less pane lost its end-of-scroll inset (${footerless.gapToFollowing.toFixed(1)}px below the last row, tokens declare ${expectedInset}px) - its last row no longer clears the pane's bottom edge`,
    );
  }
  if (!footerless.atBottom) {
    failures.push("the footer-less fixture's scroll port was not settled at its true bottom");
  }

  reasons.push(
    `docked gap ${docked.gapToFollowing.toFixed(1)}px, footer-less inset ${footerless.gapToFollowing.toFixed(1)}px (expected ${expectedInset}px)`,
  );
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
