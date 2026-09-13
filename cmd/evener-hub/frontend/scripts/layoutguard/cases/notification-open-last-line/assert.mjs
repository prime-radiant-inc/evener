const ADJACENCY_SLACK_PX = 4;
const CENTER_SLACK_PX = 1;

function centerOf(box) {
  return (box.top + box.bottom) / 2;
}

// Every summary shape holds its trailing slots to the same contract: they ride
// the final text line (never a line of their own), they hug the text they
// open, and they center on that line - the flex-end cross alignment pins
// their 1lh wrappers to the line's end, and the glyph inside must not ride
// half-leading below the words. The Open control only exists on the
// secondary-with-open shape; the chevron exists on all three.
export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    const where = `${fixture.mode}/${fixture.shape}`;
    if (fixture.lineCount < 2) {
      failures.push(`${where}: tail text rendered only ${fixture.lineCount} line; fixture no longer exercises wrapping`);
    }
    const lineCenter = centerOf(fixture.lastLine);

    const trailingSlots = fixture.open
      ? [
          ["Open", fixture.open, fixture.textToOpenGap, fixture.openSharesLastLine],
          ["chevron", fixture.chevron, fixture.textToChevronGap, fixture.sharesLastLine],
        ]
      : [["chevron", fixture.chevron, fixture.textToChevronGap, fixture.sharesLastLine]];

    for (const [name, slot, gapAfterText, sharesLastLine] of trailingSlots) {
      if (!sharesLastLine) {
        failures.push(
          `${where}: ${name} [${slot.top.toFixed(1)}, ${slot.bottom.toFixed(1)}] does not overlap the final text line [${fixture.lastLine.top.toFixed(1)}, ${fixture.lastLine.bottom.toFixed(1)}]; it stranded on its own line`,
        );
      }
      if (gapAfterText < -1 || gapAfterText > fixture.gap + ADJACENCY_SLACK_PX) {
        failures.push(
          `${where}: ${name} is ${gapAfterText.toFixed(1)}px after the final text edge, expected 0..${(fixture.gap + ADJACENCY_SLACK_PX).toFixed(1)}px; it is not adjacent to the item it opens`,
        );
      }
      const delta = centerOf(slot) - lineCenter;
      if (Math.abs(delta) > CENTER_SLACK_PX) {
        failures.push(
          `${where}: ${name} center sits ${delta.toFixed(1)}px off the final text line's center (limit ${CENTER_SLACK_PX}px); the trailing slot no longer centers on the line (half-leading regression)`,
        );
      }
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: measurement
      .map((f) => `${f.mode}/${f.shape} ${f.viewportWidth}px: ${f.lineCount} tail lines, trailing slots share, hug, and center on the last`)
      .join(" | "),
  };
}
