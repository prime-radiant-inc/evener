// On a desktop viewport an editable fact's value cell takes the rest of the
// row, so its control reaches the row's right edge. A read-only fact keeps
// PropertyList's compact 96px label column and does not grow.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  const aligned = (a, b) => Math.abs(a - b) <= TOLERANCE;

  for (const [name, row] of [
    ["name", m.nameRow],
    ["base URL", m.baseUrlRow],
    ["read-only", m.readonlyRow],
  ]) {
    if (row.right > m.viewportWidth + TOLERANCE) {
      failures.push(`the ${name} row runs ${(row.right - m.viewportWidth).toFixed(1)}px past the viewport`);
    }
  }
  if (m.nameRowScrollWidth > m.nameRowClientWidth + TOLERANCE) {
    failures.push(`the name row scrolls horizontally (scrollWidth ${m.nameRowScrollWidth}px in ${m.nameRowClientWidth}px)`);
  }
  if (m.baseUrlRowScrollWidth > m.baseUrlRowClientWidth + TOLERANCE) {
    failures.push(
      `the base URL row scrolls horizontally (scrollWidth ${m.baseUrlRowScrollWidth}px in ${m.baseUrlRowClientWidth}px)`,
    );
  }
  if (m.readonlyRowScrollWidth > m.readonlyRowClientWidth + TOLERANCE) {
    failures.push(
      `the read-only row scrolls horizontally (scrollWidth ${m.readonlyRowScrollWidth}px in ${m.readonlyRowClientWidth}px)`,
    );
  }

  for (const [name, label, input, row] of [
    ["name", m.nameLabel, m.nameInput, m.nameRow],
    ["Base URL", m.baseUrlLabel, m.baseUrlInput, m.baseUrlRow],
  ]) {
    if (Math.abs(label.width - 96) > TOLERANCE) {
      failures.push(`the ${name} label column is ${label.width.toFixed(1)}px, expected the compact 96px`);
    }
    if (input.top >= label.bottom - TOLERANCE) {
      failures.push(`the ${name} input stacks below its label at a desktop width instead of staying beside it`);
    }
    if (input.left < label.right - TOLERANCE) {
      failures.push(`the ${name} input starts ${(label.right - input.left).toFixed(1)}px before the label column ends`);
    }
    if (!aligned(input.right, row.right)) {
      failures.push(
        `the ${name} input ends ${(row.right - input.right).toFixed(1)}px short of the row's right edge`,
      );
    }
  }

  if (m.readonlyValue.left < m.readonlyLabel.right - TOLERANCE) {
    failures.push(
      `the read-only value starts ${(m.readonlyLabel.right - m.readonlyValue.left).toFixed(1)}px before the label column ends`,
    );
  }
  if (m.readonlyValue.top >= m.readonlyLabel.bottom - TOLERANCE) {
    failures.push("the read-only value stacks below its label instead of staying beside it");
  }
  if (m.readonlyValue.right > m.readonlyRow.right + TOLERANCE) {
    failures.push(`the read-only value runs ${(m.readonlyValue.right - m.readonlyRow.right).toFixed(1)}px past the row`);
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `editable facts grow to the row's right edge (Base URL input ${m.baseUrlInput.width.toFixed(1)}px in a ${m.baseUrlRow.width.toFixed(1)}px row), while the read-only fact keeps its ${m.readonlyLabel.width.toFixed(1)}px label column`,
  };
}
