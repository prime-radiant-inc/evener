export default function assert(m) {
  const failures = [];
  const tolerance = 1;
  const { short, tall } = m;

  if (m.documentHeight > m.viewportHeight + tolerance) {
    failures.push(`outer document scrolls: ${m.documentHeight}px document in ${m.viewportHeight}px viewport`);
  }
  if (short.pane.bottom > m.viewportHeight + tolerance) {
    failures.push(`pane escapes viewport by ${(short.pane.bottom - m.viewportHeight).toFixed(1)}px`);
  }
  if (short.composer.height > short.dock.height + tolerance) {
    failures.push(
      `short question keeps dead space in replacement slot (${short.dock.height}px dock in ${short.composer.height}px slot)`,
    );
  }
  if (Math.abs(short.dock.bottom - short.composer.bottom) > tolerance) {
    failures.push(
      `short question is not bottom-anchored (${short.dock.bottom}px vs slot bottom ${short.composer.bottom}px)`,
    );
  }
  if (short.dock.bottom > short.status.top + tolerance) {
    failures.push(`short question overlaps status footer by ${(short.dock.bottom - short.status.top).toFixed(1)}px`);
  }
  if (tall.dock.scrollHeight <= tall.dock.clientHeight + tolerance) {
    failures.push(`tall question did not exceed its dock (${tall.dock.scrollHeight}px vs ${tall.dock.clientHeight}px)`);
  }
  if (tall.dock.overflowY !== "auto" && tall.dock.overflowY !== "scroll") {
    failures.push(`tall question overflow belongs to ${tall.dock.overflowY}, not the question dock`);
  }
  if (tall.dock.bottom > tall.status.top + tolerance) {
    failures.push(`tall question overlaps status footer by ${(tall.dock.bottom - tall.status.top).toFixed(1)}px`);
  }
  if (short.body.clientHeight < 0.2 * short.pane.height) {
    failures.push(`transcript collapsed to ${short.body.clientHeight}px of a ${short.pane.height}px pane`);
  }

  return failures.length === 0
    ? { pass: true, reason: "desktop replacement slot fills, bottom-anchors, and internally scrolls" }
    : { pass: false, reason: failures.join("; ") };
}
