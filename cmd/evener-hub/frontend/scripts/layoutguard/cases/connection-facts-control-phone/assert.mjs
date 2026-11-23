// On a phone an editable connection fact stacks: its label on one line and a
// full-width control below it. A read-only fact keeps PropertyList's compact
// 96px label column beside its value.
const TOLERANCE = 1;

export default function assert(m) {
  const failures = [];
  const aligned = (a, b) => Math.abs(a - b) <= TOLERANCE;

  for (const [name, row] of [
    ["name", m.row],
    ["credential", m.credentialRow],
    ["read-only", m.readonlyRow],
  ]) {
    if (row.right > m.viewportWidth + TOLERANCE) {
      failures.push(`the ${name} row runs ${(row.right - m.viewportWidth).toFixed(1)}px past the viewport`);
    }
  }
  if (m.rowScrollWidth > m.rowClientWidth + TOLERANCE) {
    failures.push(`the name row scrolls horizontally (scrollWidth ${m.rowScrollWidth}px in ${m.rowClientWidth}px)`);
  }
  if (m.credentialRowScrollWidth > m.credentialRowClientWidth + TOLERANCE) {
    failures.push(
      `the credential row scrolls horizontally (scrollWidth ${m.credentialRowScrollWidth}px in ${m.credentialRowClientWidth}px)`,
    );
  }
  if (m.readonlyRowScrollWidth > m.readonlyRowClientWidth + TOLERANCE) {
    failures.push(
      `the read-only row scrolls horizontally (scrollWidth ${m.readonlyRowScrollWidth}px in ${m.readonlyRowClientWidth}px)`,
    );
  }

  if (m.input.top < m.label.bottom - TOLERANCE) {
    failures.push(
      `the name input sits beside its label (input top ${m.input.top.toFixed(1)}px, label bottom ${m.label.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (!aligned(m.input.left, m.label.left)) {
    failures.push(`the name input starts ${(m.input.left - m.label.left).toFixed(1)}px from its label's left edge`);
  }
  if (m.input.width < m.row.width - TOLERANCE) {
    failures.push(
      `the name input is ${m.input.width.toFixed(1)}px wide in a ${m.row.width.toFixed(1)}px row instead of filling it`,
    );
  }

  if (m.credentialInput.top < m.credentialLabel.bottom - TOLERANCE) {
    failures.push(
      `the credential input sits beside its label (input top ${m.credentialInput.top.toFixed(1)}px, label bottom ${m.credentialLabel.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (!aligned(m.credentialInput.left, m.credentialLabel.left)) {
    failures.push(
      `the credential input starts ${(m.credentialInput.left - m.credentialLabel.left).toFixed(1)}px from its label's left edge`,
    );
  }
  if (m.credentialInput.width < m.credentialRow.width - TOLERANCE) {
    failures.push(
      `the credential input is ${m.credentialInput.width.toFixed(1)}px wide in a ${m.credentialRow.width.toFixed(1)}px row instead of filling it`,
    );
  }
  if (m.help.top < m.credentialInput.bottom - TOLERANCE) {
    failures.push(
      `the help sits beside the credential input (help top ${m.help.top.toFixed(1)}px, input bottom ${m.credentialInput.bottom.toFixed(1)}px) instead of below it`,
    );
  }
  if (!aligned(m.help.left, m.credentialLabel.left)) {
    failures.push(`the help starts ${(m.help.left - m.credentialLabel.left).toFixed(1)}px from the label's left edge`);
  }
  if (m.help.right > m.credentialRow.right + TOLERANCE) {
    failures.push(`the help runs ${(m.help.right - m.credentialRow.right).toFixed(1)}px past the row's right edge`);
  }

  if (Math.abs(m.readonlyLabel.width - 96) > TOLERANCE) {
    failures.push(`the read-only label column is ${m.readonlyLabel.width.toFixed(1)}px, expected the compact 96px`);
  }
  if (m.readonlyValue.left < m.readonlyLabel.right - TOLERANCE) {
    failures.push(`the read-only value starts ${(m.readonlyLabel.right - m.readonlyValue.left).toFixed(1)}px before the label column ends`);
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
    reason: `editable facts stack a full-width ${m.input.width.toFixed(1)}px control under their labels, and the read-only fact keeps its ${m.readonlyLabel.width.toFixed(1)}px column`,
  };
}
