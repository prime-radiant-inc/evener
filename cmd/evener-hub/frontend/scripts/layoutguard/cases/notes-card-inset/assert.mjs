// The notes panel is attached to the header: its top edge is flush to the
// header's bottom border (negative margin cancels the body's padding), its
// top corners are square and its top border is omitted, while the bottom
// corners carry the panel radius.
export default function assert(measurement) {
  const failures = [];
  for (const [state, card] of Object.entries(measurement)) {
    // Attached to header: top edge is at the clip line (negative margin cancels padding).
    if (Math.abs(card.insetAboveClip) > 1) {
      failures.push(
        `${state}: card top is ${card.insetAboveClip.toFixed(1)}px from clip line (expected 0 — attached to header)`,
      );
    }
    // Top is square, bottom is rounded.
    if (card.radiusTopLeft !== 0 || card.radiusTopRight !== 0) {
      failures.push(
        `${state}: top radii are ${card.radiusTopLeft}/${card.radiusTopRight}px, expected 0 (square top attached to header)`,
      );
    }
    const bottomRadii = [card.radiusBottomLeft, card.radiusBottomRight];
    if (bottomRadii.some((r) => r !== card.expectedRadius)) {
      failures.push(
        `${state}: bottom radii are ${bottomRadii.join("/")}px, expected --radius-panel (${card.expectedRadius}px)`,
      );
    }
    // No top border (header already has one), other sides have border.
    if (card.borderTop !== 0) {
      failures.push(`${state}: top border is ${card.borderTop}px, expected 0 (header provides the line)`);
    }
    const sideBorders = [card.borderRight, card.borderBottom, card.borderLeft];
    if (sideBorders.some((w) => !(w > 0))) {
      failures.push(`${state}: side/bottom borders are ${sideBorders.join("/")}px, expected >0 on each`);
    }
    if (card.triggerVisibleHeight < card.triggerHeight - 1) {
      failures.push(
        `${state}: header shows ${card.triggerVisibleHeight.toFixed(1)}px of ${card.triggerHeight.toFixed(1)}px`,
      );
    }
  }
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
      `expanded attached to header (top square, bottom ${expanded.radiusBottomLeft}px), ` +
      `collapsed top square bottom ${collapsed.radiusBottomLeft}px`,
  };
}
