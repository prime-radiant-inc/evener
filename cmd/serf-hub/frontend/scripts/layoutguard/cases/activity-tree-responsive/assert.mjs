export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

  function assertNoHorizontalOverflow(fixture, ownerName, box, { expectedOverflowX, allowAutoWhenNotScrollable = false } = {}) {
    if (!box) {
      failures.push(`${fixture}: missing ${ownerName}`);
      return;
    }
    if (expectedOverflowX !== undefined && box.overflowX !== expectedOverflowX) {
      failures.push(`${fixture}: ${ownerName} overflow-x is ${JSON.stringify(box.overflowX)}, expected ${JSON.stringify(expectedOverflowX)}`);
    }
    if (!allowAutoWhenNotScrollable && /auto|scroll/.test(box.overflowX)) {
      failures.push(`${fixture}: ${ownerName} overflow-x is ${JSON.stringify(box.overflowX)} - non-output owners must not be horizontal scrollers`);
    }
    if (box.scrollWidth > box.clientWidth + tolerance) {
      failures.push(
        `${fixture}: ${ownerName} scrollWidth ${box.scrollWidth}px exceeds clientWidth ${box.clientWidth}px - ${ownerName} must not scroll sideways`,
      );
    }
  }

  for (const measurement of measurements) {
    const fixture = measurement.fixture;
    const paneRoles = measurement.visiblePaneRoles.join(", ") || "none";

    if (!measurement.structure?.sheetPresent) failures.push(`${fixture}: missing sheet wrapper`);
    if (!measurement.structure?.sheetBodyPresent) failures.push(`${fixture}: missing sheet body wrapper`);
    if (!measurement.structure?.activityPanelPresent) failures.push(`${fixture}: missing inner Activity panel wrapper`);
    if (!measurement.structure?.primaryLayoutPresent) failures.push(`${fixture}: missing primary layout wrapper (master-detail or mobile pane)`);
    if (measurement.structure?.sheetContainsBody === false) failures.push(`${fixture}: sheet wrapper does not contain the sheet body wrapper`);
    if (measurement.structure?.bodyContainsActivity === false) failures.push(`${fixture}: sheet body wrapper does not contain the inner Activity panel wrapper`);
    if (measurement.structure?.activityContainsPrimaryLayout === false) failures.push(`${fixture}: inner Activity panel wrapper does not contain the primary layout wrapper`);

    for (const [ownerName, box] of [
      ["sheet wrapper", measurement.sheet],
      ["sheet body", measurement.sheetBody],
      ["Activity panel wrapper", measurement.activityPanel],
      ["primary layout wrapper", measurement.primaryLayout],
      ["inspector pane", measurement.inspectorPane],
    ]) {
      if (ownerName === "inspector pane" && !box) continue;
      assertNoHorizontalOverflow(fixture, ownerName, box);
    }

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

    assertNoHorizontalOverflow(fixture, "tree pane", measurement.treePane, { expectedOverflowX: "hidden" });

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
