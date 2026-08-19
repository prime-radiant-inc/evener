export default function assert(m) {
  const failures = [];
  const tolerance = 1;

  if (m.documentHeight > m.viewportHeight + tolerance) {
    failures.push(`outer document scrolls: ${m.documentHeight}px document in ${m.viewportHeight}px viewport`);
  }
  if (m.pane.bottom > m.viewportHeight + tolerance) {
    failures.push(`pane escapes viewport by ${(m.pane.bottom - m.viewportHeight).toFixed(1)}px`);
  }
  if (m.dock.clientHeight < 0.4 * m.pane.height) {
    failures.push(`question dock collapsed to ${m.dock.clientHeight}px of a ${m.pane.height}px pane`);
  }
  if (m.body.clientHeight < 0.2 * m.pane.height) {
    failures.push(`transcript body collapsed to ${m.body.clientHeight}px of a ${m.pane.height}px pane`);
  }
  if (m.body.bottom > m.footer.top + tolerance) {
    failures.push(`transcript body overlaps footer by ${(m.body.bottom - m.footer.top).toFixed(1)}px`);
  }
  if (m.dock.bottom > m.status.top + tolerance) {
    failures.push(`question dock overlaps status footer by ${(m.dock.bottom - m.status.top).toFixed(1)}px`);
  }
  if (m.status.bottom > m.footer.bottom + tolerance) {
    failures.push(`status footer escapes its pane footer by ${(m.status.bottom - m.footer.bottom).toFixed(1)}px`);
  }
  if (m.body.scrollHeight > m.body.clientHeight && m.body.overflowY !== "auto" && m.body.overflowY !== "scroll") {
    failures.push(`transcript overflow belongs to ${m.body.overflowY}, not the pane body`);
  }
  if (m.dock.scrollHeight > m.dock.clientHeight && m.dock.overflowY !== "auto" && m.dock.overflowY !== "scroll") {
    failures.push(`question overflow belongs to ${m.dock.overflowY}, not the question dock`);
  }

  return failures.length === 0
    ? { pass: true, reason: "pane allocates usable transcript and question scroll regions without outer scrolling" }
    : { pass: false, reason: failures.join("; ") };
}
