// The Menu popup asks for its label's `max-content` width and lets the visible
// viewport cap it, wrapping only when the cap decided the width. jsdom resolves
// no cascade, so the vitest tests can only assert the class and the stylesheet
// text; this case is what measures a rendered popup.
//
// Every bound is two-sided or an exact value, and a missing measurement fails
// the case rather than skipping a comparison: Number.parseFloat("") is NaN and
// NaN compares false against everything, so an open inequality would pass on a
// page that never measured anything.
function finite(value, name) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`layoutguard: ${name} is not a finite number (got ${String(value)})`);
  }
  return value;
}

const TOLERANCE = 1;

export default function assert(measurement) {
  const roomy = measurement.roomy ?? {};
  const capped = measurement.capped ?? {};

  const gaugeWidth = finite(measurement.gaugeWidth, "gaugeWidth");
  const gaugeLines = finite(measurement.gaugeLines, "gaugeLines");
  const roomyWidth = finite(roomy.popupWidth, "roomy.popupWidth");
  const roomyHeight = finite(roomy.popupHeight, "roomy.popupHeight");
  const roomyLines = finite(roomy.labelLines, "roomy.labelLines");
  const roomyOverflow = finite(roomy.horizontalOverflow, "roomy.horizontalOverflow");
  const cappedWidth = finite(capped.popupWidth, "capped.popupWidth");
  const cappedHeight = finite(capped.popupHeight, "capped.popupHeight");
  const cappedLines = finite(capped.labelLines, "capped.labelLines");
  const cappedOverflow = finite(capped.horizontalOverflow, "capped.horizontalOverflow");
  const cap = finite(capped.capWidth, "capped.capWidth");
  const collapseWidth = finite(measurement.collapse?.popupWidth, "collapse.popupWidth");

  // 0. The reference popup is the label's natural width on one line. It has no
  //    cap and no positioning, so it is the baseline every other width is read
  //    against; if it wrapped, the baseline itself is wrong.
  if (gaugeLines !== 1) {
    return {
      pass: false,
      reason: `the uncapped reference popup wrapped its label onto ${gaugeLines} line(s), so it is not a usable natural-width baseline`,
    };
  }

  // 1. A popup whose own offset leaves less room than its label still asks for
  //    the label's natural width. This is the box `width: max-content` exists
  //    for: a shrink-to-fit popup here collapses to the 182px the offset leaves
  //    it, which is the bug that clipped the long session-activity label.
  const roomyGap = Math.abs(roomyWidth - gaugeWidth);
  if (roomyGap > TOLERANCE) {
    return {
      pass: false,
      reason: `popup measured ${roomyWidth.toFixed(1)}px where its label's natural width is ${gaugeWidth.toFixed(1)}px (${roomyGap.toFixed(1)}px off) - the popup is sized to the space its offset leaves, not to its content (menu.module.css .popup is missing width: max-content)`,
    };
  }

  // 1b. The same tight offset with rows that CAN wrap. A wrapped row's
  //     min-content is one word, so a shrink-to-fit box collapses to the space
  //     its offset leaves it; `max-content` asks for the label's single-line
  //     width anyway and leaves the cap to decide. This is the only geometry
  //     where the two differ, because a nowrap row makes min-content equal
  //     max-content.
  if (Math.abs(collapseWidth - gaugeWidth) > TOLERANCE) {
    return {
      pass: false,
      reason: `a wrapping popup with the same tight offset measured ${collapseWidth.toFixed(1)}px against the label's ${gaugeWidth.toFixed(1)}px natural width - it shrink-to-fit the room its offset left instead of asking for its content (menu.module.css .popup is missing width: max-content)`,
    };
  }

  // 2. With that room, the row does not wrap and does not scroll sideways.
  if (roomyLines !== 1) {
    return {
      pass: false,
      reason: `label occupies ${roomyLines} line(s) in the popup with room for its natural width, expected exactly 1 - the row wrapped where it had no need to`,
    };
  }
  if (Math.abs(roomyOverflow) > TOLERANCE) {
    return {
      pass: false,
      reason: `label overflows the popup horizontally by ${roomyOverflow.toFixed(1)}px with room to spare - the row is being clipped rather than sized`,
    };
  }

  // 3. Under a cap narrower than the label, the popup sits AT the cap: both
  //    edges, because a popup narrower than the cap collapsed instead of
  //    filling the room it was given, and one wider pushed past the visible
  //    edge it was capped to.
  if (Math.abs(cappedWidth - cap) > TOLERANCE) {
    return {
      pass: false,
      reason: `capped popup measures ${cappedWidth.toFixed(1)}px against a ${cap.toFixed(1)}px cap - it must fill the cap it was given and go no further (the 180px min-width floor has to yield to the cap)`,
    };
  }

  // 4. The capped popup wraps instead of clipping: the label takes at least two
  //    lines and nothing runs past the popup's own edge.
  if (cappedLines < 2) {
    return {
      pass: false,
      reason: `label occupies ${cappedLines} line(s) under a ${cap.toFixed(1)}px cap narrower than itself, expected at least 2 - the label did not wrap (.popupNarrow .item must be white-space: normal)`,
    };
  }
  if (Math.abs(cappedOverflow) > TOLERANCE) {
    return {
      pass: false,
      reason: `label overflows the capped popup horizontally by ${cappedOverflow.toFixed(1)}px - the label is clipped instead of wrapped`,
    };
  }

  // 5. Wrapping is visible: the same row takes more vertical space, so the
  //    popup with one item is taller than its single-line form. Without this a
  //    silent width change could pass as wrapping.
  if (cappedHeight <= roomyHeight + TOLERANCE) {
    return {
      pass: false,
      reason: `capped popup is ${cappedHeight.toFixed(1)}px tall, not taller than the ${roomyHeight.toFixed(1)}px single-line form - the wrapped row did not gain height`,
    };
  }

  return {
    pass: true,
    reason: `popup asks its label's ${gaugeWidth.toFixed(1)}px natural width (measured ${roomyWidth.toFixed(1)}px) and stays at its ${cap.toFixed(1)}px cap with ${cappedLines} lines of wrapped label`,
  };
}
