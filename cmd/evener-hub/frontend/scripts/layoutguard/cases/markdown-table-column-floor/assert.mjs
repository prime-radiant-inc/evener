// The markdown table's column contract: a width-constrained GFM table renders
// every column at a readable floor (>= 5ch of the table's own font) and never
// widens past its pane. The collapse this guards against: the widget's root
// sets overflow-wrap: anywhere (inherited by every cell), which drops each
// column's min-content to ~1 character, and width: 100% + auto table layout
// distributes a width deficit proportionally to max-content - so one cell's
// long line (a test command, a URL, a JSON blob) collapses every
// short-max-content column to that ~1-character floor. Measured in the live
// hub 2026-11-19: colW [27, 70, 31, 71, 223] in a 422px column - "GH Status"
// rendered at ~2 content characters per line.
const MIN_COLUMN_CH = 5;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px column`;
    const narrowest = Math.min(...m.colWidths);
    const narrowestCh = narrowest / m.ch;
    if (narrowestCh < MIN_COLUMN_CH) {
      failures.push(
        `${label}: narrowest column is ${narrowest.toFixed(1)}px (${narrowestCh.toFixed(1)}ch) - below the ${MIN_COLUMN_CH}ch floor; a long-content column's max-content is stealing the table's width and collapsing its siblings (auto layout + inherited overflow-wrap: anywhere)`,
      );
    }
    if (m.tableWidth > m.paneWidth + 1) {
      failures.push(
        `${label}: table is ${m.tableWidth.toFixed(1)}px in a ${m.paneWidth.toFixed(1)}px pane - the fix must not widen the table past its container`,
      );
    }
    reasons.push(
      `${label}: narrowest column ${narrowest.toFixed(1)}px (${narrowestCh.toFixed(1)}ch), table ${m.tableWidth.toFixed(0)}px in a ${m.paneWidth.toFixed(0)}px pane (colW ${m.colWidths.map((w) => w.toFixed(0)).join("/")})`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
