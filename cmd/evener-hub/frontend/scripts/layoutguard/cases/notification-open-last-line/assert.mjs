const ADJACENCY_SLACK_PX = 4;
const CENTER_SLACK_PX = 1;

export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    if (fixture.lineCount < 2) {
      failures.push(
        `${fixture.mode}: secondary rendered only ${fixture.lineCount} line; fixture no longer exercises wrapping`,
      );
    }
    if (!fixture.sharesLastLine) {
      failures.push(
        `${fixture.mode}: Open [${fixture.open.top.toFixed(1)}, ${fixture.open.bottom.toFixed(1)}] does not overlap the secondary's last line [${fixture.lastLine.top.toFixed(1)}, ${fixture.lastLine.bottom.toFixed(1)}]; it stranded on its own line`,
      );
    }
    if (fixture.textToOpenGap < -1 || fixture.textToOpenGap > fixture.gap + ADJACENCY_SLACK_PX) {
      failures.push(
        `${fixture.mode}: Open is ${fixture.textToOpenGap.toFixed(1)}px after the final text edge, expected 0..${(fixture.gap + ADJACENCY_SLACK_PX).toFixed(1)}px; it is not adjacent to the item it opens`,
      );
    }
    for (const [name, slot] of [
      ["Open", fixture.open],
      ["chevron", fixture.chevron],
    ]) {
      const slotCenter = (slot.top + slot.bottom) / 2;
      const lineCenter = (fixture.lastLine.top + fixture.lastLine.bottom) / 2;
      const delta = slotCenter - lineCenter;
      if (Math.abs(delta) > CENTER_SLACK_PX) {
        failures.push(
          `${fixture.mode}: ${name} center sits ${delta.toFixed(1)}px off the final text line's center (limit ${CENTER_SLACK_PX}px); the trailing slot no longer centers on the line (half-leading regression)`,
        );
      }
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: measurement
      .map(
        (f) =>
          `${f.mode} ${f.viewportWidth}px: ${f.lineCount} secondary lines, Open overlaps last line, follows its text edge by ${f.textToOpenGap.toFixed(1)}px, and centers on it`,
      )
      .join(" | "),
  };
}
