// The transcript's icon-rail seats, held to one column (2026-09-20 rail pass):
//
// 1. Above the 700px breakpoint every run row's leading glyph - the live
//    thinking row's bulb, the content-free thinking row's Loader grid, and
//    the notification head's status glyph - seats in the SAME avatar column:
//    each 24px slot starts at the turn's left edge (one --speaker-gutter out
//    of .runContent's reserved padding, via each row's own pull), and every
//    row's text starts at the content edge regardless of which row it is.
// 2. The slot-to-text gap is the speaker gap on every row, even though the
//    three rows reach it by three mechanisms (the think row's .icon
//    margin-right with no flex gap; the Loader's grid margin-right plus the
//    row's own --space-2 gap; the notification head's slot margin-right plus
//    the head's --space-2 gap) - the pull arithmetic must stay netted to the
//    same 10px or one row's text drifts off its siblings'.
// 3. Below the breakpoint there is no gutter and no pull: every slot leads
//    its line INLINE at the content edge, and the text follows one full
//    gutter later (slot + margin + gap, 34px) - the same phone-width
//    arithmetic ToolRow's .rowIcon documents.
// 4. No seat ever escapes the turn's own box (a pull applied below the
//    breakpoint, or without runContent's padding, would push the glyph out
//    of the pane).
//
// 1px tolerances, not 0, to stay clear of sub-pixel rounding noise.
export default function assert(measurement) {
  // Both regime frames must be present and must be the regimes they claim:
  // a silently-dead iframe would otherwise pass the case measuring only the
  // surviving half.
  const regimes = measurement.map((m) => `${m.mode}:${m.viewportWidth}`).sort();
  // sort() is alphabetical: "desktop:920" sorts before "phone:390".
  if (regimes.length !== 2 || regimes[0] !== "desktop:920" || regimes[1] !== "phone:390") {
    return { pass: false, reason: `expected the phone(390) and desktop(920) regime frames, measured [${regimes.join(", ")}] - an iframe failed to load or render` };
  }
  for (const m of measurement) {
    const { mode, turn, probe, think, loader, notification } = m;
    // One row per seat, slot and text together: the pairing is structural,
    // not positional, so adding a fourth row is a one-line change.
    const rows = [
      { row: "think", slot: think.slot, text: think.text },
      { row: "loader", slot: loader.slot, text: loader.text },
      { row: "notification", slot: notification.slot, text: notification.text },
    ];

    // No seat escapes the turn's box, in either regime.
    for (const { row, slot } of rows) {
      if (slot.left < turn.left - 1) {
        return { pass: false, reason: `${mode}: the ${row} row's slot escapes the turn's box (slot.left=${slot.left.toFixed(1)} < turn.left=${turn.left.toFixed(1)}) - a pull is eating padding that does not exist` };
      }
    }

    if (mode === "desktop") {
      // The rail: every slot starts at the turn's left edge.
      for (const { row, slot } of rows) {
        if (Math.abs(slot.left - turn.left) > 1) {
          return { pass: false, reason: `${mode}: the ${row} row's slot does not sit at the rail's origin (slot.left=${slot.left.toFixed(1)}, turn.left=${turn.left.toFixed(1)}) - its gutter pull is off` };
        }
      }
      // One column: all three slot centres coincide.
      const centres = rows.map((r) => r.slot.left + r.slot.width / 2);
      const spread = Math.max(...centres) - Math.min(...centres);
      if (spread > 1) {
        return { pass: false, reason: `${mode}: the three icon seats do not share one avatar column (slot centres ${centres.map((c) => c.toFixed(1)).join(", ")}, spread ${spread.toFixed(1)}px)` };
      }
      // Text at the content edge on every row.
      for (const { row, text } of rows) {
        if (Math.abs(text.left - probe.left) > 1) {
          return { pass: false, reason: `${mode}: the ${row} row's text starts at ${text.left.toFixed(1)}, not the content edge ${probe.left.toFixed(1)} - its pull/slot arithmetic has drifted` };
        }
      }
      // The slot-to-text gap is the speaker gap (10px) on every row.
      for (const { row, slot, text } of rows) {
        const gap = text.left - slot.right;
        if (gap < 9 || gap > 11) {
          return { pass: false, reason: `${mode}: the ${row} row's slot-to-text gap is ${gap.toFixed(1)}px, not the speaker gap's 10px - the margin-right/gap netting is off` };
        }
      }
    } else {
      // Phone: no pull. Every slot leads its line inline at the content edge.
      for (const { row, slot } of rows) {
        if (Math.abs(slot.left - probe.left) > 1) {
          return { pass: false, reason: `${mode}: the ${row} row's slot is not leading the line inline at the content edge (slot.left=${slot.left.toFixed(1)}, content edge=${probe.left.toFixed(1)}) - the gutter pull must not apply below the breakpoint` };
        }
      }
      // The text follows one full gutter (slot + margin + gap = 34px).
      for (const { row, slot, text } of rows) {
        const lead = text.left - slot.left;
        if (Math.abs(lead - 34) > 1) {
          return { pass: false, reason: `${mode}: the ${row} row's text leads its slot by ${lead.toFixed(1)}px, not the one-gutter 34px (slot 24px + margin + gap)` };
        }
      }
    }
  }

  return {
    pass: true,
    reason: "all three rail seats share the avatar column above the breakpoint with their text at the content edge and a 10px speaker gap, lead inline below it, and never escape the turn's box",
  };
}
