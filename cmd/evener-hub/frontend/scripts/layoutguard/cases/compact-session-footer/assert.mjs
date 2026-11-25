export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

  for (const m of measurements) {
    const fixtureLabel = `${m.width}px${m.folded ? "-folded" : ""}/${m.statusBody.width}px-actual-status${m.expectedStatusWidth === null ? "" : `/${m.expectedStatusWidth}px-expected-status`}${m.shortModel ? " short-model" : ""}`;
    const fail = (message) => failures.push(`${fixtureLabel}: ${message}`);
    const expectDisplay = (display, visible, label) => {
      const shown = display !== "none";
      if (shown !== visible) fail(`${label} should be ${visible ? "shown" : "hidden"}, got display:${display}`);
    };
    const expectContained = (inner, outer, label) => {
      if (
        inner.width <= 0 ||
        inner.left < outer.left - tolerance ||
        inner.right > outer.right + tolerance ||
        inner.top < outer.top - tolerance ||
        inner.bottom > outer.bottom + tolerance
      ) {
        fail(`${label} escapes or is clipped by its container`);
      }
    };
    const expectSameRow = (box, label) => {
      const controlsCenter = (m.controls.top + m.controls.bottom) / 2;
      const boxCenter = (box.top + box.bottom) / 2;
      if (Math.abs(boxCenter - controlsCenter) > tolerance)
        fail(`${label} is not centered on the composer control row`);
    };
    const expectTwoPixelOutline = (style, width, label) => {
      if (style === "none" || Number.parseFloat(width) < 2) fail(`${label} has no usable focus-visible outline`);
    };
    const expectBefore = (left, right, label) => {
      if (left.right > right.left + tolerance) fail(`${label} controls are out of horizontal order or overlap`);
    };

    for (const [label, box] of [
      ["card", m.card],
      ["composer controls", m.controls],
      ["leading controls", m.leading],
      ["inline session chrome", m.inlineChrome],
      ["status body", m.statusBody],
      ["status facts", m.status],
    ]) {
      if (box.scrollWidth > box.clientWidth + tolerance) fail(`${label} scroll width exceeds client width`);
    }

    expectContained(m.controls, m.card, "composer control row");
    if (m.folded) {
      expectContained(m.field, m.card, "folded field");
      expectBefore(m.field, m.controls, "field → controls");
      expectSameRow(m.field, "folded field");
      // The folded card is one control row inside its own framing: the
      // card's computed padding and border, measured by the harness.
      if (m.card.height > m.controls.height + tolerance + m.cardFraming) {
        fail("folded card is taller than one control row plus its framing");
      }
    }
    expectContained(m.attachment, m.card, "attachment control");
    expectContained(m.modelTrigger, m.controls, "model control");
    expectContained(m.effort, m.controls, "effort control");
    expectContained(m.context, m.controls, "context control");
    expectContained(m.sessionActions, m.controls, "session actions");
    expectContained(m.send, m.controls, "Send control");
    expectContained(m.effort, m.status, "effort");
    expectContained(m.context, m.status, "context");
    expectContained(m.context, m.identity, "context inside identity");
    expectContained(m.queue, m.status, "queue");

    for (const [label, box] of [
      ["model control", m.modelTrigger],
      ["effort control", m.effort],
      ["context control", m.context],
      ["session actions", m.sessionActions],
      ["Send control", m.send],
    ]) {
      expectSameRow(box, label);
    }
    if (Math.abs(m.attachment.bottom - m.controls.bottom) > tolerance) {
      fail("attachment control is not bottom-aligned with the composer controls");
    }

    expectBefore(m.attachment, m.context, "attachment → context");
    expectBefore(m.context, m.modelTrigger, "context → model");
    expectBefore(m.modelTrigger, m.effort, "model → effort");
    expectBefore(m.effort, m.sessionActions, "effort → session actions");
    expectBefore(m.sessionActions, m.send, "session actions → Send");
    if (Math.abs(m.send.right - m.controls.right) > tolerance) fail("Send is not pinned to the trailing edge");
    // PromptCard's horizontal padding must leave enough room for Button's 2px
    // outline plus its 2px offset even though Send is the row's trailing item.
    if (m.card.right - m.send.right < 4 - tolerance) fail("Send focus ring has no trailing clearance inside the card");

    const context = m.contextStructure;
    if (context.meterCount !== 1) fail(`context should expose exactly one role=meter, got ${context.meterCount}`);
    if (context.meterAriaLabel === null || context.meterAriaLabel.trim() === "") {
      fail("exposed context meter has no accessible name");
    }
    if (context.meterAriaHidden !== false) fail("context meter is hidden from the accessibility tree");
    if (context.wrapperAriaHidden !== null) fail("context wrapper is aria-hidden, hiding its only meter");
    if (m.model.textOverflow !== "ellipsis" || m.model.whiteSpace !== "nowrap") fail("model does not ellipsize");
    if (m.model.clientWidth <= 0) fail("model visible value has zero width");

    expectTwoPixelOutline(m.focus.attachOutlineStyle, m.focus.attachOutlineWidth, "attachment control");
    // Containment-safe ring recipe (design-system focus-ring contract): the
    // ring is the shared `outline: var(--focus-ring)` pulled fully inside the
    // border box with a negative outline-offset, so an overflow boundary flush
    // against the control cannot clip it.
    const expectInsetOutline = (style, width, offset, label) => {
      const widthPx = Number.parseFloat(width);
      if (style === "none" || widthPx < 2 || Number.parseFloat(offset) > -widthPx) {
        fail(`focused ${label} ring is not containment-safe`);
      }
    };
    expectInsetOutline(m.focus.modelOutlineStyle, m.focus.modelOutlineWidth, m.focus.modelOutlineOffset, "model");
    expectInsetOutline(m.focus.effortOutlineStyle, m.focus.effortOutlineWidth, m.focus.effortOutlineOffset, "effort");
    expectInsetOutline(
      m.focus.actionsOutlineStyle,
      m.focus.actionsOutlineWidth,
      m.focus.actionsOutlineOffset,
      "session-actions",
    );
    expectTwoPixelOutline(m.focus.sendOutlineStyle, m.focus.sendOutlineWidth, "Send control");

    // Boundary fixtures declare the exact StatusRow query-container width they
    // must produce. This makes 399/400 (compact row gap / effort shrink),
    // 479/480 (queue label), and 559/560 (work time) falsifiable: moving a CSS
    // boundary even when the outer composer still fits fails below.
    if (m.expectedStatusWidth !== null && Math.abs(m.statusBody.width - m.expectedStatusWidth) > tolerance) {
      fail(
        `status query container should be ${m.expectedStatusWidth}px, got ${m.statusBody.width}px rect/${m.statusBody.clientWidth}px client`,
      );
    }
    const queryWidth = m.expectedStatusWidth ?? m.statusBody.clientWidth;
    const full = queryWidth >= 560;
    const fullQueue = queryWidth >= 480;

    expectDisplay(m.workDisplay, full, "work time");
    expectDisplay(m.queueFullDisplay, fullQueue, "full queue label");
    expectDisplay(m.queueCompactDisplay, !fullQueue, "compact queue label");
    if (context.wrapperDisplay === "none" || context.meterDisplay === "none") {
      fail("context ring is hidden via display:none");
    }
    if (context.meterBox === null || context.meterBox.width < 13 || context.meterBox.height < 13) {
      fail(
        `context ring is not visible at full ring dimensions (${context.meterBox === null ? "missing" : `${context.meterBox.width.toFixed(1)}x${context.meterBox.height.toFixed(1)}px`})`,
      );
    } else {
      if (
        m.context.width < context.meterBox.width - tolerance ||
        m.context.height < context.meterBox.height - tolerance
      ) {
        fail(
          `context wrapper shrank below the 14px ring (${m.context.width.toFixed(1)}x${m.context.height.toFixed(1)}px for ${context.meterBox.width.toFixed(1)}x${context.meterBox.height.toFixed(1)}px ring)`,
        );
      }
      expectContained(context.meterBox, m.context, "context ring inside context wrapper");
    }
    // The resolver inlines composed @container rules ungated, so only the narrow hide is observable here.
    if (m.width <= 559) expectDisplay(m.stopLabelDisplay, false, "Stop label stack");
    if (full) expectContained(m.work, m.status, "work time");

    // Shrink-to-fit contract: the model trigger hugs its label at every width.
    // 40px leaves slack beyond the real 26px chrome for sub-pixel drift while
    // remaining far below the hundreds of px a grow-to-fill trigger adds in the
    // wide short-label fixture.
    const modelChromeSlack = 40;
    if (!m.folded && m.model.triggerWidth > m.model.clientWidth + modelChromeSlack) {
      fail("model trigger reserves space beyond its label (not shrink-to-fit)");
    }
  }

  return failures.length === 0
    ? { pass: true, reason: "inline composer controls share one row and preserve every compression threshold" }
    : { pass: false, reason: failures.join("; ") };
}
