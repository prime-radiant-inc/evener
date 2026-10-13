// ListRow's label is the row's identity. At phone widths two state chips
// beside a long label must not squeeze it below a readable floor (10ch of the
// label's own font); the chips wrap under it instead. Whatever wraps, nothing
// may spill out of the row, overlap the label, or break inside a chip's
// fixed-height pill.
const TOLERANCE = 1;
// The heading's column gap is --space-2; anything past twice that means the
// label stretched and pushed its chips away from its text.
const MAX_BESIDE_GAP = 16;

function overlaps(a, b) {
  return a.left < b.right - TOLERANCE && b.left < a.right - TOLERANCE && a.top < b.bottom - TOLERANCE && b.top < a.bottom - TOLERANCE;
}

export default function assert(measurements) {
  const failures = [];

  for (const m of measurements) {
    const tag = `${m.width}px${m.short ? " short label" : ""}`;
    if (m.short) {
      // A short label's chips stay on its line, right after its text: the
      // label grows only up to its own width, never pushing them away.
      const first = m.chips[0];
      if (first === undefined || first.rect.top >= m.label.bottom - TOLERANCE) {
        failures.push(`${tag}: the chips wrapped off the label's line though both fit`);
      } else if (first.rect.left - m.label.right > MAX_BESIDE_GAP) {
        failures.push(
          `${tag}: the first chip sits ${(first.rect.left - m.label.right).toFixed(1)}px past the label's text, not beside it`,
        );
      }
    } else if (m.label.width + TOLERANCE < m.readableFloor) {
      failures.push(
        `${tag}: label squeezed to ${m.label.width.toFixed(1)}px, under the ${m.readableFloor.toFixed(1)}px (10ch) readable floor`,
      );
    }
    if (m.row.scrollWidth > m.row.clientWidth + TOLERANCE) {
      failures.push(`${tag}: row content is ${m.row.scrollWidth}px wide in a ${m.row.clientWidth}px row`);
    }
    m.chips.forEach((chip, index) => {
      const name = `chip ${index + 1}`;
      if (chip.rect.right > m.row.rect.right + TOLERANCE || chip.rect.left < m.row.rect.left - TOLERANCE) {
        failures.push(`${tag}: ${name} spills out of the row horizontally`);
      }
      if (chip.rect.bottom > m.row.rect.bottom + TOLERANCE) {
        failures.push(`${tag}: ${name} spills below the row`);
      }
      if (overlaps(chip.rect, m.label)) {
        failures.push(`${tag}: ${name} overlaps the label`);
      }
      if (chip.scrollHeight > chip.clientHeight + TOLERANCE) {
        failures.push(`${tag}: ${name}'s text wrapped inside its fixed-height pill`);
      }
    });
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `a long label keeps at least 10ch, a short one keeps its chips beside it, and the chips stay inside the row (${measurements.map((m) => `${m.width}px${m.short ? " short" : ""}: label ${m.label.width.toFixed(0)}px`).join(", ")})`,
      }
    : { pass: false, reason: failures.join("; ") };
}
