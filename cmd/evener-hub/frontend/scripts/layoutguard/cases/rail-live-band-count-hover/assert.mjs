// A Live band header has no actions menu to swap into the right slot, so its
// count must stay painted and hit-testable while the header row is hovered.
// The hover-hide rule only applies on a hover-capable desktop pointer, so the
// measurement also proves that gate is active and the row really is hovered.
// Every comparison is two-sided and false on NaN.
export default function assert(m) {
  const { row, count } = m;

  if (m.wide !== true || m.gated < 1) {
    return { pass: false, reason: "harness does not exercise the hover-capable desktop hide rule" };
  }
  if (m.probeHidden !== true) {
    return { pass: false, reason: "the hover-hide rule is not active: a plain right-slot occupant stayed visible under hover" };
  }
  if (m.rowHovered !== true) {
    return { pass: false, reason: "the band header row is not in the forced :hover state" };
  }
  if (!(m.opacity >= 1 && m.opacity <= 1)) {
    return { pass: false, reason: `band count opacity is ${m.opacity} while hovered, not 1` };
  }
  if (m.visibility !== "visible") {
    return { pass: false, reason: `band count visibility is "${m.visibility}" while hovered` };
  }
  if (!(count.width > 0 && count.width <= row.right - row.left)) {
    return { pass: false, reason: `band count width ${count.width}px is outside (0, row width]` };
  }
  if (!(count.right <= row.right + 0.5 && count.left >= row.left)) {
    return {
      pass: false,
      reason: `band count [${count.left}, ${count.right}] is not inside the row [${row.left}, ${row.right}]`,
    };
  }
  return { pass: true, reason: "band count stays visible inside the row's right edge while the band header is hovered" };
}
