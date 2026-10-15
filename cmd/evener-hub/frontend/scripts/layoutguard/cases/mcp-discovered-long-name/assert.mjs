// Geometric contract for Settings -> MCP's discovered-server rows (see
// case.json's description):
//
//   A. The server name is never clipped: the label's content fits its box
//      (scrollWidth <= clientWidth), so the whole name is on screen. A
//      truncated name with only a hover title is unreadable on touch.
//   B. The wrapped name stays inside the row: nothing spills past the row's
//      right edge and the row itself does not scroll horizontally.
export default function assert(fixtures) {
  const failures = [];

  for (const f of fixtures) {
    const at = `${f.width}px ${f.name} name`;
    if (f.label.scrollWidth > f.label.clientWidth + 1) {
      failures.push(`${at}: the name is clipped (${f.label.scrollWidth}px of text in a ${f.label.clientWidth}px label)`);
    }
    if (f.label.rect.right > f.row.rect.right + 1) {
      failures.push(`${at}: the label escapes the row by ${(f.label.rect.right - f.row.rect.right).toFixed(1)}px`);
    }
    if (f.row.scrollWidth > f.row.clientWidth + 1) {
      failures.push(`${at}: the row overflows horizontally (${f.row.scrollWidth}px in ${f.row.clientWidth}px)`);
    }
  }

  return failures.length === 0
    ? { pass: true, reason: "discovered MCP server names wrap in full inside their rows at phone widths" }
    : { pass: false, reason: failures.join("; ") };
}
