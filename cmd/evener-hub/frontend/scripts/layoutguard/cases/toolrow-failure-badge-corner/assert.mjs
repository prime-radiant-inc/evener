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
//
// Each fixture is its own document at its own width, and the case verifies
// that: a phone leg measured inside a 1200px document would silently assert
// the desktop cascade (the first version of this case did exactly that).

const TOLERANCE_PX = 1.5;

export default function assert(measurements) {
  const failures = [];

  for (const m of measurements) {
    const {
      mode,
      width,
      viewportWidth,
      slot,
      cross,
      iconGlyph,
      badgePosition,
      intentLeft,
      cleanIntentLeft,
      rowLeft,
      cleanRowLeft,
    } = m;

    if (Math.abs(viewportWidth - width) > TOLERANCE_PX) {
      failures.push(
        `${mode}: the fixture rendered in a ${viewportWidth}px viewport, not the declared ${width}px - its media context (the 700px gutter pull) is not the one it claims to measure`,
      );
    }

    if (badgePosition !== "absolute") {
      failures.push(
        `${mode} (${width}px): the failure badge is positioned ${badgePosition}, not absolute - it is back in flow beside the icon, the inline layout this badge replaces`,
      );
    }

    if (cross === null || iconGlyph === null) {
      failures.push(`${mode} (${width}px): the failed row did not render both the icon's svg and the badge's cross`);
      continue;
    }

    // Assert on the INK, not the wrappers. The badge box is as tall as the
    // seat (FailureGlyph's own box is 1lh), so a bottom-edge check on the box
    // passes even when the 10px cross is drawn at mid-height - which is
    // exactly what the first version of this case missed. The cross must meet
    // the icon glyph's bottom and right edges.
    if (Math.abs(cross.bottom - iconGlyph.bottom) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the cross's bottom edge is ${(cross.bottom - iconGlyph.bottom).toFixed(1)}px from the icon glyph's bottom (${cross.bottom.toFixed(1)} vs ${iconGlyph.bottom.toFixed(1)}) - it is not on the bottom of the icon`,
      );
    }
    if (Math.abs(cross.right - iconGlyph.right) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the cross's right edge is ${(cross.right - iconGlyph.right).toFixed(1)}px from the icon glyph's right (${cross.right.toFixed(1)} vs ${iconGlyph.right.toFixed(1)}) - it is not on the icon's right side`,
      );
    }
    // The cross overlaps the icon glyph in both axes, i.e. it sits ON the
    // corner rather than beside it.
    const overlapsX = cross.left < iconGlyph.right && cross.right > iconGlyph.left;
    const overlapsY = cross.top < iconGlyph.bottom && cross.bottom > iconGlyph.top;
    if (!overlapsX || !overlapsY) {
      failures.push(
        `${mode} (${width}px): the cross (${cross.left.toFixed(1)}..${cross.right.toFixed(1)} x ${cross.top.toFixed(1)}..${cross.bottom.toFixed(1)}) does not overlap the icon glyph (${iconGlyph.left.toFixed(1)}..${iconGlyph.right.toFixed(1)} x ${iconGlyph.top.toFixed(1)}..${iconGlyph.bottom.toFixed(1)}) - it is beside the icon, not on its corner`,
      );
    }
    // And it stays inside its slot, so it cannot collide with the rationale.
    if (cross.right > slot.right + TOLERANCE_PX || cross.left < slot.left - TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the cross escapes its slot horizontally (cross ${cross.left.toFixed(1)}..${cross.right.toFixed(1)}, slot ${slot.left.toFixed(1)}..${slot.right.toFixed(1)}) - it would collide with the intent text`,
      );
    }

    // The control: a failed row's rationale starts where a clean row's does.
    if (Math.abs(intentLeft - cleanIntentLeft) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the failed row's rationale starts at ${intentLeft.toFixed(1)}, a clean row's at ${cleanIntentLeft.toFixed(1)} - the badge is taking layout space`,
      );
    }
    // Above the breakpoint the slot still seats the text on the row's content
    // edge: the pull (--speaker-gutter) cancels the seat's own width plus the
    // row's column gap. Below it there is no gutter and the slot leads the
    // line inline, so the text legitimately starts one slot-plus-gap later.
    if (width >= 700 && Math.abs(intentLeft - rowLeft) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the rationale starts at ${intentLeft.toFixed(1)} but the row's content edge is ${rowLeft.toFixed(1)} - the slot no longer nets the seat's width and the gutter pull`,
      );
    }
    if (width < 700 && Math.abs(intentLeft - rowLeft - 34) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the rationale starts ${(intentLeft - rowLeft).toFixed(1)}px after the row edge, not the inline seat's one gutter (34px) - the phone leg is not leading its line inline`,
      );
    }
    if (Math.abs(rowLeft - cleanRowLeft) > TOLERANCE_PX) {
      failures.push(
        `${mode} (${width}px): the failed row starts at ${rowLeft.toFixed(1)}, a clean row at ${cleanRowLeft.toFixed(1)} - the badge moved the row`,
      );
    }
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  const corners = measurements
    .map(
      (m) =>
        `${m.mode} (${m.width}px) cross ${m.cross.left.toFixed(1)}..${m.cross.right.toFixed(1)} x ${m.cross.top.toFixed(1)}..${m.cross.bottom.toFixed(1)} / icon ${m.iconGlyph.left.toFixed(1)}..${m.iconGlyph.right.toFixed(1)} x ${m.iconGlyph.top.toFixed(1)}..${m.iconGlyph.bottom.toFixed(1)}`,
    )
    .join(" | ");
  return {
    pass: true,
    reason: `at every width the drawn cross is absolutely positioned on the icon glyph's bottom-right corner, stays inside its slot, and leaves the failed row's rationale on the same content edge as a clean row's (${corners})`,
  };
}
