// A single-token env-var label must stay inside the settings row's fixed
// label column: it wraps instead of running into the input beside it.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  const lineHeight = Number.parseFloat(m.labelLineHeight);
  if (!Number.isFinite(lineHeight)) {
    return { pass: false, reason: `label line-height is not a number: ${m.labelLineHeight}` };
  }

  if (m.labelScrollWidth > m.labelClientWidth + TOLERANCE) {
    failures.push(
      `the label's glyphs run ${m.labelScrollWidth - m.labelClientWidth}px past its ${m.labelClientWidth}px column instead of wrapping`,
    );
  }
  if (m.rowScrollWidth > m.rowClientWidth + TOLERANCE) {
    failures.push(
      `the settings row scrolls horizontally (scrollWidth ${m.rowScrollWidth}px in clientWidth ${m.rowClientWidth}px) - the label is not contained`,
    );
  }
  // Two-sided: the label must actually wrap, or the containment bounds above
  // would trivially pass for an empty or vanished label.
  if (m.label.height < lineHeight * 1.5) {
    failures.push(
      `the label is ${m.label.height.toFixed(1)}px tall (line height ${lineHeight}px) - it did not wrap inside the column`,
    );
  }
  if (!m.rowColumns.startsWith("104px")) {
    failures.push(`the row's first column is not the real 104px label column: ${m.rowColumns}`);
  }
  if (m.input.left < m.label.right - TOLERANCE) {
    failures.push(
      `the input starts ${(m.label.right - m.input.left).toFixed(1)}px before the label column ends instead of beside it`,
    );
  }
  if (m.input.width < 200) {
    failures.push(`the control lost its 1fr column: only ${m.input.width.toFixed(1)}px wide`);
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the label wraps to ${Math.round(m.label.height / lineHeight)} lines inside its ${m.labelClientWidth}px column and the input keeps ${m.input.width.toFixed(1)}px beside it`,
  };
}
