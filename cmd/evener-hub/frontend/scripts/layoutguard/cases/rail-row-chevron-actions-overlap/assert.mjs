// issue #196: the sidebar's trailing disclosure chevron (.chevronButton)
// must stay clickable even while the hover-revealed "..."/"+" actions
// overlay (.actions) covers the row's right edge.
//
// This case was first written against a z-index fix (raise .chevronButton
// above .actions in stacking order) and accepted "the chevron demonstrably
// wins clicks in the overlap" as green. That was too weak a bar: on the
// z-index branch, EVERY fixture showed the chevron's full 12x12 hit box
// sitting inside the "..." trigger's own hit box (roughly a quarter of its
// ~47px width) - the chevron won, but only by taking a bite out of the
// kebab trigger's own clickable area. That's the exact failure mode the
// original RCA predicted for a stacking-order fix: it doesn't remove the
// overlap, it just moves whichever control loses it.
//
// The shipped fix instead makes `.actions` a real in-flow flex item, so
// flexbox reserves it actual space and the two controls' hit boxes can
// never share a pixel - a structural guarantee, not a stacking outcome.
// The bar here now matches that: every fixture must show the chevron and
// EVERY actions button (kebab, and a project row's "+") fully disjoint
// (zero-width intersection, not just "loser resolves elsewhere"), AND each
// control's own center must resolve to itself via elementFromPoint - a
// sanity check that "disjoint boxes" actually means both controls are
// independently clickable, not that a third element is eating clicks at
// either one's center.
export default function assert(measurement) {
  const failures = [];

  for (const f of measurement) {
    for (const [kind, overlap] of [
      ["kebab", f.kebabOverlap],
      ["plus", f.plusOverlap],
    ]) {
      if (overlap) {
        failures.push(
          `${f.id} (${f.label}): chevron=[${f.chevron.left.toFixed(1)},${f.chevron.right.toFixed(1)}] overlaps the ${kind} button's hit box by ${overlap.width.toFixed(1)}x${overlap.height.toFixed(1)}px - the two controls must be fully disjoint, not merely "chevron wins the overlap"`,
        );
      }
    }

    if (f.chevronSelfWinner !== "chevron") {
      failures.push(
        `${f.id} (${f.label}): the chevron's own center resolves to "${f.chevronSelfWinner}", not the chevron itself - it isn't reliably clickable at its own position`,
      );
    }
    if (f.kebabSelfWinner !== "kebab") {
      failures.push(
        `${f.id} (${f.label}): the kebab trigger's own center resolves to "${f.kebabSelfWinner}", not the kebab itself - it isn't reliably clickable at its own position`,
      );
    }
    if (f.plus && f.plusSelfWinner !== "plus") {
      failures.push(
        `${f.id} (${f.label}): the "+" button's own center resolves to "${f.plusSelfWinner}", not the button itself - it isn't reliably clickable at its own position`,
      );
    }
  }

  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }

  return {
    pass: true,
    reason: `chevron, kebab, and (where present) the "+" button are fully disjoint and each independently clickable at its own center in all ${measurement.length} fixtures`,
  };
}
