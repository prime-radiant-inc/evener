// Expanded, the notes panel is a card attached to the header: its top edge is
// flush to the header's bottom border (negative margin cancels the body's
// padding), its top corners are square and its top border is omitted, while
// the bottom corners carry the panel radius. Collapsed, it is one quiet line
// on the reading surface (docs/web-ui/design-system.md section 12): the body's
// own top padding above it and no card chrome around it.
export default function assert(measurement) {
  const failures = [];
  const { expanded, collapsed } = measurement;

  // Attached to header: top edge is at the clip line (negative margin cancels padding).
  if (Math.abs(expanded.insetAboveClip) > 1) {
    failures.push(
      `expanded: card top is ${expanded.insetAboveClip.toFixed(1)}px from clip line (expected 0 - attached to header)`,
    );
  }
  // Top is square, bottom is rounded.
  if (expanded.radiusTopLeft !== 0 || expanded.radiusTopRight !== 0) {
    failures.push(
      `expanded: top radii are ${expanded.radiusTopLeft}/${expanded.radiusTopRight}px, expected 0 (square top attached to header)`,
    );
  }
  const bottomRadii = [expanded.radiusBottomLeft, expanded.radiusBottomRight];
  if (bottomRadii.some((r) => r !== expanded.expectedRadius)) {
    failures.push(
      `expanded: bottom radii are ${bottomRadii.join("/")}px, expected --radius-panel (${expanded.expectedRadius}px)`,
    );
  }
  // No top border (header already has one), other sides have border.
  if (expanded.borderTop !== 0) {
    failures.push(`expanded: top border is ${expanded.borderTop}px, expected 0 (header provides the line)`);
  }
  const sideBorders = [expanded.borderRight, expanded.borderBottom, expanded.borderLeft];
  if (sideBorders.some((w) => !(w > 0))) {
    failures.push(`expanded: side/bottom borders are ${sideBorders.join("/")}px, expected >0 on each`);
  }
  if (expanded.triggerVisibleHeight < expanded.triggerHeight - 1) {
    failures.push(
      `expanded: header shows ${expanded.triggerVisibleHeight.toFixed(1)}px of ${expanded.triggerHeight.toFixed(1)}px`,
    );
  }

  // Collapsed, the line keeps the body's own top spacing: it belongs to the
  // reading surface, and only the expanded card cancels that padding.
  if (Math.abs(collapsed.insetAboveClip - collapsed.scrollPaddingTop) > 1) {
    failures.push(
      `collapsed: line top is ${collapsed.insetAboveClip.toFixed(1)}px from clip line, expected the body's own ${collapsed.scrollPaddingTop}px top padding`,
    );
  }
  const collapsedBorders = [collapsed.borderTop, collapsed.borderRight, collapsed.borderBottom, collapsed.borderLeft];
  if (collapsedBorders.some((w) => w !== 0)) {
    failures.push(`collapsed: borders are ${collapsedBorders.join("/")}px, expected no card chrome (all 0)`);
  }
  const collapsedRadii = [
    collapsed.radiusTopLeft,
    collapsed.radiusTopRight,
    collapsed.radiusBottomLeft,
    collapsed.radiusBottomRight,
  ];
  if (collapsedRadii.some((r) => r !== 0)) {
    failures.push(`collapsed: radii are ${collapsedRadii.join("/")}px, expected no card chrome (all 0)`);
  }
  if (collapsed.triggerVisibleHeight < collapsed.triggerHeight - 1) {
    failures.push(
      `collapsed: line shows ${collapsed.triggerVisibleHeight.toFixed(1)}px of ${collapsed.triggerHeight.toFixed(1)}px`,
    );
  }
  if (collapsed.triggerBorderBottom !== 0) {
    failures.push(
      `collapsed: summary carries a ${collapsed.triggerBorderBottom}px bottom border, drawing an edge the quiet line must not have`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason:
      `expanded attached to header (top square, bottom ${expanded.radiusBottomLeft}px), ` +
      `collapsed quiet line keeps ${collapsed.scrollPaddingTop}px padding with no chrome`,
  };
}
