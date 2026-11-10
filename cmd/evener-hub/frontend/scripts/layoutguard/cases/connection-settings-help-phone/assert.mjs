// At a phone width the settings row is one column: the label, the control,
// and the help must stack in that order. The credential-header help is always
// present, so a help cell that keeps grid-column: 2 grows an implicit second
// column beside the control instead of stacking under it.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  const aligned = (a, b) => Math.abs(a - b) <= TOLERANCE;

  if (m.row.right > m.viewportWidth + TOLERANCE) {
    failures.push(
      `the settings row runs ${(m.row.right - m.viewportWidth).toFixed(1)}px past the ${m.viewportWidth}px viewport`,
    );
  }
  if (m.rowScrollWidth > m.rowClientWidth + TOLERANCE) {
    failures.push(
      `the settings row scrolls horizontally (scrollWidth ${m.rowScrollWidth}px in clientWidth ${m.rowClientWidth}px) - the help is not in the single column`,
    );
  }
  if (!aligned(m.input.left, m.label.left)) {
    failures.push(
      `the input starts ${(m.input.left - m.label.left).toFixed(1)}px from the label's left edge instead of stacking below it`,
    );
  }
  if (m.input.top < m.label.bottom - TOLERANCE) {
    failures.push(
      `the input sits beside the label (input top ${m.input.top.toFixed(1)}px, label bottom ${m.label.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (!aligned(m.help.left, m.label.left)) {
    failures.push(
      `the help starts ${(m.help.left - m.label.left).toFixed(1)}px from the label's left edge instead of stacking in its column`,
    );
  }
  if (m.help.top < m.field.bottom - TOLERANCE) {
    failures.push(
      `the help sits beside the control (help top ${m.help.top.toFixed(1)}px, field bottom ${m.field.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (m.help.right > m.row.right + TOLERANCE) {
    failures.push(`the help runs ${(m.help.right - m.row.right).toFixed(1)}px past the row's right edge`);
  }
  if (m.helpGridColumn !== "1") {
    failures.push(`the help grid-column is ${m.helpGridColumn}, expected 1 in the one-column phone row`);
  }
  if (m.input.width <= 0) {
    failures.push("the control has no visible width");
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `label, input, and help stack in one ${m.row.width.toFixed(1)}px column (input ${m.input.width.toFixed(1)}px wide, help below it, columns ${m.rowColumns})`,
  };
}
