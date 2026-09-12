// The wide-measure stamp contract (PR 1041): with the 64rem column the
// 55-75rem band has no real margin, so the override must keep the stamp IN
// FLOW with wrapping restored (white-space: normal - the rail's nowrap is
// not inert on a static box), and the rail may only engage from a 75rem
// (1200px) container. Italic in both regimes. Measured before the override
// reverted wrapping: the in-flow stamp in the band kept the rail's nowrap.
const RAIL_MIN = 1200;

export default function assert(measurements) {
  const failures = [];
  const reasons = [];
  for (const m of measurements) {
    const label = `${m.width}px container`;
    const expectRail = m.width >= RAIL_MIN;
    if (expectRail) {
      if (m.position !== "absolute") {
        failures.push(
          `${label}: stamp is in flow (${m.position}) but a 75rem container has room for the rail even at the wide measure - the 55rem rail rule is missing`,
        );
        continue;
      }
      if (!m.outsideRight) {
        failures.push(`${label}: rail stamp is not fully right of the column`);
      }
      if (!m.railAligned) {
        failures.push(`${label}: rail stamp is not aligned with the first prose line`);
      }
      if (m.whiteSpace !== "nowrap") {
        failures.push(`${label}: rail stamp lost its nowrap (${m.whiteSpace})`);
      }
    } else {
      if (m.position !== "static") {
        failures.push(
          `${label}: stamp left the flow (${m.position}) inside the wide-measure revert band - the 55-75rem override's position: static is missing`,
        );
        continue;
      }
      if (m.whiteSpace !== "normal") {
        failures.push(
          `${label}: in-flow band stamp keeps the rail's ${m.whiteSpace} - the override must revert to white-space: normal`,
        );
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
    reasons.push(`${label}: ${m.position}, ${expectRail ? "rail" : "band in-flow"} regime`);
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return { pass: true, reason: reasons.join("; ") };
}
