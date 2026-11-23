// At a phone width the settings row is one column: the label, the control,
// and the help must stack in that order and fit the viewport. The help lives
// inside .settingsValue now (a flex column under the control), so the failure
// this guards is the row keeping its two-column grid: the control would sit
// beside the label and the help would run past the row.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  const aligned = (a, b) => Math.abs(a - b) <= TOLERANCE;
  const tracks = m.rowColumns.trim().split(/\s+/).filter(Boolean);

  if (m.row.right > m.viewportWidth + TOLERANCE) {
    failures.push(
      `the settings row runs ${(m.row.right - m.viewportWidth).toFixed(1)}px past the ${m.viewportWidth}px viewport`,
    );
  }
  if (m.rowScrollWidth > m.rowClientWidth + TOLERANCE) {
    failures.push(
      `the settings row scrolls horizontally (scrollWidth ${m.rowScrollWidth}px in clientWidth ${m.rowClientWidth}px) - the row is not one column`,
    );
  }
  if (tracks.length !== 1) {
    failures.push(`the settings row has ${tracks.length} tracks (${m.rowColumns}), expected one at a phone width`);
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
  if (m.help.top < m.input.bottom - TOLERANCE) {
    failures.push(
      `the help sits beside the control (help top ${m.help.top.toFixed(1)}px, input bottom ${m.input.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (m.help.right > m.row.right + TOLERANCE) {
    failures.push(`the help runs ${(m.help.right - m.row.right).toFixed(1)}px past the row's right edge`);
  }
  if (m.value.right > m.row.right + TOLERANCE) {
    failures.push(`the value cell runs ${(m.value.right - m.row.right).toFixed(1)}px past the row's right edge`);
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
