// The invariant from the bug report (screenshot: three read/grep rows whose
// trailing disclosure arrows sat alone on their own wrapped lines): a
// trailing glyph may never wrap to a line by itself. It holds at BOTH of
// ToolRow's glyph surfaces - the intent line (chevron glued to the intent's
// final word) and the two-level expanded summary line (the "Open beside"
// control and the body chevron glued to the summary text's final word; the
// read_file anchor shape glues the chevron to its meta segment's final word).
//
// Each swept fixture renders the production row PLUS an unglued probe row
// (the pre-fix markup) and walks a width ladder, because whether a text's
// last line lands within a glyph unit of the edge is a property of font
// metrics the harness must not guess. Two consequences, both asserted:
//
//   1. EVERY swept width must satisfy the invariant for EVERY production
//      glyph - the non-binding widths are the no-regression half (glyphs
//      stay inline when there IS room).
//   2. EVERY swept fixture's probe must STRAND at least once across the
//      sweep: the unglued markup failing at this font is what proves the
//      sweep exercises the stranding geometry. A probe that never strands
//      means the fixture passes vacuously - a guard that can no longer see
//      the defect - and that is a failure, not a pass.

export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    if (fixture.kind === "short") {
      const m = fixture.fixed;
      if (m.lineCount !== 1) {
        failures.push(
          `${fixture.id}: short intent rendered ${m.lineCount} lines; fixture no longer exercises the one-line case`,
        );
      }
      const chevron = m.production[0];
      if (!chevron.sharesLastLine) {
        failures.push(
          `${fixture.id}: chevron [${chevron.glyphTop.toFixed(1)}, ${chevron.glyphBottom.toFixed(1)}] does not share the intent's single line [${chevron.lineTop.toFixed(1)}, ${chevron.lineBottom.toFixed(1)}]`,
        );
      }
      continue;
    }
    for (const m of fixture.widths) {
      for (const g of m.production) {
        if (!g.sharesLastLine) {
          failures.push(
            `${fixture.id}@${m.width}px: ${g.glyph} [${g.glyphTop.toFixed(1)}, ${g.glyphBottom.toFixed(1)}] stranded below its reference text's last line [${g.lineTop.toFixed(1)}, ${g.lineBottom.toFixed(1)}]`,
          );
        }
      }
    }
    if (fixture.binding === null) {
      failures.push(
        `${fixture.id}: the unglued probe never stranded across ${fixture.widths.length} swept widths; the sweep no longer exercises the stranding geometry at this font`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.slice(0, 12).join("; ") };

  const summary = measurement
    .filter((f) => f.widths)
    .map(
      (f) =>
        `${f.id}: ${f.widths.length} widths all inline (probe strands at ${f.binding.width}px, ${f.binding.probe.filter((p) => !p.sharesLastLine).length} glyph(s))`,
    )
    .join(" | ");
  return {
    pass: true,
    reason: `${summary} | short-inline: 1 line, chevron inline`,
  };
}
