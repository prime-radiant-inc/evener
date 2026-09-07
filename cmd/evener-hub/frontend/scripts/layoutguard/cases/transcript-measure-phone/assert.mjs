// The phone half of the reading-measure contract (typography-spacing-
// critique-2026-09-06 finding 2): below 700px the agent opener is a grid whose
// prose drops to a full-width second row, so it gets the whole pane rather
// than the column beside the 24px avatar. Measured before the fix: 260px of
// 375 (28 characters a line); measured with the phone block mis-ordered in
// the stylesheet: 187px (19 a line).
const MIN_SPAN = 0.85;
const MIN_CHARS_PER_LINE = 30;

export default function assert(m) {
  const span = m.proseWidth / m.paneInner;
  if (m.viewport > 699) {
    return { pass: false, reason: `harness viewport is ${m.viewport}px; the phone rules need <700px (case.json viewport)` };
  }
  if (span < MIN_SPAN) {
    return {
      pass: false,
      reason: `opener prose spans ${(span * 100).toFixed(0)}% of the pane (${m.proseWidth.toFixed(0)}px of ${m.paneInner}px, starting ${m.proseLeft.toFixed(0)}px in) - the agent opener's phone grid is not applying (block order in agentmessageitem.module.css?)`,
    };
  }
  if (m.charsPerLine < MIN_CHARS_PER_LINE) {
    return { pass: false, reason: `only ${m.charsPerLine.toFixed(0)} characters per line at ${m.fontSize}` };
  }
  return {
    pass: true,
    reason: `opener prose spans ${(span * 100).toFixed(0)}% of the pane, ${m.charsPerLine.toFixed(0)} chars/line over ${m.lines} lines at ${m.fontSize}`,
  };
}
