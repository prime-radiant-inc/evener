export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

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
      `tree=${sizing.treePaneWidth}px`,
      `rowActionsWrap=${JSON.stringify(sizing.rowActionsFlexWrap)}`,
      `rowActionsPadding=${JSON.stringify(sizing.rowActionsPaddingLeft)}`,
      widest,
    ].join(", ");
  }

  function assertNoHorizontalOverflow(fixture, ownerName, box, diagnostics, { expectedOverflowX } = {}) {
    if (!box) {
      failures.push(`${fixture}: missing ${ownerName}`);
      return;
    }
    if (expectedOverflowX !== undefined && box.overflowX !== expectedOverflowX) {
      failures.push(
        `${fixture}: ${ownerName} overflow-x is ${JSON.stringify(box.overflowX)}, expected ${JSON.stringify(expectedOverflowX)}`,
      );
    }
    if (expectedOverflowX === undefined && /auto|scroll/.test(box.overflowX)) {
      failures.push(
        `${fixture}: ${ownerName} overflow-x is ${JSON.stringify(box.overflowX)} - non-output owners must not be horizontal scrollers`,
      );
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
    if (!measurement.structure?.treePanePresent) failures.push(`${fixture}: missing tree pane`);
    if (measurement.structure?.sheetContainsBody === false)
      failures.push(`${fixture}: sheet wrapper does not contain the sheet body wrapper`);
    if (measurement.structure?.bodyContainsActivity === false)
      failures.push(`${fixture}: sheet body wrapper does not contain the inner Activity panel wrapper`);
    if (measurement.structure?.activityContainsTreePane === false)
      failures.push(`${fixture}: inner Activity panel wrapper does not contain the tree pane`);
    if (measurement.structure?.treePaneContainsTree === false)
      failures.push(`${fixture}: tree pane does not contain the tree element`);
    if (measurement.mode === "desktop" && !isWideSheetInlineSize(measurement.sizing?.sheetInlineSizeVar)) {
      failures.push(
        `${fixture}: sheet wrapper --sheet-inline-size is ${JSON.stringify(measurement.sizing?.sheetInlineSizeVar ?? null)}, expected the real wide Sheet value`,
      );
    }

    for (const [ownerName, box] of [
      ["sheet wrapper", measurement.sheet],
      ["sheet body", measurement.sheetBody],
      ["Activity panel wrapper", measurement.activityPanel],
    ]) {
      assertNoHorizontalOverflow(fixture, ownerName, box, diagnostics);
    }
    // The tree pane is the one sanctioned scroller in this view: it scrolls
    // vertically, never horizontally.
    assertNoHorizontalOverflow(fixture, "tree pane", measurement.treePane, diagnostics, {
      expectedOverflowX: "hidden",
    });

    // The dense tree is a single column in every form factor since the
    // 2026-08-05 redesign removed the master-detail inspector pane.
    if (measurement.visiblePaneCount !== 1) {
      failures.push(
        `${fixture}: dense tree shows ${measurement.visiblePaneCount} visible panes (${paneRoles}), expected exactly 1 pane`,
      );
    }

    // The dense row's label is the collapsed form: it clips to one line
    // with an ellipsis no matter the form factor.
    const labelBox = measurement.deepLabel;
    if (!labelBox) {
      failures.push(`${fixture}: missing deep label probe`);
    } else {
      if (labelBox.whiteSpace !== "nowrap") {
        failures.push(
          `${fixture}: deep label white-space is ${JSON.stringify(labelBox.whiteSpace)}, expected "nowrap" for single-line clipping`,
        );
      }
      if (labelBox.textOverflow !== "ellipsis") {
        failures.push(
          `${fixture}: deep label text-overflow is ${JSON.stringify(labelBox.textOverflow)}, expected "ellipsis"`,
        );
      }
      if (labelBox.scrollWidth <= labelBox.clientWidth + tolerance) {
        failures.push(
          `${fixture}: deep label scrollWidth ${labelBox.scrollWidth}px does not exceed clientWidth ${labelBox.clientWidth}px - the probe text is not actually clipped`,
        );
      }
    }
    // The detail strip is the label's unabridged counterpart: it WRAPS the
    // whole command instead of clipping it, so it must never overflow
    // sideways either.
    const detailBox = measurement.deepDetail;
    if (!detailBox) {
      failures.push(`${fixture}: missing deep detail probe`);
    } else {
      if (detailBox.whiteSpace !== "pre-wrap") {
        failures.push(
          `${fixture}: deep detail white-space is ${JSON.stringify(detailBox.whiteSpace)}, expected "pre-wrap" so the whole command shows`,
        );
      }
      if (detailBox.scrollWidth > detailBox.clientWidth + tolerance) {
        failures.push(
          `${fixture}: deep detail scrollWidth ${detailBox.scrollWidth}px exceeds clientWidth ${detailBox.clientWidth}px - the wrapped command must not scroll sideways`,
        );
      }
    }
  }

  return failures.length === 0
    ? {
        pass: true,
        reason:
          "the dense activity tree keeps exactly one visible pane at desktop and mobile widths, the sheet/body/pane owners stay free of horizontal scrolling, deep row labels clip with ellipsis while the detail strip wraps the whole command, and the continuation strip wraps instead of scrolling sideways",
      }
    : {
        pass: false,
        reason: failures.join("; "),
      };
}
