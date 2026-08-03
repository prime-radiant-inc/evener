export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;
  const treeModes = new Set(["desktop", "mobile-tree"]);

  function isWideSheetInlineSize(value) {
    if (typeof value !== "string") return false;
    const normalized = value.replace(/\s+/g, "");
    return /^calc\(\(24rem\*2\)\+(?:var\(--space-3\)|12px)\+\((?:var\(--space-3\)|12px)\*2\)\)$/.test(normalized);
  }

  function measurementSizingForFailure(measurement) {
    const sizing = measurement?.sizing;
    if (!sizing) return "";
    const widest = sizing.treeWidestDescendant
      ? `widest=${sizing.treeWidestDescendant.hint} width=${sizing.treeWidestDescendant.width}px client=${sizing.treeWidestDescendant.clientWidth}px scroll=${sizing.treeWidestDescendant.scrollWidth}px right=${sizing.treeWidestDescendant.rightFromContainerLeft}px`
      : "widest=none";
    return [
      `sheet=${sizing.sheetPanelWidth}px`,
      `body=${sizing.sheetBodyWidth}px`,
      `activity=${sizing.activityPanelWidth}px`,
      `layout=${sizing.primaryLayoutWidth}px`,
      `tree=${sizing.treePaneWidth}px`,
      `inspector=${sizing.inspectorPaneWidth}px`,
      `display=${JSON.stringify(sizing.masterDetailDisplay)}`,
      `grid=${JSON.stringify(sizing.masterDetailGridColumns)}`,
      `treeMinWidth=${JSON.stringify(sizing.treePaneMinWidth)}`,
      `treeFlexBasis=${JSON.stringify(sizing.treePaneFlexBasis)}`,
      `groupPadding=${JSON.stringify(sizing.firstGroupPaddingLeft)}`,
      `rowActionsWrap=${JSON.stringify(sizing.rowActionsFlexWrap)}`,
      widest,
    ].join(", ");
  }

  function assertNoHorizontalOverflow(fixture, ownerName, box, diagnostics, { expectedOverflowX, allowAutoWhenNotScrollable = false } = {}) {
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
        `${fixture}: ${ownerName} scrollWidth ${box.scrollWidth}px exceeds clientWidth ${box.clientWidth}px - ${ownerName} must not scroll sideways${diagnostics ? `; ${diagnostics}` : ""}`,
      );
    }
  }

  for (const measurement of measurements) {
    const fixture = measurement.fixture;
    const paneRoles = measurement.visiblePaneRoles.join(", ") || "none";
    const diagnostics = measurementSizingForFailure(measurement);

    if (!measurement.structure?.sheetPresent) failures.push(`${fixture}: missing sheet wrapper`);
    if (measurement.mode === "desktop" && !measurement.structure?.sheetWideClassPresent) {
      failures.push(`${fixture}: sheet wrapper is missing the real wide class`);
    }
    if (!measurement.structure?.sheetBodyPresent) failures.push(`${fixture}: missing sheet body wrapper`);
    if (!measurement.structure?.activityPanelPresent) failures.push(`${fixture}: missing inner Activity panel wrapper`);
    if (!measurement.structure?.primaryLayoutPresent) failures.push(`${fixture}: missing primary layout wrapper (master-detail or mobile pane)`);
    if ((measurement.mode === "desktop" || measurement.mode === "mobile-inspector") && !measurement.structure?.outputCodeBlockPresent) {
      failures.push(`${fixture}: missing output CodeBlock wrapper`);
    }
    if ((measurement.mode === "desktop" || measurement.mode === "mobile-inspector") && !measurement.structure?.outputCodePrePresent) {
      failures.push(`${fixture}: missing output CodeBlock pre`);
    }
    if (measurement.structure?.sheetContainsBody === false) failures.push(`${fixture}: sheet wrapper does not contain the sheet body wrapper`);
    if (measurement.structure?.bodyContainsActivity === false) failures.push(`${fixture}: sheet body wrapper does not contain the inner Activity panel wrapper`);
    if (measurement.structure?.activityContainsPrimaryLayout === false) failures.push(`${fixture}: inner Activity panel wrapper does not contain the primary layout wrapper`);
    if (measurement.mode === "desktop" && !isWideSheetInlineSize(measurement.sizing?.sheetInlineSizeVar)) {
      failures.push(
        `${fixture}: sheet wrapper --sheet-inline-size is ${JSON.stringify(measurement.sizing?.sheetInlineSizeVar ?? null)}, expected the real wide Sheet value`,
      );
    }

    for (const [ownerName, box] of [
      ["sheet wrapper", measurement.sheet],
      ["sheet body", measurement.sheetBody],
      ["Activity panel wrapper", measurement.activityPanel],
      ["primary layout wrapper", measurement.primaryLayout],
      ["inspector pane", measurement.inspectorPane],
      ["output CodeBlock wrapper", measurement.outputCodeBlock],
      ["output CodeBlock pre", measurement.outputCodePre],
    ]) {
      if ((ownerName === "inspector pane" || ownerName === "output CodeBlock wrapper" || ownerName === "output CodeBlock pre") && !box) continue;
      assertNoHorizontalOverflow(fixture, ownerName, box, diagnostics);
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

    if (treeModes.has(measurement.mode)) {
      assertNoHorizontalOverflow(fixture, "tree pane", measurement.treePane, diagnostics, { expectedOverflowX: "hidden" });
    }

    if (measurement.outputCodePre) {
      if (measurement.outputCodePre.whiteSpace !== "pre-wrap") {
        failures.push(`${fixture}: output CodeBlock pre white-space is ${JSON.stringify(measurement.outputCodePre.whiteSpace)}, expected "pre-wrap"`);
      }
    } else if (measurement.mode === "desktop" || measurement.mode === "mobile-inspector") {
      failures.push(`${fixture}: missing output CodeBlock pre`);
    }

    if (treeModes.has(measurement.mode)) {
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
          "desktop keeps exactly two visible panes, mobile variants keep exactly one readable pane, the sheet/body/pane owners stay free of horizontal scrolling, production CodeBlock output wraps instead of scrolling sideways, deep labels clip with ellipsis, and the mobile Back control stays inside the viewport",
      }
    : {
        pass: false,
        reason: failures.join("; "),
      };
}
