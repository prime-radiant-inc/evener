export default function assert(measurements) {
  const failures = [];
  for (const m of measurements) {
    const id = `${m.theme}/${m.scale}/${m.width}`;
    if (m.outside.length || m.overflow > 1) failures.push(`${id}: overflow ${m.overflow}, outside ${JSON.stringify(m.outside)}`);
    if (m.toolCount < 8 || m.openControls.length !== 7) failures.push(`${id}: incomplete real fixture`);
    if (m.openControls.some((b) => b.nested || b.width < (m.viewport < 900 ? 44 : 28) || b.height < (m.viewport < 900 ? 44 : 28))) failures.push(`${id}: clipped/nested Open control`);
    if (!m.attentionVisible || !m.collapsed || !m.collapsedAttention.includes('Needs attention') || m.unavailable !== 2) failures.push(`${id}: dishonest or hidden lifecycle`);
    if (!m.collapsedAttentionVisible) failures.push(`${id}: collapsed attention hidden`);
    if (![m.userFont, m.agentFont, m.quoteFont].every((f) => f?.includes('Source Serif 4'))) failures.push(`${id}: prose not serif ${JSON.stringify(m)}`);
    if (m.userSize !== (m.scale === 'xl' ? '22.5px' : '18px')) failures.push(`${id}: reading preference did not scale: ${m.userSize}`);
    if (m.cardBackground !== 'rgba(0, 0, 0, 0)') failures.push(`${id}: enclosing collaborator fill`);
  }
  return { pass: failures.length === 0, reason: failures.length ? failures.join('\n') : `${measurements.length} real-component theme/size/measure combinations: contained evidence, independent Open controls, visible expanded/collapsed attention, truthful unknown, serif reading text and flat collaborators` };
}
