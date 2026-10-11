// The rail's glyph-grammar contract (RailRow.module.css's own comments on
// .watchGlyph and .hostGlyph): both drawn marks occupy one fixed 13px
// leading box, so a host group row reads as drawn in the same visual
// language as a watch row. The boxes are what the row's label offset and
// the rail's scannability actually ride on - the width value itself is
// implementation detail, the equality is the behavior.
//
// 1px tolerances, not 0, to stay clear of sub-pixel rounding noise.
export default function assert(measurement) {
  const { watchGlyph, hostGlyph } = measurement;

  const widthDrift = hostGlyph.width - watchGlyph.width;
  if (Math.abs(widthDrift) > 1) {
    return {
      pass: false,
      reason: `the host glyph's leading box is ${hostGlyph.width.toFixed(1)}px against the watch glyph's ${watchGlyph.width.toFixed(1)}px (${widthDrift.toFixed(1)}px drift) - .hostGlyph no longer occupies the glyph grammar's box`,
    };
  }

  const heightDrift = hostGlyph.height - watchGlyph.height;
  if (Math.abs(heightDrift) > 1) {
    return {
      pass: false,
      reason: `the host glyph's box is ${hostGlyph.height.toFixed(1)}px tall against the watch glyph's ${watchGlyph.height.toFixed(1)}px (${heightDrift.toFixed(1)}px drift) - .hostGlyph no longer occupies the glyph grammar's box`,
    };
  }

  return {
    pass: true,
    reason: `the host glyph's leading box (${hostGlyph.width.toFixed(1)}x${hostGlyph.height.toFixed(1)}) matches the watch glyph's (${watchGlyph.width.toFixed(1)}x${watchGlyph.height.toFixed(1)}) - one drawn-mark grammar`,
  };
}
