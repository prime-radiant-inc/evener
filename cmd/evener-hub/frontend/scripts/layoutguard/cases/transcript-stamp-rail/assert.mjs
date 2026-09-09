// The stamp geometry contract (PR 1041): a mid-exchange agent timestamp is
// a right-margin note only when the transcript container has room for it.
// At or above the 55rem (880px) container floor it leaves the flow entirely
// - absolute, fully right of the column, aligned with the first prose line,
// adding no height. Below the floor (a phone, a narrow dock split) it stays
// a readable in-flow line: right-justified inside the column, snug under
// the content. Italic in both regimes. Measured before the container query
// existed: at a 500px pane in a 1440px window the stamp left the flow on a
// viewport media query and overflow-x: clip cut it away.
const RAIL_MIN = 880;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px container`;
    const expectRail = m.width >= RAIL_MIN;
    if (expectRail) {
      if (m.position !== "absolute") {
        failures.push(
          `${label}: stamp is in flow (${m.position}) but the container has room for the rail - the @container (min-width: 55rem) rule or .message's containing block is missing`,
        );
        continue;
      }
      if (!m.outsideRight) {
        failures.push(`${label}: rail stamp is not fully right of the column (left edge inside it)`);
      }
      if (!m.railAligned) {
        failures.push(`${label}: rail stamp is not aligned with the first prose line`);
      }
    } else {
      if (m.position !== "static") {
        failures.push(
          `${label}: stamp left the flow (${m.position}) in a container too narrow for the rail - it would clip away under overflow-x: clip`,
        );
        continue;
      }
      if (!m.insideColumn) {
        failures.push(`${label}: in-flow stamp overflows the column's right edge`);
      }
      if (!m.belowBubble) {
        failures.push(`${label}: in-flow stamp is not under the content`);
      }
    }
    if (m.fontStyle !== "italic") {
      failures.push(`${label}: stamp is not italic (${m.fontStyle})`);
    }
    reasons.push(`${label}: ${m.position}, ${expectRail ? "rail" : "in-flow"} regime`);
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
