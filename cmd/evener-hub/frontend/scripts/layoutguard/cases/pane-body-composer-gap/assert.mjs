import { checkShared, TOLERANCE_PX } from "./shared-assert.mjs";

// PaneScaffold's body carries an end-of-scroll inset so a body that sits on the
// pane's own bottom edge clears it. Any in-flow slot rendered below the body -
// the padded footer, the flush edgeFooter - owns that edge instead, so the
// body closes on that surface's hairline with no dead band. The desktop footer
// never floats, so the measured surface is the following slot.
export default function assert(measurement) {
  const { failures, reasons } = checkShared({
    viewportHeight: measurement.viewportHeight,
    documentHeight: measurement.documentHeight,
    expectedInset: measurement.expectedInset,
    footerlessGap: measurement.footerless.gapToFollowing,
    footerlessAtBottom: measurement.footerless.atBottom,
    flush: measurement.flush,
  });

  for (const [key, label] of [
    ["docked", "docked footer"],
    ["edgeOnly", "flush edge footer"],
  ]) {
    const reading = measurement[key];
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

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join(", ") };
}
