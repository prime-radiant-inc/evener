export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

  for (const m of measurements) {
    const fail = (message) => failures.push(`${m.width}px: ${message}`);
    const expectDisplay = (display, visible, label) => {
      const shown = display !== "none";
      if (shown !== visible) fail(`${label} should be ${visible ? "shown" : "hidden"}, got display:${display}`);
    };
    const expectContained = (inner, outer, label) => {
      if (inner.width <= 0 || inner.left < outer.left - tolerance || inner.right > outer.right + tolerance) {
        fail(`${label} escapes or is clipped by its container`);
      }
    };

    if (m.chrome.scrollWidth > m.chrome.clientWidth + tolerance) fail("footer scroll width exceeds client width");
    if (m.body.scrollWidth > m.body.clientWidth + tolerance) fail("status body scroll width exceeds client width");
    if (m.status.scrollWidth > m.status.clientWidth + tolerance) fail("status facts are internally clipped");
    const bodyCenter = (m.body.top + m.body.bottom) / 2;
    const rightCenter = (m.right.top + m.right.bottom) / 2;
    if (Math.abs(bodyCenter - rightCenter) > tolerance) fail("body and trailing controls are not on one line");
    if (m.actions.right > m.chrome.right + tolerance) fail("session actions escape the footer");
    expectContained(m.effort, m.status, "effort");
    expectContained(m.context, m.status, "context");
    expectContained(m.queue, m.status, "queue");
    if (m.contextStructure.semanticTag !== "METER") fail("context semantic is not a native meter");
    if (m.contextStructure.semanticChildren !== 0) fail("native context meter contains visual fallback children");
    if (
      m.contextStructure.semanticBox.width > tolerance ||
      m.contextStructure.semanticBox.height > tolerance
    ) {
      fail("native context meter is not visually hidden");
    }
    if (!m.contextStructure.semanticNextIsVisual) fail("context meter and visual wrapper are not siblings");
    if (m.contextStructure.visualTag !== "SPAN" || m.contextStructure.visualAriaHidden !== "true") {
      fail("context visual wrapper is not an aria-hidden span");
    }
    if (m.model.textOverflow !== "ellipsis" || m.model.whiteSpace !== "nowrap") fail("model does not ellipsize");
    if (m.model.clientWidth <= 0) fail("model visible value has zero width");
    if (m.focusedModelBoxShadow === "none" || !m.focusedModelBoxShadow.includes("inset")) {
      fail("focused model ring is not inset");
    }
    if (m.focusedGoalBoxShadow === "none" || !m.focusedGoalBoxShadow.includes("inset") || m.focusedGoalOutline !== "none") {
      fail("focused goal ring is not containment-safe");
    }
    if (
      m.focusedActionsBoxShadow === "none" ||
      !m.focusedActionsBoxShadow.includes("inset") ||
      m.focusedActionsOutline !== "none"
    ) {
      fail("focused session-actions ring is not containment-safe");
    }

    const full = m.width >= 560;
    const fullQueue = m.width >= 480;
    const barContext = m.width >= 400;

    expectDisplay(m.workDisplay, full, "work time");
    expectDisplay(m.goalDisplay, full, "goal chip");
    expectDisplay(m.queueFullDisplay, fullQueue, "full queue label");
    expectDisplay(m.queueCompactDisplay, !fullQueue, "compact queue label");
    expectDisplay(m.contextMeterDisplay, barContext, "context meter");
    expectDisplay(m.contextPercentDisplay, !barContext, "context percentage");
    if (full) {
      expectContained(m.work, m.status, "work time");
      expectContained(m.goal, m.body, "goal chip");
      expectContained(m.goalButton, m.body, "goal button");
      if (m.model.triggerWidth < 72 - tolerance) fail("model did not receive its 72px preferred width");
    }
  }

  return failures.length === 0
    ? { pass: true, reason: "footer stays on one line and follows every compression threshold" }
    : { pass: false, reason: failures.join("; ") };
}
