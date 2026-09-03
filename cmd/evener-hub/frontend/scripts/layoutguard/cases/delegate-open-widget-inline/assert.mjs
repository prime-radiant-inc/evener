// The delegate / subagent card is an intent-only tool row (a stated intent,
// no separate verb/target summary) whose trailing "open" control opens the
// child transcript. ToolRow's visible order is intent text → Open → chevron,
// with one overlay disclosure trigger and valid sibling controls. The visible
// trigger content's flex:0 1 auto + max-width reservation keeps all three on
// line 1. Three invariants:
//
//   1. SAME LINE. The control must share the intent's FIRST line, not drop to
//      its own line. Without the max-width reservation a long intent's flex
//      base is max-content; flex line-breaking happens before shrink, so it
//      claims the full line and the trailing items wrap.
//
//   2. TEXT-EDGE ADJACENCY. In short fixtures with slack, Open's left edge is
//      one column-gap after the intent TEXT edge. A growing visible-content
//      wrapper absorbs the slack and springs Open away. The measurement never
//      unions the chevron into the text edge (the defect in the old guard).
//
//   3. ORDER. Open's right edge precedes the chevron's left edge. Open may
//      never sit on the far side of the disclosure arrow.
//
// Font- and platform-independent: line membership uses a Range over line 1,
// and adjacency uses the row's computed column-gap rather than a snapshot.

const ADJACENCY_SLACK_PX = 4;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (!f.sameLine) {
      failures.push(
        `#${f.id} (${f.label}): the 'open' control's top sits ${f.dropBelowLine1.toFixed(1)}px below the intent's first line - it wrapped to its own line`,
      );
    }
    if (!f.controlPrecedesChevron) {
      failures.push(
        `#${f.id} (${f.label}): Open does not precede the chevron (open right ${f.open.right.toFixed(1)}px, chevron left ${f.chevron.left.toFixed(1)}px); required order is intent text → Open → chevron`,
      );
    }
    if (!f.chevronHitIsTrigger) {
      failures.push(`#${f.id} (${f.label}): the chevron center is not owned by the disclosure trigger's hit target`);
    }
    if (!f.openHitIsOpen) {
      failures.push(`#${f.id} (${f.label}): the Open center is not owned by its independent button hit target`);
    }
    if (f.shortIntent) {
      const tolerance = f.columnGap + ADJACENCY_SLACK_PX;
      if (f.controlLeftGap > tolerance) {
        failures.push(
          `#${f.id} (${f.label}): Open sits ${f.controlLeftGap.toFixed(1)}px right of the intent TEXT edge (column-gap ${f.columnGap.toFixed(1)}px + ${ADJACENCY_SLACK_PX}px tolerance) - growing content sprung it away`,
        );
      } else if (f.controlLeftGap < -f.open.width) {
        failures.push(
          `#${f.id} (${f.label}): Open starts ${(-f.controlLeftGap).toFixed(1)}px left of the intent TEXT edge, more than its own ${f.open.width.toFixed(1)}px width`,
        );
      }
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const drops = measurement.map((f) => f.dropBelowLine1.toFixed(1)).join(", ");
  const gaps = measurement
    .filter((f) => f.shortIntent)
    .map((f) => f.controlLeftGap.toFixed(1))
    .join(", ");
  return {
    pass: true,
    reason: `intent text, Open, and chevron share line 1 in text → Open → chevron order in all ${measurement.length} fixtures; chevron hits the disclosure trigger while Open stays independent (dropBelowLine1: ${drops}; <=0 = inline), and Open hugs the TEXT edge in short fixtures (controlLeftGap: ${gaps}; = column-gap)`,
  };
}
