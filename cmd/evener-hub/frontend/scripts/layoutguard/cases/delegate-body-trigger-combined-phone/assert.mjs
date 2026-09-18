// The combined shape at a PHONE viewport: one intent line carries BOTH the
// trailing Open control and the two-level body trigger. Under the phone block
// both trailing items grow to --tap-min (44px), so the .intentTriggerContent
// max-width reservation must subtract var(--tap-min, 14px) for the body
// trigger, not a hard-coded 14px. A hard-coded 14px under-reserves by 30px:
// the line sums past 100% and flex line-breaking wraps the body trigger to its
// own line.
//
// The reservation must also cover EXACTLY the items the shape renders. A
// summary-less delegate row (data-intent-suppressed) renders no decorative
// chevron, so it must not reserve the 14px + gap for one - otherwise its intent
// is narrowed by 22px and wraps early (roborev).
//
// And the documentation comment above must be a SINGLE comment: a nested `<!--`
// closes it early and renders the example markup (a stray .bodyTrigger/.row)
// above .fixtures, which both adds unrelated content and makes the
// elementFromPoint hit test depend on prose edits (roborev).
//
// Invariants:
//   1. No stray content is rendered outside .fixtures.
//   2. Both the body trigger and Open share the intent's first line.
//   3. The content reservation equals the shape's rendered trailing items.
//   4. The phone-grown body trigger meets the --tap-min floor and owns its hit.

const TAP_MIN_PX = 44;
const TOLERANCE_PX = 0.5;
const RESERVATION_TOLERANCE_PX = 1.5;

export default function assert(measurement) {
  const failures = [];
  if (!measurement.fixturesIsFirstBodyChild || measurement.strayOutsideFixtures !== 0) {
    failures.push(
      `stray content rendered outside .fixtures (fixturesIsFirstBodyChild ${measurement.fixturesIsFirstBodyChild}, stray elements ${measurement.strayOutsideFixtures}) - the harness's documentation comment is not a single comment`,
    );
  }
  for (const f of measurement.fixtures) {
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
    if (Math.abs(f.contentWidthSlack) > RESERVATION_TOLERANCE_PX) {
      const shape = f.chevron ? "collapsed (decorative chevron present)" : "delegate (no decorative chevron)";
      failures.push(
        `#${f.id} (${f.label}): the content width ${f.content.width.toFixed(1)}px differs from the ${shape} reservation ${f.expectedContentWidth.toFixed(1)}px by ${f.contentWidthSlack.toFixed(1)}px - the reservation does not match the rendered trailing items`,
      );
    }
    // Production renders the overlay intent trigger only on the shape whose
    // intent control is NOT suppressed (the decorative-chevron collapsed row).
    // The summary-less delegate renders none - it is the body trigger alone.
    const expectedOverlay = f.chevron ? 1 : 0;
    if (f.overlayTriggerCount !== expectedOverlay) {
      const shape = f.chevron ? "collapsed" : "summary-less delegate";
      failures.push(
        `#${f.id} (${f.label}): the ${shape} fixture renders ${f.overlayTriggerCount} overlay intent trigger(s), expected ${expectedOverlay} - the fixture does not match production's suppressed shape`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  const drops = measurement.fixtures.map((f) => f.bodyDropBelowLine1.toFixed(1)).join(", ");
  const slacks = measurement.fixtures.map((f) => f.contentWidthSlack.toFixed(1)).join(", ");
  const overlays = measurement.fixtures.map((f) => f.overlayTriggerCount).join(", ");
  return {
    pass: true,
    reason: `no stray content outside .fixtures; the combined intent line keeps Open and the ${TAP_MIN_PX}px body trigger on line 1 in all ${measurement.fixtures.length} fixtures (bodyDropBelowLine1: ${drops}; <=0 = inline); each content reservation matches its rendered trailing items (slack: ${slacks}px); each shape renders production's overlay intent trigger count (${overlays}; delegate 0, collapsed 1); the body trigger owns its own hit target`,
  };
}
