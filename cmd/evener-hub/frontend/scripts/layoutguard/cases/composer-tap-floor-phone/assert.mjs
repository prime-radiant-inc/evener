const TAP_MIN = 44;
const TOLERANCE = 0.5;

export default function assert(m) {
  const failures = [];
  if (m.effort.width < TAP_MIN - TOLERANCE || m.effort.height < TAP_MIN - TOLERANCE) {
    failures.push(
      `the reasoning-effort target is ${m.effort.width.toFixed(1)}x${m.effort.height.toFixed(1)}, below the ${TAP_MIN}px tap floor`,
    );
  }
  if (m.row.scrollWidth > m.row.clientWidth + TOLERANCE) {
    failures.push(
      `the status row overflows horizontally (${m.row.scrollWidth.toFixed(1)}px content in ${m.row.clientWidth.toFixed(1)}px) to make room for the target`,
    );
  }
  if (m.effort.right > m.row.right + TOLERANCE || m.effort.left < m.row.left - TOLERANCE) {
    failures.push("the effort target escapes the status row's horizontal bounds");
  }
  return failures.length === 0
    ? {
        pass: true,
        reason: `the reasoning-effort target is ${m.effort.width.toFixed(1)}x${m.effort.height.toFixed(1)} at ${m.viewport.width}px, and the status row stays within ${m.row.clientWidth.toFixed(1)}px`,
      }
    : { pass: false, reason: failures.join("; ") };
}
