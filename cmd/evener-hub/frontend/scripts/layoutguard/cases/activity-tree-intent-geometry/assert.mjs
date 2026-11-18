// A non-zero sliver is not a readable name. Keep at least one real glyph
// beside the ellipsis, independently of the meta's protected 60% cap.
const EPS = 0.5;
// Exact comparisons against the pre-fix measurements: a row that was already
// readable must not move at all, so these are two-sided and tight.
const EXACT = 0.01;
// The visible-name floor the guard insists on: 16px, the smallest box that
// holds a glyph and the ellipsis after it at the 13px UI size.
const READABLE_NAME = 16;

// The issue's own table plus the combinations the fix must not disturb.
// contentWidth is the tree pane: the wide Sheet is
// min(--sheet-inline-size, 100vw) and Dialog's .body holds 16px per side.
// openWidth is the row's Open control: --tap-min on phones, 28px at desktop.
const EXPECTED = [
  { id: "320-0", width: 320, contentWidth: 288, depth: 0, openWidth: 44 },
  { id: "320-1", width: 320, contentWidth: 288, depth: 1, openWidth: 44 },
  { id: "360-0", width: 360, contentWidth: 328, depth: 0, openWidth: 44 },
  { id: "360-3", width: 360, contentWidth: 328, depth: 3, openWidth: 44 },
  { id: "390-2", width: 390, contentWidth: 358, depth: 2, openWidth: 44 },
  { id: "390-8", width: 390, contentWidth: 358, depth: 8, openWidth: 44 },
  { id: "430-0", width: 430, contentWidth: 398, depth: 0, openWidth: 44 },
  { id: "430-4", width: 430, contentWidth: 398, depth: 4, openWidth: 44 },
  { id: "430-8", width: 430, contentWidth: 398, depth: 8, openWidth: 44 },
  { id: "900-0", width: 900, contentWidth: 772, depth: 0, openWidth: 28 },
];

// Rows the near-limit intent already left a readable name on before the fix,
// with their measured name and meta widths. Each is cap-bound: the intent
// sits at exactly 60% of the row's content box. These numbers are held
// unchanged, so the name floor only ever rescues a starved row and never
// re-lays-out a comfortable one.
const UNCHANGED = new Map([
  ["360-0/0", { name: 19.61, meta: 182.39 }],
  ["390-2/0", { name: 31.61, meta: 200.39 }],
  ["390-2/1", { name: 24.0, meta: 189.0 }],
  ["390-2/2", { name: 16.41, meta: 177.59 }],
  ["430-0/0", { name: 45.23, meta: 224.39 }],
  ["430-4/0", { name: 45.23, meta: 224.39 }],
  ["430-4/1", { name: 40.0, meta: 213.0 }],
  ["430-4/2", { name: 32.41, meta: 201.59 }],
  ["430-4/3", { name: 24.81, meta: 190.19 }],
  ["430-4/4", { name: 17.2, meta: 178.8 }],
  ["430-8/0", { name: 45.23, meta: 224.39 }],
  ["430-8/1", { name: 40.0, meta: 213.0 }],
  ["430-8/2", { name: 32.41, meta: 201.59 }],
  ["430-8/3", { name: 24.81, meta: 190.19 }],
  ["430-8/4", { name: 17.2, meta: 178.8 }],
  ["900-0/0", { name: 45.23, meta: 448.8 }],
]);

function contains(outer, inner) {
  return (
    inner.left >= outer.left - EPS &&
    inner.right <= outer.right + EPS &&
    inner.top >= outer.top - EPS &&
    inner.bottom <= outer.bottom + EPS
  );
}

/** The difference between an actual and an expected width, or null when the
 * two agree to EXACT. */
function deviation(actual, expected) {
  return Math.abs(actual - expected) <= EXACT ? null : `${actual.toFixed(2)}px, expected ${expected.toFixed(2)}px`;
}

/** Every per-row contract except the name's own size decision, which the
 * caller makes from UNCHANGED. */
function assertRow(row, where, expected, pane, failures) {
  // The whole point: whatever the intent holds, the name keeps a box wider
  // than one glyph plus its ellipsis.
  const nameMinimum = Math.max(READABLE_NAME, Math.min(row.nameTextWidth, row.prefixEllipsisWidth));
  if (row.name.width < nameMinimum) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px < ${nameMinimum.toFixed(2)}px, no readable prefix beside ellipsis`,
    );
  }
  if (row.name.width < row.prefixEllipsisWidth) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px is narrower than its own glyph-plus-ellipsis prefix ${row.prefixEllipsisWidth.toFixed(2)}px`,
    );
  }
  // A name wider than its own text would mean it stopped ellipsizing.
  if (row.name.width > row.nameTextWidth + EXACT) {
    failures.push(`${where}: name ${row.name.width.toFixed(2)}px exceeds its ${row.nameTextWidth.toFixed(2)}px text`);
  }
  for (const [part, box] of Object.entries({ name: row.name, Open: row.button, meta: row.meta })) {
    if (box.height <= 0 || !box.visible) failures.push(`${where}: ${part} is not visibly measurable`);
    // A fully-starved meta is a legitimate zero-width yield: it keeps its line
    // box and its clipping, it just has no space left. Anything with space
    // must actually measure.
    if ((part !== "meta" || row.meta.clientWidth > 0) && box.width <= 0) {
      failures.push(`${where}: ${part} has no width`);
    }
    if (!contains(row.box, box) || !contains(pane, box)) failures.push(`${where}: ${part} escapes its row or pane`);
  }
  if (!row.iconVisible) failures.push(`${where}: Open icon is not visible`);
  // The name floor must buy space from the intent, never from the Open
  // control: the three still sit side by side, in order, no overlap.
  if (row.name.right > row.button.left + EPS || row.button.right > row.meta.left + EPS) {
    failures.push(`${where}: name, Open and meta overlap`);
  }
  if (Math.abs(row.button.width - expected.openWidth) > EPS) {
    failures.push(`${where}: Open width ${row.button.width}px, expected ${expected.openWidth}px`);
  }
  if (!contains({ left: 0, top: 0, right: expected.width, bottom: 100000 }, row.button)) {
    failures.push(`${where}: Open escapes the viewport`);
  }
  if (row.meta.width > row.contentWidth * 0.6 + EPS) {
    failures.push(`${where}: meta ${row.meta.width.toFixed(2)}px exceeds 60% of ${row.contentWidth.toFixed(2)}px`);
  }
  if (row.meta.scrollWidth <= row.meta.clientWidth) failures.push(`${where}: near-limit intent is not constrained`);
  if (
    row.meta.overflowX !== "hidden" ||
    row.meta.textOverflow !== "ellipsis" ||
    row.meta.whiteSpace !== "nowrap" ||
    row.meta.height > row.meta.lineHeight + EPS
  ) {
    failures.push(`${where}: meta does not remain a clipped single-line ellipsis`);
  }
}

export default function assert(fixtures) {
  const failures = [];
  const names = [];
  const controls = [];
  let rowsChecked = 0;
  let rescued = 0;
  let hitProbes = 0;
  if (fixtures.length !== EXPECTED.length)
    failures.push(`expected ${EXPECTED.length} fixtures, got ${fixtures.length}`);
  for (const expected of EXPECTED) {
    const fixture = fixtures.find((entry) => entry.id === expected.id);
    if (!fixture) {
      failures.push(`${expected.id}: missing fixture`);
      continue;
    }
    if (fixture.width !== expected.width) failures.push(`${expected.id}: fixture reports width ${fixture.width}px`);
    if (fixture.viewport.width !== fixture.width)
      failures.push(`${expected.id}: realized viewport ${fixture.viewport.width}px`);
    if (Math.abs(fixture.pane.width - expected.contentWidth) > EPS) {
      failures.push(
        `${expected.id}: tree pane ${fixture.pane.width}px, expected ${expected.contentWidth}px with the Sheet inset`,
      );
    }
    if (fixture.rows.length !== expected.depth + 1) {
      failures.push(`${expected.id}: expected ${expected.depth + 1} rows, got ${fixture.rows.length}`);
    }
    // The name floor must not buy itself horizontal scroll: the pane keeps
    // clipping its inline axis instead of becoming a sideways scroller.
    if (!["hidden", "clip"].includes(fixture.pane.overflowX)) {
      failures.push(
        `${expected.id}: tree pane overflow-x is ${JSON.stringify(fixture.pane.overflowX)}, expected hidden or clip`,
      );
    }
    if (fixture.pane.scrollWidth > fixture.pane.clientWidth + EPS) {
      failures.push(
        `${expected.id}: tree pane scrollWidth ${fixture.pane.scrollWidth}px exceeds clientWidth ${fixture.pane.clientWidth}px`,
      );
    }
    for (let depth = 0; depth <= expected.depth; depth++) {
      const row = fixture.rows[depth];
      const where = `${expected.id}/depth ${depth}`;
      if (!row) {
        failures.push(`${where}: missing row`);
        continue;
      }
      rowsChecked++;
      if (row.depth !== depth || row.level !== depth + 1)
        failures.push(`${where}: lost nesting, groups=${row.depth}, aria-level=${row.level}`);
      if (row.intentLength !== 200) failures.push(`${where}: intent has ${row.intentLength} characters, expected 200`);
      if (
        !row.statusDot.visible ||
        Math.abs(row.statusDot.width - 8) > EPS ||
        Math.abs(row.statusDot.height - 8) > EPS
      ) {
        failures.push(`${where}: StatusDot is not its styled 8px box`);
      }
      assertRow(row, where, expected, fixture.pane, failures);
      const unchanged = UNCHANGED.get(`${expected.id}/${depth}`);
      if (unchanged) {
        // Already readable before the fix: every box stays where it was.
        const nameDrift = deviation(row.name.width, unchanged.name);
        if (nameDrift) failures.push(`${where}: name moved to ${nameDrift}, this row was readable before the fix`);
        const metaDrift = deviation(row.meta.width, unchanged.meta);
        if (metaDrift) failures.push(`${where}: meta moved to ${metaDrift}, this row was readable before the fix`);
        // The cap is a layout relation, so it carries layout's own rounding;
        // the exact width above is what proves nothing drifted.
        const cap = row.contentWidth * 0.6;
        if (Math.abs(row.meta.width - cap) > EPS) {
          failures.push(`${where}: meta ${row.meta.width.toFixed(2)}px left its ${cap.toFixed(2)}px 60% cap`);
        }
      } else {
        // Starved before the fix: the name now holds the readable floor and
        // is still ellipsized there rather than fitted whole.
        rescued++;
        if (row.name.width >= row.nameTextWidth) {
          failures.push(
            `${where}: name ${row.name.width.toFixed(2)}px was starved before the fix but now fits its whole text`,
          );
        }
      }
      hitProbes += row.hits.length;
      if (row.disabled || row.hits.length !== 5 || row.hits.some((hit) => !hit.ownButton)) {
        failures.push(`${where}: Open hit probes miss its enabled button (${JSON.stringify(row.hits)})`);
      }
      if (row.opened.length !== 1 || row.opened[0] !== `${fixture.width}-${depth}` || row.disclosures !== 0) {
        failures.push(
          `${where}: hit-tested Open activation opened=${JSON.stringify(row.opened)}, disclosures=${row.disclosures}`,
        );
      }
      names.push(row.name.width);
      controls.push(row.button.width);
    }
  }
  return failures.length > 0
    ? { pass: false, reason: failures.join("; ") }
    : {
        pass: true,
        reason: `${rowsChecked} near-limit intent rows at 320/360/390/430px depths 0-8 plus a 900px row: name >= ${Math.min(...names).toFixed(2)}px, Open >= ${Math.min(...controls).toFixed(2)}px, ${rescued} starved rows rescued, ${rowsChecked - rescued} readable rows held exactly, ${hitProbes} hit probes and ${rowsChecked} DOM activations reach their own control, no pane horizontal scroll`,
      };
}
