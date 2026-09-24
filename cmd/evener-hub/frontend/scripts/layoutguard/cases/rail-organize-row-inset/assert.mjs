// The organize row's right-inset contract (Rail.module.css's own comment on
// .organizeRow): the switcher is icon-only and hard right, with the same
// right inset the pinned-section heading menus use (.sectionHeadingAction's
// padding-right), so it lines up with that family of controls and reads as
// chrome of the tier below it - never a floating control, never left.
//
// 1px tolerances, not 0, to stay clear of sub-pixel rounding noise.
export default function assert(measurement) {
  const failures = [];
  for (const [key, fixture] of Object.entries(measurement)) {
    const drift = fixture.organizeButton.right - fixture.headingButton.right;
    if (Math.abs(drift) > 1) {
      failures.push(
        `the ${key} fixture's organize button right edge (${fixture.organizeButton.right.toFixed(1)}) sits ${drift.toFixed(1)}px from the heading action's (${fixture.headingButton.right.toFixed(1)}) - .organizeRow's justify-content or right padding no longer matches .sectionHeadingAction's inset`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason:
      "the organize switcher's right edge lines up with the section heading action's in both width fixtures - hard right with the heading family's own inset",
  };
}
