// The markdown table's layout contract across narrow and wide panes:
// - Wide tables (5+ columns) in narrow containers (<= 559px) reflow into
//   list-style record cards: thead is visually hidden, cells in each row stack
//   vertically inside the pane with zero horizontal overflow, long values wrap
//   across a wide readable line (>= 18ch), and short values (like metric cells)
//   fit cleanly on one line without mid-token splitting.
// - Tables that stay tabular (wide containers > 559px, or <= 4 columns at any
//   width) keep fixed-layout equal columns above the 5ch floor, fill the pane,
//   and keep enhanced entity chips inside their own cell.
const REFLOW_MAX_WIDTH = 559;
const MIN_COLUMN_CH = 5;
const MIN_LONG_VALUE_CH = 18;
const LONG_TEXT_CHARS = 30;
const SHORT_TEXT_CHARS = 18;

// Bounds are two-sided and NaN-safe so hidden/unrendered zero-width boxes fail
// instead of passing vacuously.
function tabularFailures(label, table, paneWidth) {
  const failures = [];
  const narrowest = Math.min(...table.colWidths);
  const narrowestCh = narrowest / table.ch;
  if (!Number.isFinite(table.ch) || table.ch <= 0) {
    failures.push(`${label}: ch calibration is ${table.ch}px - table is hidden or unrendered`);
  } else if (!(narrowestCh >= MIN_COLUMN_CH)) {
    failures.push(
      `${label}: narrowest column is ${narrowest.toFixed(1)}px (${narrowestCh.toFixed(1)}ch) - below the ${MIN_COLUMN_CH}ch floor`,
    );
  }
  if (table.theadWidth <= 1) {
    failures.push(`${label}: thead is ${table.theadWidth.toFixed(1)}px wide - tabular mode must keep headers visible`);
  }
  if (table.tableWidth < paneWidth - 1) {
    failures.push(`${label}: table is ${table.tableWidth.toFixed(1)}px in a ${paneWidth.toFixed(1)}px pane`);
  }
  if (table.tableWidth > paneWidth + 1 || table.wrapWidth > paneWidth + 1) {
    failures.push(
      `${label}: table (${table.tableWidth.toFixed(1)}px) / wrapper (${table.wrapWidth.toFixed(1)}px) widens past its ${paneWidth.toFixed(1)}px pane`,
    );
  }
  if (Math.max(...table.colWidths) > table.tableWidth + 1) {
    failures.push(`${label}: a measured column is wider than its table`);
  }
  return failures;
}

function reflowFailures(label, table, paneWidth) {
  const failures = [];
  if (!Number.isFinite(table.ch) || table.ch <= 0) {
    failures.push(`${label}: ch calibration is ${table.ch}px - table is hidden or unrendered`);
    return failures;
  }
  if (table.theadWidth > 2) {
    failures.push(
      `${label}: thead is ${table.theadWidth.toFixed(1)}px wide - list-style reflow must visually hide thead`,
    );
  }
  if (table.wrapWidth > paneWidth + 1 || table.tableWidth > paneWidth + 1) {
    failures.push(
      `${label}: table (${table.tableWidth.toFixed(1)}px) / wrapper (${table.wrapWidth.toFixed(1)}px) widens past its ${paneWidth.toFixed(1)}px pane`,
    );
  }
  if (!Array.isArray(table.rowCells) || table.rowCells.length < 2) {
    failures.push(`${label}: missing row cells to verify list-style stacking`);
    return failures;
  }
  // Card outer border (2px) + body inset margins (24px = 2 * --space-3).
  const minCellWidth = paneWidth - 28;
  for (let i = 0; i < table.rowCells.length; i += 1) {
    const cell = table.rowCells[i];
    if (cell.width < minCellWidth || cell.width > paneWidth + 1) {
      failures.push(
        `${label} cell ${i}: cell is ${cell.width.toFixed(1)}px wide in a ${paneWidth.toFixed(1)}px pane - reflowed cells must span the record card`,
      );
    }
    if (!(cell.valueWidth > 0) || !Number.isFinite(cell.valueWidth)) {
      failures.push(`${label} cell ${i}: value box is zero-width or hidden`);
    }
    if (cell.valueRight > table.paneRight + 1) {
      failures.push(
        `${label} cell ${i}: value right edge ${cell.valueRight.toFixed(1)}px escapes pane right ${table.paneRight.toFixed(1)}px`,
      );
    }
    if (cell.textLength >= LONG_TEXT_CHARS) {
      const valueCh = cell.valueWidth / table.ch;
      if (!(valueCh >= MIN_LONG_VALUE_CH)) {
        failures.push(
          `${label} cell ${i}: long value is ${cell.valueWidth.toFixed(1)}px (${valueCh.toFixed(1)}ch) - below the ${MIN_LONG_VALUE_CH}ch readable floor`,
        );
      }
    } else if (cell.textLength <= SHORT_TEXT_CHARS && cell.lineHeight > 0) {
      if (cell.valueHeight > cell.lineHeight * 1.8) {
        failures.push(
          `${label} cell ${i}: short value wrapped to ${cell.valueHeight.toFixed(1)}px (line-height ${cell.lineHeight.toFixed(1)}px) - must fit on one line`,
        );
      }
    }
    if (i > 0) {
      const prev = table.rowCells[i - 1];
      if (cell.top < prev.bottom - 1) {
        failures.push(
          `${label} cell ${i}: top ${cell.top.toFixed(1)}px overlaps previous cell bottom ${prev.bottom.toFixed(1)}px - cells are not stacked in list mode`,
        );
      }
    }
  }
  return failures;
}

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px column`;
    const expectReflow = m.paneWidth <= REFLOW_MAX_WIDTH;
    const checkReflowable = expectReflow ? reflowFailures : tabularFailures;
    failures.push(
      ...checkReflowable(`${label} 5-col table`, m.plain, m.paneWidth),
      ...checkReflowable(`${label} 8-col table`, m.wide, m.paneWidth),
    );
    // 2-column entity table always stays tabular with its chip contained.
    failures.push(...tabularFailures(`${label} entity table`, m.entity, m.paneWidth));
    if (!(m.chipWidth > 0) || !Number.isFinite(m.chipWidth)) {
      failures.push(`${label} entity chip: chip is ${m.chipWidth}px wide - the chip is not rendering`);
    } else if (m.chipRight > m.chipCellRight + 1) {
      failures.push(
        `${label} entity chip: chip's right edge is at ${m.chipRight.toFixed(1)}px, past its cell's ${m.chipCellRight.toFixed(1)}px`,
      );
    }
    const wideSummary = expectReflow
      ? `list-reflow (lead ${m.wide.rowCells[0].valueWidth.toFixed(0)}px, max short height ${Math.max(...m.wide.rowCells.slice(1).map((c) => c.valueHeight)).toFixed(0)}px)`
      : `tabular (colW ${m.wide.colWidths.map((w) => w.toFixed(0)).join("/")})`;
    reasons.push(
      `${label}: 8-col ${wideSummary}, entity chip ${m.chipWidth.toFixed(1)}px inside ${m.chipCellRight.toFixed(1)}px cell`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
