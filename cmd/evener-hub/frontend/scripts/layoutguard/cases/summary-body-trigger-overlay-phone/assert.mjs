// The two-level body trigger overlays the summary line (position: absolute;
// inset: 0). Its containing block is that line. The phone tap floor
// (min-height: var(--tap-min), 44px) must not let the invisible button escape
// the line's box - otherwise it captures taps and focus in the rows and gaps
// below, outside the line it is drawn on.

const TOLERANCE_PX = 0.5;

export default function assert(measurement) {
  const failures = [];
  for (const f of measurement) {
    if (f.overflowsBottom > TOLERANCE_PX || f.overflowsTop > TOLERANCE_PX) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger overlay escapes its summary line by ${f.overflowsTop.toFixed(1)}px above / ${f.overflowsBottom.toFixed(1)}px below (line ${f.line.height.toFixed(1)}px tall, trigger ${f.body.height.toFixed(1)}px) - its min-height is not contained`,
      );
    }
    if (f.escapesLeft > TOLERANCE_PX || f.escapesRight > TOLERANCE_PX) {
      failures.push(
        `#${f.id} (${f.label}): the body trigger overlay escapes its summary line horizontally (${f.escapesLeft.toFixed(1)}px left / ${f.escapesRight.toFixed(1)}px right)`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const heights = measurement.map((f) => `${f.line.height.toFixed(1)}/${f.body.height.toFixed(1)}`).join(", ");
  return {
    pass: true,
    reason: `the body trigger overlay stays inside its summary line in all ${measurement.length} fixtures (line/trigger heights: ${heights}; the tap floor is contained, not overflowed)`,
  };
}
