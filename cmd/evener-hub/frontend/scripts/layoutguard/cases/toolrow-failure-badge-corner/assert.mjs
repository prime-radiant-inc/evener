// A failed tool call badges its kind icon: the danger cross overlays the icon's
// bottom-right corner instead of opening a second inline column of ink beside
// it. Two things must hold, and neither is visible to jsdom (no cascade, and
// getBoundingClientRect always reports zero there):
//
//   1. the badge is POSITIONED, not in flow - a static badge becomes a flex
//      sibling and pushes the rationale right, which is the inline layout this
//      change removes;
//   2. it lands on the icon SEAT's bottom-right corner, at both the desktop
//      gutter-pulled width and the phone width where the slot leads inline.
//
// Every width renders a clean row beside the failed one as the control: the
// badge must not shift the rationale off the content edge both rows share.

const TOLERANCE_PX = 1.5;

export default function assert(measurements) {
  const failures = [];

  for (const m of measurements) {
    const { width, slot, seat, badge, badgePosition, intentLeft, cleanIntentLeft, rowLeft, cleanRowLeft } = m;

    if (badgePosition !== "absolute") {
      failures.push(
        `${width}px: the failure badge is positioned ${badgePosition}, not absolute - it is back in flow beside the icon, the inline layout this badge replaces`,
      );
    }

    // The cross sits at the icon's bottom-right corner: its bottom and right
    // edges meet the seat's, and the two stay inside the slot's box (a badge
    // that escaped the slot would collide with the rationale text).
    if (Math.abs(badge.bottom - seat.bottom) > TOLERANCE_PX) {
      failures.push(
        `${width}px: the badge's bottom edge is ${(badge.bottom - seat.bottom).toFixed(1)}px from the icon seat's bottom (${badge.bottom.toFixed(1)} vs ${seat.bottom.toFixed(1)}) - it must pin to the corner, not float`,
      );
    }
    if (badge.right > slot.right + TOLERANCE_PX || badge.left < slot.left - TOLERANCE_PX) {
      failures.push(
        `${width}px: the badge escapes its slot horizontally (badge ${badge.left.toFixed(1)}..${badge.right.toFixed(1)}, slot ${slot.left.toFixed(1)}..${slot.right.toFixed(1)}) - it would collide with the intent text`,
      );
    }
    // The corner is the SEAT's, not the slot's margin box: the seat carries a
    // margin-right inside the slot, so a badge pinned to the slot's edge would
    // hang past the glyph.
    if (Math.abs(badge.right - seat.right) > TOLERANCE_PX) {
      failures.push(
        `${width}px: the badge's right edge is ${(slot.right - badge.right).toFixed(1)}px inside the slot's edge but ${(badge.right - seat.right).toFixed(1)}px from the seat's - the cross does not sit on the icon's corner`,
      );
    }
    // The cross overlaps the 14px glyph's own horizontal span, i.e. it is ON
    // the icon rather than beside it.
    const seatGlyphLeft = seat.left + (seat.width - 14) / 2;
    const seatGlyphRight = seatGlyphLeft + 14;
    if (badge.right < seatGlyphLeft || badge.left > seatGlyphRight) {
      failures.push(
        `${width}px: the badge (${badge.left.toFixed(1)}..${badge.right.toFixed(1)}) does not overlap the 14px glyph inside its seat (${seatGlyphLeft.toFixed(1)}..${seatGlyphRight.toFixed(1)}) - it is beside the icon, not on its corner`,
      );
    }

    // The control: a failed row's rationale starts where a clean row's does.
    if (Math.abs(intentLeft - cleanIntentLeft) > TOLERANCE_PX) {
      failures.push(
        `${width}px: the failed row's rationale starts at ${intentLeft.toFixed(1)}, a clean row's at ${cleanIntentLeft.toFixed(1)} - the badge is taking layout space`,
      );
    }
    // And the slot still seats the text on the row's content edge: the pull
    // (--speaker-gutter) cancels the seat's own width plus the row's column
    // gap, so moving the pull onto the slot must not have moved the text.
    if (Math.abs(intentLeft - rowLeft) > TOLERANCE_PX) {
      failures.push(
        `${width}px: the rationale starts at ${intentLeft.toFixed(1)} but the row's content edge is ${rowLeft.toFixed(1)} - the slot no longer nets the seat's width and the gutter pull`,
      );
    }
    if (Math.abs(rowLeft - cleanRowLeft) > TOLERANCE_PX) {
      failures.push(
        `${width}px: the failed row starts at ${rowLeft.toFixed(1)}, a clean row at ${cleanRowLeft.toFixed(1)} - the badge moved the row`,
      );
    }
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  const corners = measurements
    .map((m) => `${m.width}px badge ${m.badge.left.toFixed(1)}..${m.badge.right.toFixed(1)} x ${m.badge.top.toFixed(1)}..${m.badge.bottom.toFixed(1)} / seat ${m.seat.left.toFixed(1)}..${m.seat.right.toFixed(1)} x ${m.seat.top.toFixed(1)}..${m.seat.bottom.toFixed(1)}`)
    .join(" | ");
  return {
    pass: true,
    reason: `at every width the failure badge is absolutely positioned on the icon seat's bottom-right corner, stays inside its slot, and leaves the failed row's rationale on the same content edge as a clean row's (${corners})`,
  };
}
