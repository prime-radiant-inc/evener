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
    if (fixture.context === null || fixture.context.width < 13 || fixture.context.height < 13) {
      failures.push(
        `${label}: the context ring is ${fixture.context === null ? "missing" : `${fixture.context.width.toFixed(1)}x${fixture.context.height.toFixed(1)}px`}, below the 14px ring size`,
      );
    } else {
      if (
        fixture.contextWrapper === null ||
        fixture.contextWrapper.width < fixture.context.width - TOLERANCE ||
        fixture.context.left < fixture.contextWrapper.left - TOLERANCE ||
        fixture.context.right > fixture.contextWrapper.right + TOLERANCE
      ) {
        failures.push(
          `${label}: the context ring is clipped by its .context wrapper (${fixture.contextWrapper === null ? "missing" : `${fixture.contextWrapper.width.toFixed(1)}px`} for ${fixture.context.width.toFixed(1)}px ring)`,
        );
      }
      if (
        fixture.context.left < fixture.identity.left - TOLERANCE ||
        fixture.context.right > fixture.identity.right + TOLERANCE ||
        fixture.context.top < fixture.identity.top - TOLERANCE ||
        fixture.context.bottom > fixture.identity.bottom + TOLERANCE
      ) {
        failures.push(`${label}: the context ring escapes .identity`);
      }
      if (fixture.models.length > 0 && fixture.context.right > fixture.models[0].left + TOLERANCE) {
        failures.push(`${label}: the context ring is not ordered before the model trigger`);
      }
    }
  }
  return failures.length === 0
    ? {
        pass: true,
        reason: m.fixtures
          .map(
            (fixture) =>
              `${fixture.label}: effort ${fixture.effort.width.toFixed(1)}x${fixture.effort.height.toFixed(1)}px, ring ${fixture.context.width.toFixed(1)}x${fixture.context.height.toFixed(1)}px (${fixture.contextWrapper.width.toFixed(1)}px slot) fit identity`,
          )
          .join("; "),
      }
    : { pass: false, reason: failures.join("; ") };
}
