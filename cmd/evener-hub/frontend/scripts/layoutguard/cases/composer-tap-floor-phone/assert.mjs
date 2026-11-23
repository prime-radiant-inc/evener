const TAP_MIN = 44;
const TOLERANCE = 0.5;

export default function assert(m) {
  const failures = [];
  if (!m.coarse) {
    failures.push("the case did not render with a coarse pointer, so the tap-floor arm it names is not exercised");
  }
  for (const fixture of m.fixtures) {
    const label = fixture.label;
    for (const [index, model] of fixture.models.entries()) {
      if (model.width < 1) {
        failures.push(`${label}: model trigger ${index + 1} has no visible width`);
      }
      if (model.scrollWidth > model.clientWidth + TOLERANCE) {
        failures.push(
          `${label}: model trigger ${index + 1} scrolls horizontally (${model.scrollWidth}px in ${model.clientWidth}px)`,
        );
      }
    }
    if (fixture.effort.width < TAP_MIN - TOLERANCE || fixture.effort.height < TAP_MIN - TOLERANCE) {
      failures.push(
        `${label}: the reasoning-effort target is ${fixture.effort.width.toFixed(1)}x${fixture.effort.height.toFixed(1)}, below the ${TAP_MIN}px tap floor`,
      );
    }
    if (fixture.row.scrollWidth > fixture.row.clientWidth + TOLERANCE) {
      failures.push(
        `${label}: the status row overflows horizontally (${fixture.row.scrollWidth.toFixed(1)}px content in ${fixture.row.clientWidth.toFixed(1)}px) to make room for the target`,
      );
    }
    if (fixture.effort.right > fixture.row.right + TOLERANCE || fixture.effort.left < fixture.row.left - TOLERANCE) {
      failures.push(`${label}: the effort target escapes the status row's horizontal bounds`);
    }
    // .identity is the row's overflow boundary. The tap-floor assertion above
    // reads the target's unclipped rect, which stays 44px wide even when a
    // shrunken shared .control lets .identity clip it. Require the whole rect
    // inside the boundary so the visible target is really 44px.
    if (
      fixture.effort.left < fixture.identity.left - TOLERANCE ||
      fixture.effort.right > fixture.identity.right + TOLERANCE
    ) {
      failures.push(
        `${label}: the reasoning-effort target is clipped by .identity (${fixture.clippedEffort.width.toFixed(1)} of ${fixture.effort.width.toFixed(1)}px visible)`,
      );
    }
  }
  return failures.length === 0
    ? {
        pass: true,
        reason: m.fixtures
          .map(
            (fixture) =>
              `${fixture.label}: effort ${fixture.effort.width.toFixed(1)}x${fixture.effort.height.toFixed(1)}px fits identity`,
          )
          .join("; "),
      }
    : { pass: false, reason: failures.join("; ") };
}
