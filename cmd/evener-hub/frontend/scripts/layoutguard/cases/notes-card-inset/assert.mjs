// The notes card is a floating card INSIDE the pane body's padding. Two
// regressions this pins, both real (see the case's own description):
//   1. Cancelling the pane's top padding parked the card's top edge on the
//      scrollport's clip line, where the top border and both top corners were
//      painted away - the card looked square and open-topped.
//   2. The collapsed bar had no boundary of its own (border: none, no radius),
//      so it read as a squared, edgeless strip.
export default function assert(measurement) {
  const failures = [];
  for (const [state, card] of Object.entries(measurement)) {
    // The card's top edge starts where the pane body's CONTENT starts, so it
    // stands a full pane padding clear of the scrollport's clip line. Measured
    // against the padding box, which is the same fact as insetAboveClip, and
    // reported in terms of the clip so a failure names the symptom.
    if (Math.abs(card.insetAbovePaddingBox) > 1) {
      failures.push(
        `${state}: card top sits ${card.insetAbovePaddingBox.toFixed(1)}px from the pane's padding-box top ` +
          `(expected 0), leaving only ${card.insetAboveClip.toFixed(1)}px above the clip line ` +
          `instead of the pane's ${card.scrollPaddingTop}px padding - its top border and corners are clipped`,
      );
    }
    // A real border on all four sides. The collapsed bar's only boundary is
    // this one, so a squared, edgeless strip fails here.
    const sides = [card.borderTop, card.borderRight, card.borderBottom, card.borderLeft];
    if (sides.some((width) => !(width > 0))) {
      failures.push(`${state}: card borders are ${sides.join("/")}px, expected a border on all four sides`);
    }
    // Rounded with the panel radius token. The expected value is read from the
    // page's own --radius-panel, so a token change reports "token moved" rather
    // than claiming this card stopped using it.
    const radii = [card.radiusTopLeft, card.radiusTopRight, card.radiusBottomRight, card.radiusBottomLeft];
    if (radii.some((radius) => radius !== card.expectedRadius)) {
      failures.push(
        `${state}: card radii are ${radii.join("/")}px, expected --radius-panel (${card.expectedRadius}px) on every corner`,
      );
    }
    // The whole header row is on screen, not shaved by the clip line.
    if (card.triggerVisibleHeight < card.triggerHeight - 1) {
      failures.push(
        `${state}: header shows ${card.triggerVisibleHeight.toFixed(1)}px of ${card.triggerHeight.toFixed(1)}px`,
      );
    }
  }
  // The collapsed trigger adds no edge of its own; the card already draws it.
  if (measurement.collapsed.triggerBorderBottom !== 0) {
    failures.push(
      `collapsed: summary carries a ${measurement.collapsed.triggerBorderBottom}px bottom border, doubling the card's own edge`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  const { expanded, collapsed } = measurement;
  return {
    pass: true,
    reason:
      `expanded card inset ${expanded.insetAboveClip.toFixed(1)}px below the clip, ` +
      `${expanded.borderTop}px border, ${expanded.radiusTopLeft}px radius, ` +
      `header ${expanded.triggerVisibleHeight.toFixed(1)}/${expanded.triggerHeight.toFixed(1)}px; ` +
      `collapsed bar ${collapsed.borderTop}px border, ${collapsed.radiusTopLeft}px radius, ` +
      `inset ${collapsed.insetAboveClip.toFixed(1)}px`,
  };
}
