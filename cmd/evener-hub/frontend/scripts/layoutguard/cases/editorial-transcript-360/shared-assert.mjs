export default function assert({ measurements, surfaces, collapsedFailure }) {
  const failures = [];
  if (!collapsedFailure?.visible || !collapsedFailure.collapsed || collapsedFailure.fill === "rgba(0, 0, 0, 0)")
    failures.push(`Intent: expected a visible filled failed call with evidence folded, got ${JSON.stringify(collapsedFailure)}`);
  for (const { level, element, edge, fill, shadow } of surfaces) {
    if (edge !== "0px" || fill !== "rgba(0, 0, 0, 0)" || shadow !== "none")
      failures.push(`${level}: ${element} frames transcript content (leading edge ${edge}, fill ${fill}, shadow ${shadow})`);
  }
  for (const m of measurements) {
    const id = `${m.theme}/${m.scale}/${m.density}/${m.width}`;
    if (m.outside.length || m.overflow > 1)
      failures.push(`${id}: overflow ${m.overflow}, outside ${JSON.stringify(m.outside)}`);
    if (m.toolCount < 8 || m.openControls.length !== 6) failures.push(`${id}: incomplete real fixture`);
    if (
      m.openControls.some(
        (b) => b.nested || b.width < (m.viewport < 900 ? 44 : 28) || b.height < (m.viewport < 900 ? 44 : 28),
      )
    )
      failures.push(`${id}: clipped/nested Open control`);
    if (!m.anchorVisible || !m.collapsed || m.collapsedStatus !== "Running" || m.unavailable !== 2)
      failures.push(`${id}: dishonest or hidden lifecycle`);
    if (!m.collapsedStatusVisible) failures.push(`${id}: collapsed status hidden`);
    if (![m.userFont, m.agentFont, m.quoteFont].every((f) => f?.includes("Source Serif 4")))
      failures.push(`${id}: prose not serif ${JSON.stringify(m)}`);
    if (m.userBackground !== "rgb(79, 91, 245)" || m.userColor !== "rgb(255, 255, 255)")
      failures.push(`${id}: user bubble is not white on accent fill (bg ${m.userBackground}, color ${m.userColor})`);
    if (
      !Array.isArray(m.tableCells) ||
      m.tableCells.length !== 5 ||
      m.tableCells.some((cell) => cell.width < 30 || cell.height < 16)
    )
      failures.push(`${id}: 5-column user table collapsed (bubble ${m.tableBubbleWidth}px, cells ${JSON.stringify(m.tableCells)})`);
    if (m.width >= 900 && (m.tableBubbleWidth < 500 || m.tableReflowed))
      failures.push(`${id}: wide 5-column user table unexpectedly collapsed or reflowed (bubble ${m.tableBubbleWidth}px, reflowed ${m.tableReflowed})`);
    if (m.width < 900 && m.tableBubbleWidth < 260)
      failures.push(`${id}: narrow 5-column user table collapsed to ${m.tableBubbleWidth}px`);
    if (m.codeLangOverlap || m.codeLangClippedLeft || m.codeBubbleWidth < 200)
      failures.push(
        `${id}: fenced code user bubble collided or collapsed (width ${m.codeBubbleWidth}px, overlap ${m.codeLangOverlap}, clippedLeft ${m.codeLangClippedLeft})`,
      );
    if (m.hrWidth < 160) failures.push(`${id}: standalone hr user bubble collapsed (hr width ${m.hrWidth}px)`);
    if (m.userSize !== (m.scale === "xl" ? "20px" : "16px"))
      failures.push(`${id}: reading preference did not scale: ${m.userSize}`);
    const expectedLineHeight = (m.scale === "xl" ? 29 : 23.2) * (m.density === "comfortable" ? 1.25 : 1);
    for (const [surface, actual] of [["settled", m.userLineHeight], ["live", m.agentLineHeight]]) {
      if (Math.abs(Number.parseFloat(actual) - expectedLineHeight) > 0.01)
        failures.push(`${id}: ${surface} prose leading ${actual}, expected ${expectedLineHeight}px`);
    }
    if (m.cardBackground !== "rgba(0, 0, 0, 0)") failures.push(`${id}: enclosing collaborator fill`);
  }
  return {
    pass: failures.length === 0,
    reason: failures.length
      ? failures.join("\n")
      : `${measurements.length} real-component theme/size/measure combinations: contained evidence, independent Open controls, visible expanded/collapsed status, truthful unknown, serif reading text and flat collaborators, filled collapsed failed call in Intent`,
  };
}
