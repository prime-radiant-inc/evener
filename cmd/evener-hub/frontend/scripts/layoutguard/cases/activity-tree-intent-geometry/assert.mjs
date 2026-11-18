// A non-zero sliver is not a readable name. Keep at least one real glyph
// beside the ellipsis, independently of the meta's protected 60% cap.
const EPS = 0.5;
// Exact comparisons against the same row measured with the floor switched
// off, in the same browser run: a row that showed its WHOLE label in the
// reference pass must not move at all, so those comparisons are two-sided and
// tight. A row the reference pass measured below its full text was truncated
// and the fix may give it more, so it is not held to its reference width.
// Both sides are live measurements, so a font-metric update moves them
// together instead of failing the gate on its own.
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
  // No stated intent at all: this row shows status and timing in the meta, and
  // a rule that gives the name priority over an intent must not apply to it.
  // At the worst width in the issue, a long status beside a long name is the
  // shape that collapses the status to zero when the name refuses to shrink.
  {
    id: "320-0-no-intent",
    width: 320,
    contentWidth: 288,
    depth: 0,
    openWidth: 44,
    label: "inspect",
    noIntent: true,
    labelChars: 160,
  },
  // The case the name cap exists for: a name long enough to want the whole row,
  // on a row that DOES state an intent. This is the only column where the cap
  // binds, so it is the only one that proves the name's upper bound and the
  // Open containment check bite together - without it the long-name row has no
  // intent, and the intent rows all have short names that never reach the cap.
  {
    id: "320-0-long-intent",
    width: 320,
    contentWidth: 288,
    depth: 0,
    openWidth: 44,
    label: "inspect",
    labelChars: 160,
  },
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

/** The name's own readable floor: one measured glyph of THIS row's label plus
 * its ellipsis, or the whole label where the label is shorter than that
 * prefix. A label shorter than its own glyph-plus-ellipsis prefix cannot show
 * that prefix inside its own text width; the most it can hold is the whole
 * label, which is why the need is capped at the label's width - a box wider
 * than the text it holds is padding, not a readable name.
 *
 * Shared by the per-row assertion below and by the rescue counter, so a width
 * one of them calls readable is the same width the other does. */
function readableNameNeed(row) {
  return Math.min(row.prefixEllipsisWidth, row.nameFullWidth);
}

/** Every per-row contract except the name's own size decision, which the
 * caller makes from the live reference.
 *
 * `room` is the row's content box less the fixed controls that sit beside the
 * name: the row's four inter-child gaps, the disclosure toggle, the kind glyph
 * and the Open control - all measured on this row, in this run. It is reported
 * in the failure text, never used to excuse one: a guard that goes quiet
 * exactly where the layout is worst certifies the case that matters most. */
function assertRow(row, where, expected, pane, failures, room) {
  const need = readableNameNeed(row);
  // The whole point: whatever the intent holds, the name keeps a box wide
  // enough for one glyph plus its ellipsis, or for the whole label when the
  // label is shorter than that. The comparison is against the measured first
  // glyph of THIS row's label, which is why a reservation derived from the
  // digit "0" fails on a full-width ideograph. No size is exempt: the indent
  // step and the shrink weight are what keep this satisfiable at any depth, so
  // a failure here is the layout's, not the fixture's.
  if (row.name.width < need - EXACT) {
    failures.push(
      `${where}: name ${row.name.width.toFixed(2)}px < its ${need.toFixed(2)}px glyph-plus-ellipsis prefix, with ${room.toFixed(2)}px available`,
    );
  }
  // A long name must not take the WHOLE room. The meta is the only place the
  // row states what it is doing - its expanded detail does not repeat it - so a
  // meta squeezed to nothing leaves a row that shows neither its name nor its
  // work. Required wherever the room affords BOTH floors; not demanded where it
  // does not, because there the name's floor is the documented precedence and
  // the check above already holds the name to it. Compared against the floors,
  // not the name's full text: the row never promised the name its whole label,
  // and using the full width made this check pass in the state it exists to
  // catch.
  const metaNeed = Math.min(row.metaPrefixEllipsisWidth, row.meta.scrollWidth);
  if (need + metaNeed <= room - EXACT && row.meta.width < metaNeed - EXACT) {
    failures.push(
      `${where}: meta ${row.meta.width.toFixed(2)}px is under its ${metaNeed.toFixed(2)}px prefix though the row could afford it`,
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
  let alreadyReadable = 0;
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
      if (expected.noIntent) {
        // A row that states no intent shows status and timing instead, and the
        // name must not be handed priority over them: a rule written for
        // intent priority must not starve ordinary metadata on rows that have
        // no intent to arbitrate with.
        if (row.intentLength !== 0) failures.push(`${where}: no-intent row carries an intent`);
        if (row.meta.clientWidth <= 0) {
          failures.push(`${where}: status meta collapsed to ${row.meta.width.toFixed(2)}px on a row with no intent`);
        }
      } else if (row.intentLength !== 200) {
        failures.push(`${where}: intent has ${row.intentLength} characters, expected 200`);
      }
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
      // `row.reference` is the SAME row measured in this same run with the
      // name-priority rule removed, so a reference width below this row's own
      // readable prefix is a direct measurement that the intent had starved it -
      // not an inference from the row's final width. That distinction is what
      // the counters below have to respect: a row that ends up readable proves
      // only that it ends up readable. It is evidence of a RESCUE only if it
      // was also starved in the reference pass.
      const need = readableNameNeed(row);
      const reachesNeed = row.name.width >= need - EXACT;
      if (row.reference.name >= row.nameFullWidth - EXACT) {
        // The row showed its WHOLE label before this change, so it was never
        // under pressure: it must be exactly where it was. A row squeezed to a
        // prefix was not comfortable, and the fix is allowed to give it more.
        held++;
        const nameDrift = deviation(row.name.width, row.reference.name);
        if (nameDrift) failures.push(`${where}: name moved to ${nameDrift} from the same row without the fix`);
        const metaDrift = deviation(row.meta.width, row.reference.meta);
        if (metaDrift) failures.push(`${where}: meta moved to ${metaDrift} from the same row without the fix`);
      } else if (row.reference.name < need - EXACT && reachesNeed) {
        // Rescued: the reference pass measured this name below its own
        // glyph-plus-ellipsis prefix and the final layout reaches it. This is
        // the only shape the word "rescued" describes.
        rescued++;
      } else if (reachesNeed) {
        // Above its prefix in the reference pass, so the intent had not
        // starved it and the fix cannot be credited with making it readable.
        // Its label was still TRUNCATED in that pass - a row that showed the
        // whole label went to `held` above - and the final layout may resolve
        // it to the whole label, since this row is not held to its reference
        // width. So "already readable" is the claim; "unchanged" would be
        // false, and so would "still truncated at the end".
        alreadyReadable++;
      } else {
        // The name did not reach its prefix. `assertRow` has already failed
        // this row, so there is nothing to excuse here: this branch only
        // counts it. The indent step and the shrink weight are what keep the
        // prefix affordable at every depth, which is why the count of these
        // rows is expected to be zero rather than tolerated.
        yielded++;
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
      // A clipped Open target is a smaller target: the name must never push
      // the target outside the pane. No depth is exempt - the indent step is
      // capped as a share of the width for exactly this reason.
      if (row.button.right > fixture.pane.right + EPS) {
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
  // The counters above are a partition, not a set of overlapping tallies: each
  // row that reached the counting falls into exactly one of them. Asserting
  // the sum makes a future edit that drops a row from the accounting fail the
  // case instead of quietly shrinking a reported number.
  if (rescued + alreadyReadable + held + yielded !== rowsChecked) {
    failures.push(
      `rescue accounting covers ${rescued + alreadyReadable + held + yielded} rows, expected ${rowsChecked}`,
    );
  }
  // The matrix carries one row that states no intent on purpose, so the
  // measured total is not a total of intent rows. Derived from the matrix
  // rather than restated, so the message cannot drift from what ran.
  const noIntentRows = EXPECTED.reduce((sum, entry) => sum + (entry.noIntent ? entry.depth + 1 : 0), 0);
  return failures.length > 0
    ? { pass: false, reason: failures.join("; ") }
    : {
        pass: true,
        reason: `${rowsChecked} measured rows at 320/360/390/430px depths 0-9 plus a 900px row, Latin, CJK, emoji and xl labels (${rowsChecked - noIntentRows} near-limit intent rows plus ${noIntentRows} no-intent control row): every name >= its own measured glyph-plus-ellipsis prefix with no size exempt, Open >= ${Math.min(...controls).toFixed(2)}px and never past the pane edge, ${rescued} starved rows rescued (measured below the prefix without the fix and at or above it with it), ${alreadyReadable} rows already at or above the prefix without the fix but with a truncated label, not counted as rescues, ${yielded} rows fell short, ${held} rows showed their whole label and held exactly, ${hitProbes} hit probes and ${rowsChecked} DOM activations reach their own control, no pane horizontal scroll`,
      };
}
