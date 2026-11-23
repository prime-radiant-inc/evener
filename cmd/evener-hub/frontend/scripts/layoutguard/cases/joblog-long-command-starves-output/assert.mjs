// A job log pane must always keep a real output viewport. The command block is
// flow:none above the virtual list, so an unbounded command - a generator
// script wraps to dozens of lines in a narrow workspace column - takes the
// whole log column, VirtualList's height:100% scroll port measures 0px, and
// @tanstack/virtual-core renders no range at all (outerSize === 0). The reader
// then sees the command and no output, forever.
//
// The measured facts are the two boxes' client heights inside the log column,
// plus the command block's computed overflow-y.
// The bounds are stated as shares of the column rather than as the command
// block's own cap, so the case keeps its meaning if the value is tuned: the
// output keeps at least half, the command never takes more than half. They are
// two-sided (a positive viewport alone would pass a command block that still
// overflows, and a bounded command alone would pass a clipped one) and
// NaN-safe (a collapsed or unloaded stylesheet yields 0/NaN readings that must
// fail, not silently skip a comparison).
const TOLERANCE_PX = 1;
const SIZER_HEIGHT_PX = 80;

export default function assert({ viewportHeight, documentHeight, long, short }) {
  const failures = [];
  const reasons = [];

  // A fixture clipped by the viewport would make every reading meaningless.
  if (documentHeight > viewportHeight + TOLERANCE_PX) {
    failures.push(`fixtures do not fit the ${viewportHeight}px viewport (${documentHeight}px document)`);
  }

  const readings = [
    ["long command", long],
    ["short command", short],
  ];
  const nonFinite = readings.flatMap(([label, reading]) =>
    Object.entries(reading)
      .filter(([, value]) => typeof value === "number" && !Number.isFinite(value))
      .map(([field, value]) => `${label}: ${field} is not a finite number (${value})`),
  );
  if (nonFinite.length > 0) {
    return { pass: false, reason: `${nonFinite.join("; ")} - the stylesheets did not load, so nothing was measured` };
  }

  for (const [label, reading] of readings) {
    if (reading.logClientHeight <= 0) {
      failures.push(
        `${label}: the log column has no height (${reading.logClientHeight}px), so the command and list cannot share it`,
      );
      continue;
    }
    // The failure itself: a zero-height scroll port makes the virtualizer's
    // range null, so not one output row reaches the DOM.
    if (reading.listClientHeight <= 0) {
      failures.push(
        `${label}: the output list's viewport is ${reading.listClientHeight}px - the virtual list renders no rows and the pane shows the command with no output`,
      );
    }
    // Two-sided: the output must keep the majority of the column, and the
    // command must not exceed the same bound.
    if (reading.listClientHeight < reading.logClientHeight / 2 - TOLERANCE_PX) {
      failures.push(
        `${label}: the output list keeps ${reading.listClientHeight}px of the ${reading.logClientHeight}px log column - it must keep at least half`,
      );
    }
    if (reading.commandClientHeight > reading.logClientHeight / 2 + TOLERANCE_PX) {
      failures.push(
        `${label}: the command block is ${reading.commandClientHeight}px of the ${reading.logClientHeight}px log column - it must be bounded to at most half`,
      );
    }
    // All overflow lives inside the log column; the pane's scaffold body must
    // not become the scroller that hides the output below it.
    if (reading.bodyScrollHeight > reading.bodyClientHeight + TOLERANCE_PX) {
      failures.push(
        `${label}: the pane body scrolls (${reading.bodyScrollHeight}px of content in ${reading.bodyClientHeight}px) - the command and output must fit the column`,
      );
    }
    if (Math.abs(reading.sizerHeight - SIZER_HEIGHT_PX) > TOLERANCE_PX) {
      failures.push(
        `${label}: the list's sizer is ${reading.sizerHeight}px where the four 20px rows measure ${SIZER_HEIGHT_PX}px`,
      );
    }
    reasons.push(
      `${label}: list ${reading.listClientHeight}px, command ${reading.commandClientHeight}px of ${reading.logClientHeight}px`,
    );
  }

  // The long command is taller than its block, so the rest stays reachable by
  // scrolling inside the block rather than being clipped away. A capped block
  // with overflow-y:hidden keeps the same box sizes, so the dimension check
  // alone would pass while the overflow is clipped: require a scroller.
  if (long.commandOverflowY !== "auto" && long.commandOverflowY !== "scroll") {
    failures.push(
      `the long command's block is overflow-y:${long.commandOverflowY} - its overflow must scroll, not clip`,
    );
  }
  if (!(long.commandScrollHeight > long.commandClientHeight + TOLERANCE_PX)) {
    failures.push(
      `the long command is not scrollable inside its block (client ${long.commandClientHeight}px, scroll ${long.commandScrollHeight}px) - its overflow must stay reachable`,
    );
  }
  // A command that fits must not be clipped by the same bound.
  if (short.commandScrollHeight > short.commandClientHeight + TOLERANCE_PX) {
    failures.push(
      `the short command is clipped (client ${short.commandClientHeight}px, scroll ${short.commandScrollHeight}px) - the bound must bite only when the command would starve the output`,
    );
  }

  if (failures.length > 0) {
    return {
      pass: false,
      reason: `${failures.join("; ")}; measured long ${JSON.stringify(long)}, short ${JSON.stringify(short)}`,
    };
  }
  return { pass: true, reason: reasons.join(", ") };
}
