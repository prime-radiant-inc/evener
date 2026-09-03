// Geometric contract for the questions widget as the transcript's trailing
// row (see case.json's description). The scroll owner under test is the
// REAL one: virtuallist.module.css's .root, nested inside PaneScaffold's
// .body exactly as Session.tsx mounts it - .body's own overflow-y never
// engages in the session pane, so guarding it would prove nothing.
//
//   A. The outer document never scrolls, short or tall question.
//   B. The virtual list's root owns the scrolling: its client height and
//      the footer's box are IDENTICAL whether the batch is short or tall -
//      a pending ask rides inside the scroll region and can neither squeeze
//      nor grow the pane's allocations.
//   C. The dock sizes to its content: even with a tall batch its own box
//      shows no internal scroll (scrollHeight ~= clientHeight, overflow-y
//      visible). An internal scrollbar here would ALSO lie to the virtual
//      list's measureElement in the real mount.
//   D. A taller batch extends the scroll root's scroll height - that, not
//      any dock containment, is how a tall batch stays reachable.
//   E. The dock centers on the 76rem reading measure: at this viewport its
//      left and right margins inside the scrollable content column are
//      equal.
//   F. The dock comes after the last turn row in scroll order.
export default function assert(m) {
  const failures = [];
  const tolerance = 1;
  const { short, tall } = m;

  if (m.documentHeight > m.viewportHeight + tolerance) {
    failures.push(`outer document scrolls: ${m.documentHeight}px document in ${m.viewportHeight}px viewport`);
  }

  if (Math.abs(tall.root.clientHeight - short.root.clientHeight) > tolerance) {
    failures.push(
      `a tall question changed the transcript's allocation (${short.root.clientHeight}px -> ${tall.root.clientHeight}px)`,
    );
  }
  if (Math.abs(tall.footer.top - short.footer.top) > tolerance) {
    failures.push(`a tall question moved the footer (${short.footer.top}px -> ${tall.footer.top}px)`);
  }
  if (short.root.overflowY !== "auto" && short.root.overflowY !== "scroll") {
    failures.push(`transcript overflow belongs to ${short.root.overflowY}, not the virtual list's root`);
  }

  if (tall.dock.scrollHeight > tall.dock.clientHeight + tolerance) {
    failures.push(
      `tall question clipped inside the dock (${tall.dock.scrollHeight}px content in ${tall.dock.clientHeight}px box) - the dock must size to content and let the transcript scroll`,
    );
  }
  if (tall.dock.overflowY !== "visible") {
    failures.push(`dock carries its own overflow-y: ${tall.dock.overflowY} - scrolling belongs to the transcript`);
  }
  if (tall.root.scrollHeight <= short.root.scrollHeight + 1000) {
    failures.push(
      `growing the question 180px -> 1200px barely grew the transcript (${short.root.scrollHeight}px -> ${tall.root.scrollHeight}px)`,
    );
  }

  // Centering is judged inside the scroll root's content column: the
  // body's --space-4 padding bounds it on the left, and the root's own
  // vertical scrollbar sits between the content and the right padding
  // edge. The .turn rows center within the same column, so the dock
  // aligning here is what aligns it with the conversation.
  const contentLeft = short.body.left + 16;
  const contentRight = short.body.right - 16 - short.root.scrollbarWidth;
  const leftMargin = short.dock.left - contentLeft;
  const rightMargin = contentRight - short.dock.right;
  if (leftMargin < -tolerance || rightMargin < -tolerance) {
    failures.push(`dock escapes the transcript's content column (${short.dock.left}..${short.dock.right})`);
  }
  if (Math.abs(leftMargin - rightMargin) > tolerance) {
    failures.push(`dock is not centered on the reading measure (margins ${leftMargin.toFixed(1)}px / ${rightMargin.toFixed(1)}px)`);
  }

  if (short.dock.top < short.lastTurn.top + tolerance) {
    failures.push(`dock does not follow the last turn row (dock top ${short.dock.top}px, last turn top ${short.lastTurn.top}px)`);
  }

  return failures.length === 0
    ? { pass: true, reason: "the questions widget is a content-sized, centered transcript row; the virtual list's root owns all scrolling" }
    : { pass: false, reason: failures.join("; ") };
}
