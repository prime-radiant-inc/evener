// A non-zero sliver is not a readable name. Keep at least one real glyph
// beside the ellipsis, independently of the meta's protected 60% cap.
const EPS = 0.5;
// Exact comparisons against the same row measured with the floor switched
// off, in the same browser run: a row the floor did not have to rescue must
// not move at all, so these are two-sided and tight. Both sides are live
// measurements, so a font-metric update moves them together instead of
// failing the gate on its own.
const EXACT = 0.01;
// The issue's own table plus the combinations the fix must not disturb. The
// fix reserves the name a glyph-plus-ellipsis prefix, so the matrix carries
// labels: Latin, full-width CJK and emoji (the widest common grapheme) prove
// the reservation covers the glyph rather than the digit `ch` measures.
// contentWidth is the tree pane: the wide Sheet is
// min(--sheet-inline-size, 100vw) and Dialog's .body holds 16px per side.
// openWidth is the row's Open control: --tap-min on phones, 28px at desktop.
const EXPECTED = [
  { id: "320-0", width: 320, contentWidth: 288, depth: 0, openWidth: 44, label: "inspect" },
  { id: "320-1", width: 320, contentWidth: 288, depth: 1, openWidth: 44, label: "inspect" },
  { id: "320-8-cjk", width: 320, contentWidth: 288, depth: 8, openWidth: 44, label: "\u8abf\u67fb" },
  { id: "320-9", width: 320, contentWidth: 288, depth: 9, openWidth: 44, label: "inspect" },
  { id: "360-0", width: 360, contentWidth: 328, depth: 0, openWidth: 44, label: "inspect" },
  { id: "360-3", width: 360, contentWidth: 328, depth: 3, openWidth: 44, label: "inspect" },
  { id: "390-2", width: 390, contentWidth: 358, depth: 2, openWidth: 44, label: "inspect" },
  { id: "390-8", width: 390, contentWidth: 358, depth: 8, openWidth: 44, label: "inspect" },
  { id: "390-8-cjk", width: 390, contentWidth: 358, depth: 8, openWidth: 44, label: "\u8abf\u67fb" },
  { id: "390-8-emoji", width: 390, contentWidth: 358, depth: 8, openWidth: 44, label: "\u{1F600}\u{1F680}" },
  { id: "430-0", width: 430, contentWidth: 398, depth: 0, openWidth: 44, label: "inspect", fitFullText: true },
  { id: "430-4", width: 430, contentWidth: 398, depth: 4, openWidth: 44, label: "inspect" },
  { id: "430-8", width: 430, contentWidth: 398, depth: 8, openWidth: 44, label: "inspect" },
  { id: "900-0", width: 900, contentWidth: 772, depth: 0, openWidth: 28, label: "inspect", fitFullText: true },
  // A larger text preference must earn a wider prefix: the floor is in em, so
  // the same row at the `xl` font scale reserves 1.8 of a LARGER em. The guard
  // compares the name against the first glyph measured at whatever size the
  // row actually renders, so a px floor fails here and a ch floor fails the
  // wide-glyph rows above.
  {
    id: "390-8-cjk-xl",
    width: 390,
    contentWidth: 358,
    depth: 8,
    openWidth: 44,
    label: "\u8abf\u67fb",
    fontScale: "xl",
  },
  { id: "430-4-xl", width: 430, contentWidth: 398, depth: 4, openWidth: 44, label: "inspect", fontScale: "xl" },
];

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
 * caller makes from the live reference.
 *
 * `room` is the row's content box less the fixed controls that sit beside the
 * name: the row's four inter-child gaps, the disclosure toggle, the kind glyph
 * and the Open control - all measured on this row, in this run. When room
 * cannot hold a glyph and its ellipsis the row has nothing to give, so the
 * name is allowed to fall back rather than buying a clipped tap target. */
function assertRow(row, where, expected, pane, failures, room) {
  // A label shorter than its own glyph-plus-ellipsis prefix cannot show that
  // prefix inside its own text width; the most it can hold is the whole label.
  // That is why the reservation is capped at the label's width - a box wider
  // than the text it holds is padding, not a readable name.
  const need = Math.min(row.prefixEllipsisWidth, row.nameFullWidth);
  const affordable = room >= need;
  // The whole point: whatever the intent holds, the name keeps a box wide
  // enough for one glyph plus its ellipsis, or for the whole label when the
  // label is shorter than that. The comparison is against the measured first
  // glyph of THIS row's label, which is why a reservation derived from the
  // digit "0" fails on a full-width ideograph.
  if (affordable && row.name.width < need - EXACT) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px < its ${need.toFixed(2)}px glyph-plus-ellipsis prefix, with ${room.toFixed(2)}px available`,
    );
  }
  // The name box never exceeds the label it holds. A min-width above the
  // label's advance pads the box - the CI failure this guard exists to catch -
  // so the box is bounded by the canvas-measured advance, capped at the label.
  if (row.name.width > Math.min(row.nameFullWidth, row.nameTextWidth) + EXACT) {
    failures.push(`${where}: name ${row.name.width.toFixed(2)}px exceeds its ${row.nameFullWidth.toFixed(2)}px text`);
  }
  for (const [part, box] of Object.entries({ name: row.name, Open: row.button, meta: row.meta })) {
    if (box.height <= 0 || !box.visible) failures.push(`${where}: ${part} is not visibly measurable`);
    // A fully-starved meta is a legitimate zero-width yield: it keeps its line
    // box and its clipping, it just has no space left. Anything with space
    // must actually measure.
    if ((part !== "meta" || row.meta.clientWidth > 0) && box.width <= 0 && part !== "name") {
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
  // The measured iframe height, not a sentinel: a control hanging below the
  // frame fails here, where a 100000px bound would have passed it.
  if (!contains({ left: 0, top: 0, right: expected.width, bottom: expected.viewportHeight }, row.button)) {
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
  let held = 0;
  let yielded = 0;
  let hitProbes = 0;
  if (fixtures.length !== EXPECTED.length)
    failures.push(`expected ${EXPECTED.length} fixtures, got ${fixtures.length}`);
  for (const expected of EXPECTED) {
    const fixture = fixtures.find((entry) => entry.id === expected.id);
    if (!fixture) {
      failures.push(`${expected.id}: missing fixture`);
      continue;
    }
    if (fixture.width !== expected.width) failures.push(`${expected.id}: fixture reports width ${expected.width}px`);
    if (fixture.viewport.width !== fixture.width)
      failures.push(`${expected.id}: realized viewport ${fixture.viewport.width}px`);
    expected.viewportHeight = fixture.viewport.height;
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
      // Everything the row spends before the name, measured here: four
      // inter-child gaps, the disclosure toggle, the kind glyph and the Open
      // target this row actually rendered.
      const room = row.contentWidth - (4 * row.gap + row.toggleWidth + row.kindWidth + row.button.width);
      assertRow(row, where, expected, fixture.pane, failures, room);
      // The fix is the meta's cap, so `row.reference` is the SAME row with the
      // pre-fix 60%-only cap, measured in this same run: a font-metric change
      // moves both sides together, while any movement the CAP causes is still
      // caught to the hundredth of a pixel.
      if (row.reference.name >= row.nameFullWidth - EXACT) {
        // The row showed its WHOLE label before this change, so it was never
        // under pressure: it must be exactly where it was. A row squeezed to a
        // prefix was not comfortable, and the fix is allowed to give it more.
        held++;
        const nameDrift = deviation(row.name.width, row.reference.name);
        if (nameDrift) failures.push(`${where}: name moved to ${nameDrift} from the same row without the fix`);
        const metaDrift = deviation(row.meta.width, row.reference.meta);
        if (metaDrift) failures.push(`${where}: meta moved to ${metaDrift} from the same row without the fix`);
      } else if (row.name.width >= Math.min(row.prefixEllipsisWidth, row.nameFullWidth) - EXACT) {
        // Rescued: the name now shows an identifying glyph beside its
        // ellipsis where the intent had reduced it to a bare sliver.
        rescued++;
      } else {
        // Too narrow for any prefix even after the meta yielded to zero: the
        // name gives the space back so the Open target keeps its full width.
        yielded++;
        if (room >= Math.min(row.prefixEllipsisWidth, row.nameFullWidth)) {
          failures.push(
            `${where}: name ${row.name.width.toFixed(2)}px fell short of its ${row.prefixEllipsisWidth.toFixed(2)}px prefix with ${room.toFixed(2)}px available`,
          );
        }
      }
      // A comfortable row keeps its whole label: the cap must never truncate a
      // name the row had room to show in full. This is what stops a too-small
      // max-width on the name from passing - both sides of the live comparison
      // above would then measure the same truncated width.
      if (expected.fitFullText && row.name.width < row.nameFullWidth - EXACT) {
        failures.push(
          `${where}: name ${row.name.width.toFixed(2)}px truncates a ${row.nameFullWidth.toFixed(2)}px label the row had room for in full`,
        );
      }
      // A clipped Open target is a smaller target: wherever the fixed
      // controls fit in the row at all, the name must not push the target
      // outside the pane. Where they do not fit, the overflow is the row's own
      // minimum, unchanged by this change.
      if (room >= 0 && row.button.right > fixture.pane.right + EPS) {
        failures.push(
          `${where}: Open ${row.button.right.toFixed(2)}px overhangs the pane edge ${fixture.pane.right.toFixed(2)}px`,
        );
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
        reason: `${rowsChecked} near-limit intent rows at 320/360/390/430px depths 0-9 plus a 900px row, Latin, CJK, emoji and xl labels: name >= its measured glyph-plus-ellipsis prefix wherever the row can fit one, Open >= ${Math.min(...controls).toFixed(2)}px, ${rescued} starved rows rescued, ${yielded} rows too narrow for a prefix left the space to Open, ${held} readable rows held exactly, ${hitProbes} hit probes and ${rowsChecked} DOM activations reach their own control, no pane horizontal scroll`,
      };
}
