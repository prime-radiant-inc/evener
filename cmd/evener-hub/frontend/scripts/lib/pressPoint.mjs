// Runs in the page. Scrolls el into view and returns its center while el is
// topmost there, else null: a press at a point that something else covers
// would land on that something, as a real press would.
//
// A zero-area element has no pressable center: elementFromPoint can still
// report a point on its boundary, and the press would land on whatever holds
// that pixel (#7314).
//
// skillguard interpolates this function into its one-page-turn press
// expressions, and cascadeguard's pressRowExpr does the same, so the source
// their regression tests import is the source the page runs.
export function uncoveredCenter(el, block) {
  el.scrollIntoView({ block });
  const r = el.getBoundingClientRect();
  if (r.width <= 0 || r.height <= 0) return null;
  const x = r.x + r.width / 2;
  const y = r.y + r.height / 2;
  return el.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
}
