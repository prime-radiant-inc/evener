// Geometric contract for the questions widget as the transcript's trailing
// row (see case.json's description):
//
//   A. The outer document never scrolls, short or tall question.
//   B. The transcript (.body) owns the scrolling: its client height and the
//      footer's box are IDENTICAL whether the batch is short or tall - a
//      pending ask rides inside the scroll region and can neither squeeze
//      nor grow the pane's allocations.
//   C. The dock sizes to its content: even with a tall batch its own box
//      shows no internal scroll (scrollHeight ~= clientHeight, overflow-y
//      visible). An internal scrollbar here would ALSO lie to the virtual
//      list's measureElement in the real mount.
//   D. A taller batch extends the transcript's scroll height - that, not
//      any dock containment, is how a tall batch stays reachable.
//   E. The dock centers on the 76rem reading measure: at this viewport its
//      left and right margins inside the body are equal.
//   F. The dock comes after the last turn row in scroll order.
export default function assert(m) {
  const failures = [];
  const tolerance = 1;
  const { short, tall } = m;

  if (m.documentHeight > m.viewportHeight + tolerance) {
    failures.push(`outer document scrolls: ${m.documentHeight}px document in ${m.viewportHeight}px viewport`);
  }

  if (Math.abs(tall.body.clientHeight - short.body.clientHeight) > tolerance) {
    failures.push(
      `a tall question changed the transcript's allocation (${short.body.clientHeight}px -> ${tall.body.clientHeight}px)`,
    );
  }
  if (Math.abs(tall.footer.top - short.footer.top) > tolerance) {
    failures.push(`a tall question moved the footer (${short.footer.top}px -> ${tall.footer.top}px)`);
  }
  if (short.body.overflowY !== "auto" && short.body.overflowY !== "scroll") {
    failures.push(`transcript overflow belongs to ${short.body.overflowY}, not the pane body`);
  }

  if (tall.dock.scrollHeight > tall.dock.clientHeight + tolerance) {
    failures.push(
      `tall question clipped inside the dock (${tall.dock.scrollHeight}px content in ${tall.dock.clientHeight}px box) - the dock must size to content and let the transcript scroll`,
    );
  }
  if (tall.dock.overflowY !== "visible") {
    failures.push(`dock carries its own overflow-y: ${tall.dock.overflowY} - scrolling belongs to the transcript`);
  }
  if (tall.body.scrollHeight <= short.body.scrollHeight + 1000) {
    failures.push(
      `growing the question 180px -> 1200px barely grew the transcript (${short.body.scrollHeight}px -> ${tall.body.scrollHeight}px)`,
    );
  }

  // Centering is judged inside the body's SCROLLABLE content column: a
  // vertical scrollbar (overflow-y: auto) sits between the content and the
  // body's right padding edge, so the right margin includes the scrollbar
  // width. The .turn rows center within the same column, so the dock
  // aligning here is what aligns it with the conversation.
  const bodyContentLeft = short.body.left + 16; // .body's --space-4 padding
  const bodyContentRight = short.body.right - 16 - short.body.scrollbarWidth;
  const leftMargin = short.dock.left - bodyContentLeft;
  const rightMargin = bodyContentRight - short.dock.right;
  if (leftMargin < -tolerance || rightMargin < -tolerance) {
    failures.push(`dock escapes the body's content column (${short.dock.left}..${short.dock.right})`);
  }
  if (Math.abs(leftMargin - rightMargin) > tolerance) {
    failures.push(`dock is not centered on the reading measure (margins ${leftMargin.toFixed(1)}px / ${rightMargin.toFixed(1)}px)`);
  }

  if (short.dock.top < short.lastTurn.top + tolerance) {
    failures.push(`dock does not follow the last turn row (dock top ${short.dock.top}px, last turn top ${short.lastTurn.top}px)`);
  }

  return failures.length === 0
    ? { pass: true, reason: "the questions widget is a content-sized, centered transcript row; the transcript owns all scrolling" }
    : { pass: false, reason: failures.join("; ") };
}
