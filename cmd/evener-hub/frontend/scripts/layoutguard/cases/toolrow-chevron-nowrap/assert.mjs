// The invariant from the bug report (screenshot: three read/grep rows whose
// trailing disclosure arrows sat alone on their own wrapped lines): a
// disclosure arrow may never wrap to a line by itself. For an intent-bearing
// tool row that means the chevron vertically shares the intent's LAST text
// line at every width - when the line runs out of room, the words it opens
// move with it.
//
// The fixtures sweep a width ladder per column, because whether an intent's
// last line lands within a chevron-width (14px box + 8px margin) of the edge
// is a property of font metrics the harness must not guess. Two consequences,
// both asserted:
//
//   1. EVERY swept width must satisfy the invariant, not just a binding one -
//      the non-binding widths are the no-regression half (the chevron must
//      stay inline when there IS room, so the fix cannot "solve" stranding by
//      parking the glyph on its own line at every width).
//   2. EVERY swept fixture must have FOUND a binding width, measured on its
//      chevron-less probe row (whose text-only wrap geometry is exactly what
//      the unglued DOM's text sees): the unglued intent's last line with less
//      than a chevron unit of slack. If a font or platform change moves the
//      geometry so no swept width binds, the fixture would pass vacuously -
//      a guard that can no longer see the defect - and that is a failure,
//      not a pass.

export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    const where = fixture.id;
    if (!fixture.sweep) {
      const m = fixture.fixed;
      if (m.lineCount !== 1) {
        failures.push(
          `${where}: short intent rendered ${m.lineCount} lines; fixture no longer exercises the one-line case`,
        );
      }
      if (!m.sharesLastLine) {
        failures.push(
          `${where}: chevron [${m.chevronTop.toFixed(1)}, ${m.chevronBottom.toFixed(1)}] does not share the intent's single line [${m.lastLineTop.toFixed(1)}, ${m.lastLineBottom.toFixed(1)}]`,
        );
      }
      if (!m.withinRow) {
        failures.push(
          `${where}: chevron right ${m.chevronRight.toFixed(1)} escapes the row's measure (${m.rowRight.toFixed(1)}); the one-line case overflows`,
        );
      }
      continue;
    }
    for (const m of fixture.widths) {
      if (!m.sharesLastLine) {
        failures.push(
          `${where}@${m.width}px: chevron [${m.chevronTop.toFixed(1)}, ${m.chevronBottom.toFixed(1)}] stranded below the intent's last text line [${m.lastLineTop.toFixed(1)}, ${m.lastLineBottom.toFixed(1)}] (probe slack ${m.probeSlack.toFixed(1)}px, ${m.lineCount} lines)`,
        );
      }
      if (!m.withinRow) {
        failures.push(
          `${where}@${m.width}px: chevron right ${m.chevronRight.toFixed(1)} escapes the row's measure (${m.rowRight.toFixed(1)})`,
        );
      }
    }
    if (!fixture.bindingFound) {
      const smallest = Math.min(...fixture.widths.map((m) => m.probeSlack));
      failures.push(
        `${where}: no swept width reached the binding geometry (probe last line with < 18px slack; smallest was ${smallest.toFixed(1)}px across ${fixture.widths.length} widths); the sweep no longer exercises the stranding case`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.slice(0, 12).join("; ") };

  const summary = measurement
    .filter((f) => f.sweep)
    .map(
      (f) =>
        `${f.id}: ${f.widths.length} widths all inline, first binding at ${f.binding.width}px (probe slack ${f.binding.probeSlack.toFixed(1)}px, ${f.binding.probeLineCount} lines)`,
    )
    .join(" | ");
  return {
    pass: true,
    reason: `${summary} | short-inline: 1 line, chevron inline`,
  };
}
