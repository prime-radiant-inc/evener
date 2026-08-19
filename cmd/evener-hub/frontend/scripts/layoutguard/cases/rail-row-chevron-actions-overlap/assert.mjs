// issue #196: the sidebar's trailing disclosure chevron (.chevronButton)
// must stay clickable even while the hover-revealed "..."/"+" actions
// overlay (.actions) covers the row's right edge. The contract this pins:
// for every (width x row-kind x right-slot) fixture the harness builds, if
// the chevron's hit box and an actions button's hit box overlap at all, a
// click landing in that overlap must resolve to the CHEVRON
// (document.elementFromPoint, not z-index arithmetic) - never to the
// actions overlay swallowing it (the original bug) and never to some third
// element neither control owns.
//
// This does NOT require the two hit boxes to never overlap - RailRow.tsx's
// own #206 fix raises the chevron's stacking order above .actions rather
// than reserving it separate flex space (see that PR's RailRow.module.css
// comment), so an overlap can exist and still be a correct fix as long as
// the chevron demonstrably wins every pixel of it. A regression that only
// repaints (e.g. a z-index in the wrong direction, or pointer-events
// swapped) still fails here because the winner comes from the browser's own
// hit test, not from reading the stylesheet.
export default function assert(measurement) {
  const failures = [];
  const notes = [];

  for (const f of measurement) {
    for (const [kind, overlap, winner] of [
      ["kebab", f.kebabOverlap, f.kebabOverlapWinner],
      ["plus", f.plusOverlap, f.plusOverlapWinner],
    ]) {
      if (!overlap) continue; // no shared pixels - nothing to adjudicate
      if (winner === "chevron") {
        notes.push(
          `${f.id}: chevron overlaps the ${kind} button by ${overlap.width.toFixed(1)}x${overlap.height.toFixed(1)}px and wins clicks there`,
        );
      } else {
        failures.push(
          `${f.id} (${f.label}): chevron=[${f.chevron.left.toFixed(1)},${f.chevron.right.toFixed(1)}] overlaps the ${kind} button's hit box by ${overlap.width.toFixed(1)}x${overlap.height.toFixed(1)}px, and a click there resolves to "${winner}", not the chevron - the disclosure triangle is still unclickable there`,
        );
      }
    }
  }

  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }

  return {
    pass: true,
    reason:
      notes.length > 0
        ? `chevron stays clickable in all ${measurement.length} fixtures (${notes.join("; ")})`
        : `chevron's hit box never overlaps an actions button in any of the ${measurement.length} fixtures`,
  };
}
