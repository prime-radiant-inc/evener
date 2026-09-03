const ADJACENCY_SLACK_PX = 4;

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
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: measurement
      .map(
        (f) =>
          `${f.mode} ${f.viewportWidth}px: ${f.lineCount} secondary lines, Open overlaps last line and follows its text edge by ${f.textToOpenGap.toFixed(1)}px`,
      )
      .join(" | "),
  };
}
