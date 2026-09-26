// An intent-less expandable ToolRow's disclosure button is a SIBLING of the
// summary line, and toolcallitem.module.css's
// .row:not([data-intent="true"]) .trigger overlay stretches it over the whole
// row. Plain summary text must therefore reach the trigger, while the summary's
// real link and its inline Open control opt back into hit testing and reach
// themselves (the summary line's pointer-events: none plus the a/button
// pointer-events: auto rules).
export default function assert(measurement) {
  const failures = [];
  for (const f of measurement.fixtures) {
    if (!f.triggerCoversRow)
      failures.push(
        `${f.name}: the disclosure trigger does not cover its row - the intent-less overlay (.row:not([data-intent="true"]) .trigger) is not applying`,
      );
    if (!f.triggerCoversSummary) failures.push(`${f.name}: the disclosure trigger does not cover the summary line`);
    if (!f.plainHitsTrigger)
      failures.push(`${f.name}: a click on plain summary text lands on ${f.plainHit}, not the disclosure trigger`);
    if (!f.controlHitsControl)
      failures.push(
        `${f.name}: a click on the ${f.controlKind} lands on ${f.controlHit}, not the ${f.controlKind} - the overlay swallows it`,
      );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `in both intent-less rows (${measurement.fixtures.map((f) => f.name).join(", ")}) the trigger covers the row, plain summary text reaches the trigger, and the link / Open control reach themselves`,
  };
}
