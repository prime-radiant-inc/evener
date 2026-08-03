export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

  for (const measurement of measurements) {
    const fixture = measurement.fixture;
    const paneRoles = measurement.visiblePaneRoles.join(", ") || "none";

    if (measurement.mode === "desktop") {
      if (measurement.visiblePaneCount !== 2) {
        failures.push(`${fixture}: desktop master-detail shows ${measurement.visiblePaneCount} visible panes (${paneRoles}), expected exactly 2 panes`);
      }
      if (!measurement.treePane) failures.push(`${fixture}: missing tree pane`);
      if (!measurement.inspectorPane) failures.push(`${fixture}: missing inspector pane`);
    }

    if (measurement.mode === "mobile-tree" || measurement.mode === "mobile-inspector") {
      if (measurement.visiblePaneCount !== 1) {
        failures.push(`${fixture}: mobile layout shows ${measurement.visiblePaneCount} visible panes (${paneRoles}), expected exactly 1 readable pane`);
      }
    }

    if (measurement.treePane) {
      if (measurement.treePane.overflowX !== "hidden") {
        failures.push(`${fixture}: tree pane overflow-x is ${JSON.stringify(measurement.treePane.overflowX)}, expected "hidden"`);
      }
      if (measurement.treePane.scrollWidth > measurement.treePane.clientWidth + tolerance) {
        failures.push(
          `${fixture}: tree pane scrollWidth ${measurement.treePane.scrollWidth}px exceeds clientWidth ${measurement.treePane.clientWidth}px - tree pane must not scroll sideways`,
        );
      }
    }

    if (measurement.outputPre) {
      if (!/auto|scroll/.test(measurement.outputPre.overflowX)) {
        failures.push(`${fixture}: output pre overflow-x is ${JSON.stringify(measurement.outputPre.overflowX)}, expected auto or scroll`);
      }
      if (measurement.outputPre.scrollWidth <= measurement.outputPre.clientWidth + tolerance) {
        failures.push(
          `${fixture}: output pre scrollWidth ${measurement.outputPre.scrollWidth}px does not exceed clientWidth ${measurement.outputPre.clientWidth}px - the output pre is not the intended horizontal scroller`,
        );
      }
    } else if (measurement.mode === "desktop" || measurement.mode === "mobile-inspector") {
      failures.push(`${fixture}: missing output pre`);
    }

    for (const [name, textBox] of [
      ["deep label", measurement.deepLabel],
      ["deep detail", measurement.deepDetail],
    ]) {
      if (!textBox) {
        failures.push(`${fixture}: missing ${name} probe`);
        continue;
      }
      if (textBox.whiteSpace !== "nowrap") {
        failures.push(`${fixture}: ${name} white-space is ${JSON.stringify(textBox.whiteSpace)}, expected "nowrap" for single-line clipping`);
      }
      if (textBox.textOverflow !== "ellipsis") {
        failures.push(`${fixture}: ${name} text-overflow is ${JSON.stringify(textBox.textOverflow)}, expected "ellipsis"`);
      }
      if (textBox.scrollWidth <= textBox.clientWidth + tolerance) {
        failures.push(
          `${fixture}: ${name} scrollWidth ${textBox.scrollWidth}px does not exceed clientWidth ${textBox.clientWidth}px - the probe text is not actually clipped`,
        );
      }
    }

    if (measurement.mobileBack) {
      if (measurement.mobileBack.left < -tolerance || measurement.mobileBack.right > measurement.viewport.width + tolerance) {
        failures.push(
          `${fixture}: mobile Back control escapes viewport horizontally (${measurement.mobileBack.left.toFixed(1)}..${measurement.mobileBack.right.toFixed(1)} in ${measurement.viewport.width}px viewport)`,
        );
      }
      if (measurement.mobileBack.top < -tolerance || measurement.mobileBack.bottom > measurement.viewport.height + tolerance) {
        failures.push(
          `${fixture}: mobile Back control escapes viewport vertically (${measurement.mobileBack.top.toFixed(1)}..${measurement.mobileBack.bottom.toFixed(1)} in ${measurement.viewport.height}px viewport)`,
        );
      }
    }
  }

  return failures.length === 0
    ? {
        pass: true,
        reason:
          "desktop keeps exactly two visible panes, mobile variants keep exactly one readable pane, the tree pane never scrolls sideways, deep labels clip with ellipsis, only the output pre scrolls horizontally, and the mobile Back control stays inside the viewport",
      }
    : {
        pass: false,
        reason: failures.join("; "),
      };
}
