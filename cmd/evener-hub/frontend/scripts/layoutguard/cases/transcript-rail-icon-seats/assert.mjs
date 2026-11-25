// The transcript's shared icon rail (Option A):
//
// 1. Above the 700px breakpoint every railed transcript row — live thought,
//    compact settled thought, full settled thought, compact live thought
//    Loader, static notification, collapsible notification, steering divider,
//    tool call, folded tool run, and turn failure — seats its leading icon in
//    ONE 0-24px avatar column starting at turn.left, while every row's text
//    starts on the shared 34px content edge (probe.left) with a 10px speaker
//    gap. The tool call's demoted summary line and the collapsible
//    notification's expanded card also align on probe.left.
// 2. Below 700px the gutter collapses: every slot leads its line inline at
//    probe.left and its text starts one full gutter (34px) later.
// 3. No seat ever escapes the turn's box in either regime.
//
// 1px tolerances absorb sub-pixel rounding noise.
export default function assert(measurement) {
  const regimes = measurement.map((m) => `${m.mode}:${m.viewportWidth}`).sort();
  if (regimes.length !== 2 || regimes[0] !== "desktop:920" || regimes[1] !== "phone:390") {
    return {
      pass: false,
      reason: `expected the phone(390) and desktop(920) regime frames, measured [${regimes.join(", ")}] - an iframe failed to load or render`,
    };
  }
  const failures = [];
  for (const m of measurement) {
    const { mode, turn, probe, rows, toolDemoted, notifExpanded } = m;
    if (!rows || rows.length !== 10) {
      failures.push(`${mode}: expected 10 railed rows, measured ${rows?.length ?? 0}`);
      continue;
    }

    // No seat escapes the turn's box in either regime.
    for (const { row, slot } of rows) {
      if (slot.left < turn.left - 1) {
        failures.push(
          `${mode}: the ${row} row's slot escapes the turn's box (slot.left=${slot.left.toFixed(1)} < turn.left=${turn.left.toFixed(1)})`,
        );
      }
    }

    if (mode === "desktop") {
      // Every slot starts at turn.left (0px rail origin).
      for (const { row, slot } of rows) {
        if (Math.abs(slot.left - turn.left) > 1) {
          failures.push(
            `${mode}: the ${row} row's slot starts at ${slot.left.toFixed(1)}, not turn.left ${turn.left.toFixed(1)} - its gutter pull is off`,
          );
        }
      }
      // All 10 icon slots share one centered avatar column.
      const centres = rows.map((r) => r.slot.left + r.slot.width / 2);
      const spread = Math.max(...centres) - Math.min(...centres);
      if (spread > 1) {
        failures.push(`${mode}: the 10 icon seats do not share one avatar column (spread ${spread.toFixed(1)}px)`);
      }
      // Every row's text starts at the shared 34px content edge.
      for (const { row, text } of rows) {
        if (Math.abs(text.left - probe.left) > 1) {
          failures.push(
            `${mode}: the ${row} row's text starts at ${text.left.toFixed(1)}, not the content edge ${probe.left.toFixed(1)}`,
          );
        }
      }
      // Tool call's demoted summary line and collapsible notification's expanded body align on probe.left.
      if (Math.abs(toolDemoted.left - probe.left) > 1) {
        failures.push(
          `${mode}: the tool call's demoted summary starts at ${toolDemoted.left.toFixed(1)}, not the content edge ${probe.left.toFixed(1)}`,
        );
      }
      if (Math.abs(notifExpanded.left - probe.left) > 1) {
        failures.push(
          `${mode}: the collapsible notification's expanded body starts at ${notifExpanded.left.toFixed(1)}, not the content edge ${probe.left.toFixed(1)}`,
        );
      }
      // Slot-to-text gap is the 10px speaker gap on every row.
      for (const { row, slot, text } of rows) {
        const gap = text.left - slot.right;
        if (gap < 9 || gap > 11) {
          failures.push(`${mode}: the ${row} row's slot-to-text gap is ${gap.toFixed(1)}px, expected 10px`);
        }
      }
    } else {
      // Phone: no pull. Every slot leads inline at probe.left, text starts
      // 34px later, and each single-line row's slot stays vertically centred
      // on its text even when Disclosure grows to the 44px phone tap floor.
      for (const { row, slot, text } of rows) {
        if (Math.abs(slot.left - probe.left) > 1) {
          failures.push(
            `${mode}: the ${row} row's slot starts at ${slot.left.toFixed(1)}, not inline at the content edge ${probe.left.toFixed(1)}`,
          );
        }
        const lead = text.left - slot.left;
        if (Math.abs(lead - 34) > 1) {
          failures.push(`${mode}: the ${row} row's text leads its slot by ${lead.toFixed(1)}px, expected 34px`);
        }
        const slotCenterY = slot.top + slot.height / 2;
        const textCenterY = text.top + text.height / 2;
        if (Math.abs(slotCenterY - textCenterY) > 2.5) {
          failures.push(
            `${mode}: the ${row} row's slot centre (${slotCenterY.toFixed(1)}) drifts ${(slotCenterY - textCenterY).toFixed(1)}px from its text centre (${textCenterY.toFixed(1)})`,
          );
        }
      }
      const toolRow = rows.find((r) => r.row === "tool-call");
      if (Math.abs(toolDemoted.left - toolRow.text.left) > 1) {
        failures.push(
          `${mode}: the tool call's demoted summary starts at ${toolDemoted.left.toFixed(1)}, not its rationale edge ${toolRow.text.left.toFixed(1)}`,
        );
      }
      if (toolDemoted.right > turn.right + 1) {
        failures.push(
          `${mode}: the tool call's demoted summary overflows the turn right edge (${toolDemoted.right.toFixed(1)} > ${turn.right.toFixed(1)})`,
        );
      }
      if (Math.abs(notifExpanded.left - probe.left) > 1) {
        failures.push(
          `${mode}: the collapsible notification's expanded body starts at ${notifExpanded.left.toFixed(1)}, not ${probe.left.toFixed(1)}`,
        );
      }
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  return {
    pass: true,
    reason:
      "all 10 transcript rail seats share the 0-24px avatar column on desktop with every row's text, tool summary, and expanded notification body aligned on the 34px content edge (10px speaker gap), lead inline below 700px, and never escape the turn's box",
  };
}
