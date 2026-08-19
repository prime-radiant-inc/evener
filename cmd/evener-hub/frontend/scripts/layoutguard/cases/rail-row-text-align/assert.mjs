// The rail's text-alignment contract (2026-08-09 sidebar pass):
//
// 1. A dotted row's title starts at the SAME x as a quiet row's - the signal
//    dot outdents into the row's leading padding (.signal's negative
//    margin-left exactly cancels the dot's width plus the title line's gap),
//    so state never shifts text. Drop the margin and the dotted title moves
//    one dot-column right.
// 2. The dot hangs fully INSIDE its row's box, left of the label - .row's
//    padding-left is its home. Drop that padding and the dot escapes the
//    row's left edge.
// 3. The inactive fold's label lines up with its sibling session row's label
//    (one x per nesting depth, no per-row-type offset).
// 4. A branch row's chevron TRAILS its label, one title-line gap after the
//    text - never a reserved leading gutter.
//
// 1px tolerances, not 0, to stay clear of sub-pixel rounding noise.
export default function assert(measurement) {
  const { dottedLabel, quietLabel, signal, dottedTreeitem, chevron, childLabel, foldLabel } = measurement;

  const textDrift = dottedLabel.left - quietLabel.left;
  if (Math.abs(textDrift) > 1) {
    return {
      pass: false,
      reason: `dotted row's title sits ${textDrift.toFixed(1)}px from a quiet row's (dotted.left=${dottedLabel.left.toFixed(1)}, quiet.left=${quietLabel.left.toFixed(1)}) - .signal's outdent margin is off`,
    };
  }

  const dotGap = dottedLabel.left - signal.right;
  if (dotGap < 0 || signal.left < dottedTreeitem.left) {
    return {
      pass: false,
      reason: `signal dot is not outdented cleanly inside the row (dot=[${signal.left.toFixed(1)}, ${signal.right.toFixed(1)}], label.left=${dottedLabel.left.toFixed(1)}, row.left=${dottedTreeitem.left.toFixed(1)}) - .row's leading padding or .signal's margin is off`,
    };
  }

  const foldDrift = foldLabel.left - childLabel.left;
  if (Math.abs(foldDrift) > 1) {
    return {
      pass: false,
      reason: `fold label sits ${foldDrift.toFixed(1)}px from its sibling session row's label (fold.left=${foldLabel.left.toFixed(1)}, child.left=${childLabel.left.toFixed(1)}) - one x per nesting depth is broken`,
    };
  }

  const chevronGap = chevron.left - dottedLabel.right;
  if (chevronGap < 1 || chevronGap > 8) {
    return {
      pass: false,
      reason: `chevron does not trail the label text (chevron.left=${chevron.left.toFixed(1)}, label.right=${dottedLabel.right.toFixed(1)}, gap=${chevronGap.toFixed(1)}px - want one title-line gap, ~4px)`,
    };
  }

  return {
    pass: true,
    reason: `text x holds dotted-vs-quiet (${textDrift.toFixed(1)}px drift), the dot hangs inside the row ${dotGap.toFixed(1)}px left of the title, the fold aligns with its siblings (${foldDrift.toFixed(1)}px drift), and the chevron trails the label by ${chevronGap.toFixed(1)}px`,
  };
}
