// A non-zero sliver is not a readable name. Keep at least one real glyph
// beside the ellipsis, independently of the meta's protected 60% cap.
const EPS = 0.5;
const EXPECTED = [
  { width: 360, contentWidth: 328, depth: 0 },
  { width: 390, contentWidth: 358, depth: 2 },
  { width: 430, contentWidth: 398, depth: 4 },
];

function contains(outer, inner) {
  return inner.left >= outer.left - EPS && inner.right <= outer.right + EPS
    && inner.top >= outer.top - EPS && inner.bottom <= outer.bottom + EPS;
}

export default function assert(fixtures) {
  const failures = [];
  const names = [];
  const controls = [];
  if (fixtures.length !== EXPECTED.length) failures.push(`expected ${EXPECTED.length} phone fixtures, got ${fixtures.length}`);
  for (const expected of EXPECTED) {
    const fixture = fixtures.find((entry) => entry.width === expected.width);
    if (!fixture) {
      failures.push(`${expected.width}px: missing fixture`);
      continue;
    }
    const label = `${expected.width}px`;
    if (fixture.viewport.width !== expected.width) failures.push(`${label}: realized viewport ${fixture.viewport.width}px`);
    if (Math.abs(fixture.pane.width - expected.contentWidth) > EPS) {
      failures.push(`${label}: tree pane ${fixture.pane.width}px, expected ${expected.contentWidth}px with the Sheet inset`);
    }
    if (fixture.rows.length !== expected.depth + 1) failures.push(`${label}: expected ${expected.depth + 1} rows, got ${fixture.rows.length}`);
    for (let depth = 0; depth <= expected.depth; depth++) {
      const row = fixture.rows[depth];
      const where = `${label}/depth ${depth}`;
      if (!row) {
        failures.push(`${where}: missing row`);
        continue;
      }
      if (row.depth !== depth || row.level !== depth + 1) failures.push(`${where}: lost nesting, groups=${row.depth}, aria-level=${row.level}`);
      if (row.intentLength !== 200) failures.push(`${where}: intent has ${row.intentLength} characters, expected 200`);
      if (!row.statusDot.visible || Math.abs(row.statusDot.width - 8) > EPS || Math.abs(row.statusDot.height - 8) > EPS) {
        failures.push(`${where}: StatusDot is not its styled 8px box`);
      }
      const nameMinimum = Math.max(16, Math.min(row.nameTextWidth, row.prefixEllipsisWidth));
      if (row.name.width < nameMinimum) failures.push(`${where}: name ${row.name.width.toFixed(2)}px < ${nameMinimum.toFixed(2)}px, no readable prefix beside ellipsis`);
      if (expected.width === 430 && depth === 0 && row.name.width + EPS < row.nameTextWidth) {
        failures.push(`${where}: root name ${row.name.width.toFixed(2)}px does not fit its ${row.nameTextWidth.toFixed(2)}px text`);
      }
      for (const [part, box] of Object.entries({ name: row.name, Open: row.button, meta: row.meta })) {
        if (box.height <= 0 || box.width <= 0 || !box.visible) failures.push(`${where}: ${part} is not visibly measurable`);
        if (!contains(row.box, box) || !contains(fixture.pane, box)) failures.push(`${where}: ${part} escapes its row or pane`);
      }
      if (!row.iconVisible) failures.push(`${where}: Open icon is not visible`);
      if (row.name.right > row.button.left + EPS || row.button.right > row.meta.left + EPS) failures.push(`${where}: name, Open and meta overlap`);
      if (Math.abs(row.button.width - 44) > EPS) failures.push(`${where}: Open width ${row.button.width}px, expected 44px`);
      if (!contains({ left: 0, top: 0, right: fixture.viewport.width, bottom: fixture.viewport.height }, row.button)) failures.push(`${where}: Open escapes the phone viewport`);
      if (row.meta.width > row.contentWidth * 0.6 + EPS) failures.push(`${where}: meta ${row.meta.width.toFixed(2)}px exceeds 60% of ${row.contentWidth.toFixed(2)}px`);
      if (row.meta.scrollWidth <= row.meta.clientWidth) failures.push(`${where}: near-limit intent is not constrained`);
      if (row.meta.overflowX !== "hidden" || row.meta.textOverflow !== "ellipsis" || row.meta.whiteSpace !== "nowrap" || row.meta.height > row.meta.lineHeight + EPS) {
        failures.push(`${where}: meta does not remain a clipped single-line ellipsis`);
      }
      if (row.disabled || row.hits.length !== 5 || row.hits.some((hit) => !hit.ownButton)) failures.push(`${where}: Open hit probes miss its enabled button (${JSON.stringify(row.hits)})`);
      if (row.opened.length !== 1 || row.opened[0] !== `${expected.width}-${depth}` || row.disclosures !== 0) {
        failures.push(`${where}: hit-tested Open activation opened=${JSON.stringify(row.opened)}, disclosures=${row.disclosures}`);
      }
      names.push(row.name.width);
      controls.push(row.button.width);
    }
  }
  return failures.length > 0
    ? { pass: false, reason: failures.join("; ") }
    : { pass: true, reason: `9 near-limit intent rows at 360/390/430px, depths 0/2/4, name >= ${Math.min(...names).toFixed(2)}px, Open >= ${Math.min(...controls).toFixed(2)}px, all 45 hit probes and 9 DOM activations reach their own control` };
}
