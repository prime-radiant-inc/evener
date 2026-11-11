// PaneScaffold's body carries an end-of-scroll inset (--space-5 plus the phone's
// safe-area band) so a footer-less pane's last row clears the home indicator.
// When the pane DOES dock a footer - the session pane's composer row - that
// inset becomes a dead band between the transcript's last row and the footer's
// own top hairline: the reader sees empty space where the composer should
// begin. The body's bottom inset is for the pane's bottom EDGE; a footer
// claims that edge, so the body must close on the footer instead.
//
// Measured in the live hub before the fix: 24px of empty band between the
// transcript's scroll port and the footer hairline at 1500x1000. The harness
// reproduces exactly that number, which is what makes it the right guard.
//
// The one legitimate band is sub-pixel rounding at the body's bottom edge, so
// the tolerance is 1px rather than 0.
const TOLERANCE_PX = 1;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const docked = m.docked;
    if (Math.abs(docked.gapToFollowing) > TOLERANCE_PX) {
      failures.push(
        `${m.width}px: the last content row leaves a ${docked.gapToFollowing.toFixed(1)}px empty band above the docked footer (body padding-bottom ${docked.bodyPaddingBottom}px) - the body must close on the footer's hairline`,
      );
    }
    if (!docked.atBottom) {
      failures.push(
        `${m.width}px: the scroll port was not settled at its true bottom, so the gap reading is meaningless`,
      );
    }
    // The footer-less pane keeps the inset: that is the case the padding
    // exists for, and a fix that removed it outright would break it.
    if (m.footerless.bodyPaddingBottom <= 0) {
      failures.push(
        `${m.width}px: a footer-less pane lost its end-of-scroll inset (padding-bottom ${m.footerless.bodyPaddingBottom}px) - its last row no longer clears the pane's bottom edge`,
      );
    }
    reasons.push(
      `${m.width}px: docked gap ${docked.gapToFollowing.toFixed(1)}px, footer-less inset ${m.footerless.bodyPaddingBottom}px`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
