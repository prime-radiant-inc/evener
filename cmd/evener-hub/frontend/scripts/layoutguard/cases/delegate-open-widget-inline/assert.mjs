// The delegate / subagent card is an intent-only tool row (a stated intent,
// no separate verb/target summary) whose trailing "open" control opens the
// child transcript. ToolRow.tsx's grammar says the control rides the
// DISCLOSURE line as a sibling flex item of the trigger (data-intent-trailing
// =true), sprung to the line's end by the trigger's own flex-grow. The control
// must share the trigger's FIRST line - not drop to its own line below the
// intent.
//
// What this guards: a flex-basis:auto on the data-intent-trailing trigger
// resolves to the intent text's max-content (one long unwrapped line).
// Because .row is flex-wrap:wrap, line-breaking is decided on flex BASE SIZES
// before shrink is considered, so an intent wider than (column - control)
// claims its full width and the .intentTrailing control wraps to a second
// line - the regression. flex-basis:0 lets the trigger's content wrap WITHIN
// a zero basis (grow absorbing the rest), so the control stays on line 1.
//
// Font- and platform-independent: "same line" is the control's top edge
// falling within the intent's first line box (a Range rect), never a pixel
// snapshot.

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (!f.sameLine) {
      failures.push(
        `#${f.id} (${f.label}): the 'open' control's top sits ${f.dropBelowLine1.toFixed(1)}px below the intent's first line - it wrapped to its own line; the trailing control must ride inline on the disclosure line, not on its own`,
      );
    }
  }
  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }
  const drops = measurement.map((f) => f.dropBelowLine1.toFixed(1)).join(", ");
  return {
    pass: true,
    reason: `trigger and 'open' control share line 1 in all ${measurement.length} fixtures (dropBelowLine1: ${drops}; <=0 = inline)`,
  };
}
