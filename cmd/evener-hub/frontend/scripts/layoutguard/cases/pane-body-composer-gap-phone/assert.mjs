import { checkShared, TOLERANCE_PX } from "../pane-body-composer-gap/shared-assert.mjs";

// The phone session composer floats over the transcript (design-system.md
// section 12): its footer is positioned chrome anchored to the pane's bottom,
// so the padded body runs to that same bottom edge - content scrolls under the
// card instead of stopping above a boxed footer - and its end-of-scroll
// padding is gone. The in-flow slot shapes are the desktop case's contract;
// the phone session passes no edge footer and floats its composer.
export default function assert(measurement) {
  const { floating } = measurement;
  const { failures, reasons } = checkShared({
    viewportHeight: measurement.viewportHeight,
    documentHeight: measurement.documentHeight,
    expectedInset: measurement.expectedInset,
    footerlessGap: measurement.footerless.gapToBodyEnd,
    footerlessAtBottom: measurement.footerless.atBottom,
    flush: measurement.flush,
  });

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

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join(", ") };
}
