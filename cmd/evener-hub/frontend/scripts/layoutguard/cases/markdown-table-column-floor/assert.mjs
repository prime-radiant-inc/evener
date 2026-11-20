// The markdown table's column contract: a width-constrained GFM table renders
// every column at a readable floor (>= 5ch of the table's own font) and never
// widens past its pane, and a cell's enhanced entity chip stays inside its
// column. The collapse this guards against: the widget's root sets
// overflow-wrap: anywhere (inherited by every cell), which drops each
// column's min-content to ~1 character, and width: 100% + auto table layout
// distributes a width deficit proportionally to max-content - so one cell's
// long line (a test command, a URL, a JSON blob) collapses every
// short-max-content column to that ~1-character floor. Measured in the live
// hub 2026-11-19: colW [27, 70, 31, 71, 223] in a 422px column - "GH Status"
// rendered at ~2 content characters per line.
const MIN_COLUMN_CH = 5;

// Bounds are two-sided and NaN-safe: a hidden or unrendered table yields
// all-zero boxes, so ch is 0 and the ratios divide 0/0 into NaN, and every
// NaN comparison is false - a one-sided `< floor` check would pass that
// vacuously. Gating on a usable ch and writing the floor as !(x >= floor)
// turns every degenerate measurement into a failure instead.
function tableFailures(label, ch, colWidths, tableWidth, paneWidth, containerNoun) {
  const failures = [];
  const narrowestCh = Math.min(...colWidths) / ch;
  if (!Number.isFinite(ch) || ch <= 0) {
    failures.push(
      `${label}: ch calibration is ${ch}px - the measurements are unusable, the table is likely hidden or unrendered`,
    );
  } else if (!(narrowestCh >= MIN_COLUMN_CH)) {
    failures.push(
      `${label}: narrowest column is ${Math.min(...colWidths).toFixed(1)}px (${narrowestCh.toFixed(1)}ch) - below the ${MIN_COLUMN_CH}ch floor; a long-content column's max-content is stealing the table's width and collapsing its siblings (auto layout + inherited overflow-wrap: anywhere)`,
    );
  }
  if (tableWidth < paneWidth - 1) {
    failures.push(
      `${label}: table is ${tableWidth.toFixed(1)}px in a ${paneWidth.toFixed(1)}px ${containerNoun} - a width:100% table must fill its ${containerNoun}, the table is likely hidden or unrendered`,
    );
  }
  if (tableWidth > paneWidth + 1) {
    failures.push(
      `${label}: table is ${tableWidth.toFixed(1)}px in a ${paneWidth.toFixed(1)}px ${containerNoun} - the fix must not widen the table past its container`,
    );
  }
  if (Math.max(...colWidths) > tableWidth + 1) {
    failures.push(`${label}: a measured column is wider than its table - the measurements are inconsistent`);
  }
  return failures;
}

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px column`;
    failures.push(...tableFailures(`${label} plain table`, m.ch, m.colWidths, m.tableWidth, m.paneWidth, "pane"));
    failures.push(
      ...tableFailures(`${label} entity table`, m.ch, m.entityColWidths, m.entityTableWidth, m.paneWidth, "pane"),
    );
    // The entity chip: a nowrap chip wider than its fixed-layout column
    // paints over the neighboring cell, so in tables the chip must render
    // (positive width) and keep its right edge inside its own cell.
    if (!(m.chipWidth > 0) || !Number.isFinite(m.chipWidth)) {
      failures.push(`${label} entity chip: chip is ${m.chipWidth}px wide - the chip is not rendering`);
    } else if (m.chipRight > m.chipCellRight + 1) {
      failures.push(
        `${label} entity chip: chip's right edge is at ${m.chipRight.toFixed(1)}px, past its cell's ${m.chipCellRight.toFixed(1)}px - an unwrapped chip is escaping its column and overlapping the neighboring cell`,
      );
    }
    reasons.push(
      `${label}: narrowest column ${Math.min(...m.colWidths).toFixed(1)}px (${(Math.min(...m.colWidths) / m.ch).toFixed(1)}ch), table ${m.tableWidth.toFixed(0)}px in a ${m.paneWidth.toFixed(0)}px pane (colW ${m.colWidths.map((w) => w.toFixed(0)).join("/")}), entity table ${m.entityTableWidth.toFixed(0)}px (colW ${m.entityColWidths.map((w) => w.toFixed(0)).join("/")}), entity chip ${m.chipWidth.toFixed(1)}px wide, right edge at ${m.chipRight.toFixed(1)}px in a ${m.chipCellRight.toFixed(1)}px cell`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
