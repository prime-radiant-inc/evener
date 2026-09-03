// The delegate / subagent card is an intent-only tool row (a stated intent,
// no separate verb/target summary) whose trailing "open" control opens the
// child transcript. ToolRow.tsx's grammar says the control rides the
// DISCLOSURE line as a sibling flex item of the trigger (data-intent-trailing
// =true), kept immediately beside the intent text by the trigger's
// flex:0 1 auto + max-width reservation (toolcallitem.module.css). Two
// invariants:
//
//   1. SAME LINE. The control must share the trigger's FIRST line - not
//      drop to its own line below the intent. Without the max-width
//      reservation a long intent's flex base size is its max-content (one
//      long unwrapped line): because .row is flex-wrap:wrap, line-breaking
//      is decided on hypothetical main sizes (base size clamped by
//      max-width) before shrink, so an unclamped intent wider than
//      (column - control) claims its full width and the control wraps to a
//      second line - the regression the old flex:1 1 0 rule fixed, now
//      guarded against the new mechanism.
//
//   2. ADJACENCY (short-intent fixtures, where the line has slack). The
//      control must hug the intent's end: its left edge sits exactly one
//      column-gap past the trigger's line-1 right edge. A growing trigger
//      (flex:1 ...) would absorb the slack and spring the control to the
//      line's far end, away from the item it opens - the defect the
//      flex:0 1 auto rule fixed. Only short intents can show this: a long
//      intent leaves no slack for the control to drift across.
//
// Font- and platform-independent: "same line" is the control's top edge
// falling within the intent's first line box (a Range rect), and "adjacent"
// is the measured gap against the row's own computed column-gap - never a
// pixel snapshot.

// Sub-pixel noise allowance on the adjacency tolerance: the gap should be
// exactly the column-gap, so the tolerance is (column-gap + 4px) - tight
// enough that a sprung-to-the-far-end control (hundreds of px away on every
// fixture width here) can never pass.
const ADJACENCY_SLACK_PX = 4;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (!f.sameLine) {
      failures.push(
        `#${f.id} (${f.label}): the 'open' control's top sits ${f.dropBelowLine1.toFixed(1)}px below the intent's first line - it wrapped to its own line; the trailing control must ride inline on the disclosure line, not on its own`,
      );
    }
    if (f.shortIntent) {
      const tolerance = f.columnGap + ADJACENCY_SLACK_PX;
      if (f.controlLeftGap > tolerance) {
        failures.push(
          `#${f.id} (${f.label}): the 'open' control sits ${f.controlLeftGap.toFixed(1)}px right of the intent's line-1 end (column-gap ${f.columnGap.toFixed(1)}px + ${ADJACENCY_SLACK_PX}px tolerance) - it drifted away from the text it opens (a growing trigger springs it to the line's far end); the trigger must stay flex:0 1 auto`,
        );
      } else if (f.controlLeftGap < -f.open.width) {
        failures.push(
          `#${f.id} (${f.label}): the 'open' control's left edge sits ${(-f.controlLeftGap).toFixed(1)}px LEFT of the intent's line-1 end - more than its own ${f.open.width.toFixed(1)}px width, so it no longer follows the text at all`,
        );
      }
    }
  }
  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }
  const drops = measurement.map((f) => f.dropBelowLine1.toFixed(1)).join(", ");
  const gaps = measurement
    .filter((f) => f.shortIntent)
    .map((f) => f.controlLeftGap.toFixed(1))
    .join(", ");
  return {
    pass: true,
    reason: `trigger and 'open' control share line 1 in all ${measurement.length} fixtures (dropBelowLine1: ${drops}; <=0 = inline), and the control hugs the intent's end in the short-intent fixtures (controlLeftGap: ${gaps}; = column-gap)`,
  };
}
