// A whiteboard keeps its line breaks: the expanded Notes panel's read-only
// note shows one visual line per note line. The collapsed whiteboard is a
// count line, not a preview (docs/web-ui/design-system.md section 12), so its
// content rides the trigger's accessible name (owned by the component test)
// and has no visual line count to guard here.
export default function assert(measurement) {
  const { agent } = measurement;
  const failures = [];
  if (agent.visibleLines !== agent.noteLines) {
    failures.push(
      `agent note: ${agent.noteLines}-line note rendered ${agent.visibleLines} visual line(s); the panel no longer keeps its line breaks`,
    );
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `agent note ${agent.visibleLines}/${agent.noteLines} lines (${agent.height.toFixed(1)}px)`,
  };
}
