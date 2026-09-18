// The combined shape at a PHONE viewport: one intent line carries BOTH the
// trailing Open control and the two-level body trigger. Under the phone block
// both trailing items grow to --tap-min (44px), so the continent's max-width
// reservation must subtract var(--tap-min, 14px) for the body trigger, not a
// hard-coded 14px. A hard-coded 14px under-reserves by 30px: the line sums
// past 100% and flex line-breaking wraps the body trigger to its own line -
// the failure this case pins, and which the desktop-viewport combined fixtures
// in delegate-open-widget-inline cannot see (the phone block never applies
// there).
//
// Invariants:
//   1. The body trigger shares the intent's FIRST line (sameLine).
//   2. The Open control shares that line too.
//   3. The phone-grown body trigger meets the --tap-min touch floor.

const TAP_MIN_PX = 44;
const TOLERANCE_PX = 0.5;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (!f.bodySameLine) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger's top sits ${f.bodyDropBelowLine1.toFixed(1)}px below the intent's first line - its phone-grown --tap-min width is not reserved, so it wrapped`,
      );
    }
    if (!f.openSameLine) {
      failures.push(`#${f.id} (${f.label}): the Open control wrapped off the intent's first line`);
    }
    if (f.body.width < TAP_MIN_PX - TOLERANCE_PX || f.body.height < TAP_MIN_PX - TOLERANCE_PX) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger measures ${f.body.width.toFixed(1)}x${f.body.height.toFixed(1)}px, below the ${TAP_MIN_PX}px tap floor`,
      );
    }
    if (!f.bodyHitIsBody) {
      failures.push(`#${f.id} (${f.label}): the body trigger's center is not owned by its own button hit target`);
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const drops = measurement.map((f) => f.bodyDropBelowLine1.toFixed(1)).join(", ");
  return {
    pass: true,
    reason: `the combined intent line keeps Open and the ${TAP_MIN_PX}px body trigger on line 1 in all ${measurement.length} fixtures (bodyDropBelowLine1: ${drops}; <=0 = inline), and the body trigger owns its own hit target`,
  };
}
