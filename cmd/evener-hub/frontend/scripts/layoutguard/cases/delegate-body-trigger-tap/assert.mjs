// On a phone viewport the two-level body trigger is sometimes the row's ONLY
// control (a summary-less delegate row). It must meet the --tap-min touch floor
// (44px), own its center hit, and still share the bare intent's first line -
// which pins that the intent's max-width reservation also reserves the grown
// --tap-min width, not just the desktop chevron.

const TAP_MIN_PX = 44;
const TOLERANCE_PX = 0.5;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (f.body.width < TAP_MIN_PX - TOLERANCE_PX || f.body.height < TAP_MIN_PX - TOLERANCE_PX) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger measures ${f.body.width.toFixed(1)}x${f.body.height.toFixed(1)}px, below the ${TAP_MIN_PX}px tap floor`,
      );
    }
    if (!f.bodyHitIsBody) {
      failures.push(`#${f.id} (${f.label}): the body trigger's center is not owned by its own button hit target`);
    }
    if (!f.sameLine) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger's top sits ${f.dropBelowLine1.toFixed(1)}px below the intent's first line - the grown trigger's width is not reserved, so it wrapped`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const sizes = measurement.map((f) => `${f.body.width.toFixed(1)}x${f.body.height.toFixed(1)}`).join(", ");
  const drops = measurement.map((f) => f.dropBelowLine1.toFixed(1)).join(", ");
  return {
    pass: true,
    reason: `the body trigger meets the ${TAP_MIN_PX}px tap floor in all ${measurement.length} fixtures (sizes: ${sizes}), owns its own hit target, and still shares the intent's first line (dropBelowLine1: ${drops}; <=0 = inline)`,
  };
}
