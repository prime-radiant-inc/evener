// A non-zero sliver is not a readable name. Keep at least one real glyph
// beside the ellipsis, independently of the meta's protected 60% cap.
const EPS = 0.5;
// Exact comparisons against the same row measured with the floor switched
// off, in the same browser run: a row the floor did not have to rescue must
// not move at all, so these are two-sided and tight. Both sides are live
// measurements, so a font-metric update moves them together instead of
// failing the gate on its own.
const EXACT = 0.01;
// The name floor the stylesheet declares, in em of the row's own font size.
// CSS cannot measure a grapheme, so the shipped floor is one constant that
// clears the widest first glyph measured in the bundled fonts: 1.78em for an
// emoji, 1.75em for a full-width ideograph, 1.61em for a Latin capital, and
// 0.97em for "inspect"'s first letter. The guard re-derives the expectation
// from the row's MEASURED font size, so a px floor, or a `ch` floor (which
// measures the digit "0" and is 1.23em here), fails it.
const FLOOR_EM = 1.8;

// The issue's own table plus the combinations the fix must not disturb. The
// floor is a text-relative budget, so the matrix carries labels: Latin,
// full-width CJK and emoji (the widest common grapheme) prove the floor
// covers the glyph rather than the digit `ch` measures.
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
  { id: "430-0", width: 430, contentWidth: 398, depth: 0, openWidth: 44, label: "inspect" },
  { id: "430-4", width: 430, contentWidth: 398, depth: 4, openWidth: 44, label: "inspect" },
  { id: "430-8", width: 430, contentWidth: 398, depth: 8, openWidth: 44, label: "inspect" },
  { id: "900-0", width: 900, contentWidth: 772, depth: 0, openWidth: 28, label: "inspect" },
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
 * caller makes from the live reference and the measured floor.
 *
 * `room` is the row's content box less the fixed controls that sit beside the
 * name: the row's four inter-child gaps, the disclosure toggle, the kind glyph
 * and the Open control - all measured on this row, in this run. When room
 * cannot hold a glyph and its ellipsis the row has nothing to give, so the
 * name is allowed to fall back rather than buying a clipped tap target. */
function assertRow(row, where, expected, pane, failures, room, floorPx) {
  const affordable = room >= row.prefixEllipsisWidth;
  // The whole point: whatever the intent holds, the name keeps a box wide
  // enough for one glyph plus its ellipsis. The comparison is against the
  // measured first glyph of THIS row's label, which is why a floor derived
  // from the digit "0" fails on a full-width ideograph.
  if (affordable && row.name.width < row.prefixEllipsisWidth - EXACT) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px < its ${row.prefixEllipsisWidth.toFixed(2)}px glyph-plus-ellipsis prefix, with ${room.toFixed(2)}px available`,
    );
  }
  if (row.name.width > row.nameTextWidth + EXACT) {
    failures.push(`${where}: name ${row.name.width.toFixed(2)}px exceeds its ${row.nameTextWidth.toFixed(2)}px text`);
  }
  // The floor never spends more than the row can spare: whatever is left
  // after the fixed controls, and never a negative claim.
  if (row.name.width > Math.max(0, room) + EXACT) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px claims more than the ${Math.max(0, room).toFixed(2)}px the row has left after its controls`,
    );
  }
  // A floor is a minimum, so a wider name is fine: comfortable rows keep
  // their full label. `floorPx` is used below as the bound the name lands on
  // when the floor is what sets its width.
  void floorPx;
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
      const floorPx = FLOOR_EM * row.fontSize;
      assertRow(row, where, expected, fixture.pane, failures, room, floorPx);
      // The floor is min(font-derived budget, room left after the controls).
      // A row that already measured at or above the budget is untouched; any
      // other row lands exactly on that bound. `row.reference` is the SAME row
      // with the floor switched off, measured in this same run, so a font
      // metric change cannot fail this and any movement the FLOOR causes is
      // still caught to the hundredth of a pixel.
      const expectedName =
        row.reference.name >= floorPx - EXACT ? row.reference.name : Math.max(0, Math.min(floorPx, room));
      const drift = deviation(row.name.width, expectedName);
      if (drift)
        failures.push(
          `${where}: name ${drift} from ${expectedName === row.reference.name ? "its floor-off" : "the floor"} geometry`,
        );
      if (row.reference.name >= floorPx - EXACT) {
        held++;
        const metaDrift = deviation(row.meta.width, row.reference.meta);
        if (metaDrift) failures.push(`${where}: meta moved to ${metaDrift} from the same row without the floor`);
      } else if (row.name.width >= row.prefixEllipsisWidth - EXACT) {
        // Rescued: the name now shows an identifying glyph beside its
        // ellipsis where the intent had reduced it to a bare sliver.
        rescued++;
      } else {
        // Too narrow for any prefix even after the meta yielded to zero: the
        // name gives the space back so the Open target keeps its full width.
        yielded++;
        if (room >= row.prefixEllipsisWidth) {
          failures.push(
            `${where}: name ${row.name.width.toFixed(2)}px fell short of its ${row.prefixEllipsisWidth.toFixed(2)}px prefix with ${room.toFixed(2)}px available`,
          );
        }
      }
      // A clipped Open target is a smaller target: wherever the fixed
      // controls fit in the row at all, the floor must leave the target
      // inside the pane. Where they do not fit, the name has already yielded
      // to zero and the overflow is the row's own minimum, unchanged by the
      // floor.
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
