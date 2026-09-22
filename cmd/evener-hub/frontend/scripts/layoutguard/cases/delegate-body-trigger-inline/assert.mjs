// A summary-less two-level row's body trigger shares the intent line with the
// row's bare content line (.intentLine: rail icon, status, and the intent - no
// overlay trigger, no Open affordance; the delegate row with no trailing
// affordance). Two invariants:
//
//   1. SAME LINE. The body trigger must share the intent's FIRST line. Without
//      the .row[data-intent-trailing="true"] > .intentLine max-width
//      reservation a long intent's flex base is max-content; flex line-breaking
//      happens before shrink, so it claims the whole line and the trigger wraps.
//      The reservation bounds the WHOLE line, so the rail icon's full outer
//      width below the 700px breakpoint (where its negative-gutter pull is off)
//      counts too (roborev).
//
//   2. TEXT-EDGE ADJACENCY. In short fixtures with slack, the trigger's left
//      edge is one column-gap after the intent TEXT edge (a Range rect). A
//      growing intent absorbs the slack and springs the trigger to the line's
//      far end.
//
// Plus ownership: no overlay exists in this shape, so the trigger's center
// must be inside the trigger button itself.

const ADJACENCY_SLACK_PX = 4;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (!f.sameLine) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger's top sits ${f.dropBelowLine1.toFixed(1)}px below the intent's first line - it wrapped to its own line`,
      );
    }
    if (!f.bodyHitIsBody) {
      failures.push(`#${f.id} (${f.label}): the body trigger's center is not owned by its own button hit target`);
    }
    if (f.shortIntent) {
      const tolerance = f.columnGap + ADJACENCY_SLACK_PX;
      if (f.bodyLeftGap > tolerance) {
        failures.push(
          `#${f.id} (${f.label}): the body trigger sits ${f.bodyLeftGap.toFixed(1)}px right of the intent TEXT edge (column-gap ${f.columnGap.toFixed(1)}px + ${ADJACENCY_SLACK_PX}px tolerance) - a growing intent sprung it away`,
        );
      } else if (f.bodyLeftGap < -f.body.width) {
        failures.push(
          `#${f.id} (${f.label}): the body trigger starts ${(-f.bodyLeftGap).toFixed(1)}px left of the intent TEXT edge, more than its own ${f.body.width.toFixed(1)}px width`,
        );
      }
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const drops = measurement.map((f) => f.dropBelowLine1.toFixed(1)).join(", ");
  const gaps = measurement
    .filter((f) => f.shortIntent)
    .map((f) => f.bodyLeftGap.toFixed(1))
    .join(", ");
  return {
    pass: true,
    reason: `the bare intent and the body trigger share line 1 in all ${measurement.length} fixtures (dropBelowLine1: ${drops}; <=0 = inline), the trigger hugs the TEXT edge in short fixtures (bodyLeftGap: ${gaps}; = column-gap), and the trigger owns its own hit target`,
  };
}
