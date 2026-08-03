export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;

  for (const m of measurements) {
    const fail = (message) => failures.push(`${m.width}px: ${message}`);
    const expectDisplay = (display, visible, label) => {
      const shown = display !== "none";
      if (shown !== visible) fail(`${label} should be ${visible ? "shown" : "hidden"}, got display:${display}`);
    };

    if (m.chrome.scrollWidth > m.chrome.clientWidth + tolerance) fail("footer scroll width exceeds client width");
    if (m.body.scrollWidth > m.body.clientWidth + tolerance) fail("status body scroll width exceeds client width");
    if (Math.abs(m.body.top - m.right.top) > tolerance) fail("body and trailing controls are not on one line");
    if (m.body.bottom > m.right.bottom + tolerance) fail("status body grew onto a second line");
    if (m.actions.right > m.chrome.right + tolerance) fail("session actions escape the footer");
    if (m.effort.right > m.body.right + tolerance) fail("effort is clipped");
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
    if (m.focusedModelBoxShadow === "none" || !m.focusedModelBoxShadow.includes("inset")) {
      fail("focused model ring is not inset");
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
  }

  return failures.length === 0
    ? { pass: true, reason: "footer stays on one line and follows every compression threshold" }
    : { pass: false, reason: failures.join("; ") };
}
