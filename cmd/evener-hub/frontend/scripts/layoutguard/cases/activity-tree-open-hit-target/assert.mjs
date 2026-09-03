// The dense activity tree's Open targets on a pitch (~23.5px) below even the
// 28px desktop hit size. Four invariants, at desktop (28px) and phone
// (--tap-min 44px) widths:
//
//   1. WIDTH. The hit width survives: 28px desktop, 44px phone.
//   2. NO OVERHANG. The target never exceeds its own row's band (stretch
//      fills the row's CONTENT box - the line cross size - so it sits a
//      couple of px inside the row's padding), so consecutive rows' targets
//      share no points (measured maxOverlap). Overlap would let a tap in a
//      row's top band open the row BELOW.
//   3. LEADING. A row with the control is exactly as tall as the plain
//      reference row without one.
//   4. OWNERSHIP. elementFromPoint at each sampled target's center and its
//      exposed top/bottom inner edges reaches that row's own button - first,
//      middle (neighbor boundaries), and last rows.
//
// Font- and platform-independent: everything is measured against the rows'
// own rects, never a pixel snapshot.

const TOLERANCE_PX = 0.5;

export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    for (const edge of ["first", "mid", "last"]) {
      const sample = fixture[edge];
      if (Math.abs(sample.target.width - fixture.expectedTargetWidth) > TOLERANCE_PX) {
        failures.push(
          `${fixture.mode} ${edge}: Open target width is ${sample.target.width.toFixed(1)}px, expected the ${fixture.expectedTargetWidth}px hit width`,
        );
      }
      if (
        sample.target.top < sample.row.top - TOLERANCE_PX ||
        sample.target.bottom > sample.row.bottom + TOLERANCE_PX
      ) {
        failures.push(
          `${fixture.mode} ${edge}: Open target [${sample.target.top.toFixed(1)}, ${sample.target.bottom.toFixed(1)}] escapes its own row's band [${sample.row.top.toFixed(1)}, ${sample.row.bottom.toFixed(1)}] - neighbors' targets can overlap`,
        );
      }
      if (Math.abs(sample.row.height - fixture.plain.height) > TOLERANCE_PX) {
        failures.push(
          `${fixture.mode} ${edge}: a row with Open is ${sample.row.height.toFixed(1)}px vs the plain row's ${fixture.plain.height.toFixed(1)}px - the control reached the leading`,
        );
      }
      const missed = sample.hits.filter((hit) => !hit.reachesButton);
      if (missed.length > 0) {
        failures.push(
          `${fixture.mode} ${edge}: ${missed.length}/${sample.hits.length} elementFromPoint probes missed the row's own Open (${missed.map((hit) => `${hit.x.toFixed(1)},${hit.y.toFixed(1)}→${hit.hit ?? "null"}`).join(", ")})`,
        );
      }
    }
    if (fixture.maxOverlap > TOLERANCE_PX) {
      failures.push(
        `${fixture.mode}: consecutive rows' Open targets overlap by up to ${fixture.maxOverlap.toFixed(1)}px - a tap near a row boundary can open the wrong transcript`,
      );
    }
    if (fixture.treeOverflow.y !== "visible") {
      failures.push(
        `${fixture.mode}: .tree block-axis overflow computed to ${fixture.treeOverflow.y}, expected visible (inline clipping must not become block clipping)`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  return {
    pass: true,
    reason: measurement
      .map(
        (f) =>
          `${f.mode}: ${f.expectedTargetWidth}px-wide targets stay inside their own rows (first target ${f.first.target.height.toFixed(1)}px within a ${f.first.row.height.toFixed(1)}px row = plain ${f.plain.height.toFixed(1)}px), max neighbor overlap ${f.maxOverlap.toFixed(1)}px, ${f.first.hits.length + f.mid.hits.length + f.last.hits.length}/${f.first.hits.length + f.mid.hits.length + f.last.hits.length} probes reach their own row's Open`,
      )
      .join(" | "),
  };
}
