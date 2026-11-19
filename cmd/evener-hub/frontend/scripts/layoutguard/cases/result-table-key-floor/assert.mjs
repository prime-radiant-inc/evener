// The structured result table's key-column contract: the label column keeps a
// readable floor (>= 10ch of its own font - wide enough for the schema's words
// like "Artifacts" on one line) no matter how huge a value cell's max-content
// line is, and the table never widens past its card. The collapse this guards
// against: .resultKey wraps anywhere (a schema key can be as long as its
// author likes, the stylesheet's own contract), which drops the key column's
// min-content to ~1 character, and width: 100% + auto table layout
// distributes the deficit proportionally to max-content - one JSON.stringify'd
// nested object (~550 characters, one line) collapses the key column to ~30px
// and "Artifacts" renders one character per line. Measured in the live hub
// 2026-11-19: colW [30, 426].
const MIN_KEY_CH = 10;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px card body`;
    const keyCh = m.keyWidth / m.ch;
    if (keyCh < MIN_KEY_CH) {
      failures.push(
        `${label}: key column is ${m.keyWidth.toFixed(1)}px (${keyCh.toFixed(1)}ch) - below the ${MIN_KEY_CH}ch floor; a long max-content value (the JSON.stringify'd data row) is collapsing the label column (auto layout + wrap-anywhere keys)`,
      );
    }
    if (m.tableWidth > m.paneWidth + 1) {
      failures.push(
        `${label}: table is ${m.tableWidth.toFixed(1)}px in a ${m.paneWidth.toFixed(1)}px card - the fix must not widen the table past its card`,
      );
    }
    reasons.push(
      `${label}: key column ${m.keyWidth.toFixed(1)}px (${keyCh.toFixed(1)}ch), value column ${m.valueWidth.toFixed(0)}px, table ${m.tableWidth.toFixed(0)}px in a ${m.paneWidth.toFixed(0)}px card`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
