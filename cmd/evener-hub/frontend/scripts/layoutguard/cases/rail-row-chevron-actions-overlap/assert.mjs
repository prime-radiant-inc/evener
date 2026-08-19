// issue #196's bar, carried into the 2026-08 shared right slot: the
// sidebar's trailing disclosure chevron (.chevronButton) must stay clickable
// - at rest AND while the hover-revealed "..."/"+"/menu is showing - and the
// menu must borrow the slot occupant's (timestamp/Badge/"Not started")
// space, never add a reserved slot of its own beside it.
//
// What changed from the #196-rework version of this case: the rework made
// .actions a permanently in-flow flex item BESIDE the timestamp, so the two
// hit boxes were disjoint by width reservation alone - and every row's title
// column paid the menu's full width at rest. The shared slot puts the
// occupant and .actions in ONE grid cell (.rightSlot > * { grid-area: 1/1 })
// and swaps their visibility instead: at rest the occupant shows and the
// menu is opacity:0 + visibility:hidden; revealed (row hover, treeitem
// focus, open menu) the menu covers the occupant. The chevron lives in
// .textCol LEFT of the slot, which is still a real in-flow flex item, so
// disjointness stays structural, not a stacking outcome.
//
// The bar per fixture, in BOTH states (each config renders twice: a resting
// copy and a hover-forced copy):
//   1. The chevron and every menu button ("..." trigger, a project row's
//      "+") are fully disjoint - zero-width intersection, not merely "the
//      chevron wins the overlap" (the z-index branch's failure mode).
//   2. The chevron's own center resolves to the chevron - it is never
//      covered, hovered or not.
//   3. At rest the hidden menu's center does NOT resolve to the menu
//      (visibility:hidden removes it from hit-testing - the invisible
//      overlay eating clicks was the original #196 mechanism), and the
//      occupant's center resolves to the occupant.
//   4. Revealed, the menu's center resolves to the menu (it is really
//      clickable), and the occupant's center no longer resolves to the
//      occupant (the menu covers it - the requested behavior).
//   5. The slot is never wider than max(occupant, menu) + 1px of rounding:
//      the menu borrows the occupant's space, so no space is "left sitting
//      there" for the hover menu.
//   6. The occupant sits flush at the slot's right edge (the revealed menu
//      right-aligns over the same edge).
//   7. The menu is right-justified to that same edge and hugs its glyph:
//      the Menu widget's standalone-button padding must not come back
//      (a trigger wider than the app's md icon-button box means it did).
export default function assert(measurement) {
  const failures = [];

  for (const f of measurement) {
    const at = `${f.id} (${f.label}, ${f.state})`;

    // 1. Disjoint hit boxes, in whichever state this copy is in.
    for (const [kind, overlap] of [
      ["kebab", f.kebabOverlap],
      ["plus", f.plusOverlap],
    ]) {
      if (overlap) {
        failures.push(
          `${at}: chevron=[${f.chevron.left.toFixed(1)},${f.chevron.right.toFixed(1)}] overlaps the ${kind} button's hit box by ${overlap.width.toFixed(1)}x${overlap.height.toFixed(1)}px - the two controls must be fully disjoint in every state, not merely "chevron wins the overlap"`,
        );
      }
    }

    // 2. The chevron is always clickable at its own position.
    if (f.chevronSelfWinner !== "chevron") {
      failures.push(
        `${at}: the chevron's own center resolves to "${f.chevronSelfWinner}", not the chevron itself - it isn't reliably clickable at its own position`,
      );
    }

    // 3/4. Per-state hit-testing of the menu and the occupant.
    if (f.state === "rest") {
      if (f.kebabSelfWinner === "kebab") {
        failures.push(
          `${at}: the HIDDEN kebab trigger wins its own center at rest - it is invisible but still hit-testable (opacity alone hid it; visibility didn't), the invisible-overlay mechanism of #196`,
        );
      }
      if (f.plus && f.plusSelfWinner === "plus") {
        failures.push(
          `${at}: the HIDDEN "+" button wins its own center at rest - invisible but still hit-testable, same mechanism`,
        );
      }
      if (f.occupant && f.occupantSelfWinner !== "occupant") {
        failures.push(
          `${at}: at rest the slot occupant's own center resolves to "${f.occupantSelfWinner}", not the occupant - something is covering the timestamp/Badge it should show`,
        );
      }
    } else {
      if (f.kebabSelfWinner !== "kebab") {
        failures.push(
          `${at}: revealed, the kebab trigger's own center resolves to "${f.kebabSelfWinner}", not the kebab itself - the revealed menu isn't reliably clickable`,
        );
      }
      if (f.plus && f.plusSelfWinner !== "plus") {
        failures.push(
          `${at}: revealed, the "+" button's own center resolves to "${f.plusSelfWinner}", not the button itself - the revealed menu isn't reliably clickable`,
        );
      }
      if (f.occupant && f.occupantSelfWinner === "occupant") {
        failures.push(
          `${at}: revealed, the slot occupant still wins its own center - the menu did not cover the timestamp/Badge it shares the cell with`,
        );
      }
    }

    // 5. No reserved menu space: the shared cell is only ever as wide as the
    // wider of its two children (geometry is state-independent, but check in
    // both states so no future state-dependent width sneaks through).
    const widest = Math.max(f.occupant ? f.occupant.width : 0, f.actions.width);
    if (Math.abs(f.slot.width - widest) > 1) {
      failures.push(
        `${at}: the right slot is ${f.slot.width.toFixed(1)}px wide but its widest child is ${widest.toFixed(1)}px - the slot must be max(occupant, menu), not occupant + menu (the reserved-space regression this design removed)`,
      );
    }

    // 6. The occupant hugs the slot's right edge, so the revealed menu
    // right-aligns over exactly the pixels it vacates.
    if (f.occupant && Math.abs(f.occupant.right - f.slot.right) > 1) {
      failures.push(
        `${at}: the occupant's right edge (${f.occupant.right.toFixed(1)}) is ${(f.slot.right - f.occupant.right).toFixed(1)}px short of the slot's right edge (${f.slot.right.toFixed(1)}) - it must sit flush right, where the revealed menu appears`,
      );
    }

    // 7. The menu hugs the slot's right edge too (right-justified, the same
    // x the occupant vacates) - and hugs its GLYPH: the Menu widget's
    // standalone-button padding (--space-4 both sides) used to center the
    // "..." ~16px in from that edge and blow the trigger up to ~47px. The
    // row's own padding override keeps it glyph-sized; 32px (the app's
    // standard md icon-button box) is the bound a padded-out regression
    // crosses. Checked revealed, where the trigger is the cell's only
    // visible occupant.
    if (Math.abs(f.kebab.right - f.slot.right) > 1) {
      failures.push(
        `${at}: the kebab trigger's right edge (${f.kebab.right.toFixed(1)}) is ${(f.slot.right - f.kebab.right).toFixed(1)}px off the slot's right edge (${f.slot.right.toFixed(1)}) - the menu must be right-justified to the same edge the timestamp sits at`,
      );
    }
    if (f.kebab.width > 32) {
      failures.push(
        `${at}: the kebab trigger is ${f.kebab.width.toFixed(1)}px wide - the row's glyph-hugging padding (RailRow.module.css's .actions button[aria-haspopup="menu"]) is gone; a trigger wider than the app's standard md icon button (32px) means the standalone-button padding is back, centering the glyph away from the right edge and re-widening the shared cell`,
      );
    }
  }

  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }

  return {
    pass: true,
    reason: `chevron and menu fully disjoint and the chevron always clickable; the hidden menu eats no clicks at rest; the revealed menu covers the occupant and is clickable; the shared slot is never wider than max(occupant, menu); occupant and menu both right-justified to the slot's edge - in all ${measurement.length} state fixtures`,
  };
}
