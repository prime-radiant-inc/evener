// The reading-measure contract (docs/web-ui/typography-spacing-critique-
// 2026-09-06.md R2): the conversation column is bounded by
// --session-measure (44rem on body, tokens.css), so a plain agent paragraph
// never runs past ~100 characters per line however wide the window is, and
// the column sits centred in the pane rather than pinned to its left edge.
// Measured before the token existed: 149 characters per line at 1440.
const MAX_CHARS_PER_LINE = 100;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.window}px window`;
    if (m.charsPerLine > MAX_CHARS_PER_LINE) {
      failures.push(
        `${label}: paragraph runs ${m.charsPerLine.toFixed(0)} characters per line (${m.lines} lines, turn ${m.turnWidth.toFixed(0)}px wide at ${m.fontSize}) - --session-measure is missing or too wide`,
      );
    }
    // 1px, not 0, to stay clear of sub-pixel centring noise.
    if (Math.abs(m.leftGap - m.rightGap) > 1) {
      failures.push(
        `${label}: column is not centred (left gap ${m.leftGap.toFixed(1)}px, right gap ${m.rightGap.toFixed(1)}px) - .turn lost its margin-inline: auto or its max-width`,
      );
    }
    reasons.push(
      `${label}: ${m.charsPerLine.toFixed(0)} chars/line over ${m.lines} lines, column ${m.turnWidth.toFixed(0)}px centred (${m.leftGap.toFixed(0)}/${m.rightGap.toFixed(0)}px gaps)`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
