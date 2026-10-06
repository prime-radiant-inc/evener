const SUMMARY_CLAMP_LINES = 3;

// A whiteboard keeps its line breaks: the Notes panel's read-only note shows
// one visual line per note line, and the collapsed summary keeps the breaks
// but shows at most three lines, clipping the rest.
export default function assert(measurement) {
  const { agent, summaryLong, summaryShort } = measurement;
  const failures = [];
  if (agent.visibleLines !== agent.noteLines) {
    failures.push(
      `agent note: ${agent.noteLines}-line note rendered ${agent.visibleLines} visual line(s); the panel no longer keeps its line breaks`,
    );
  }
  if (summaryShort.visibleLines !== summaryShort.noteLines) {
    failures.push(
      `collapsed summary: ${summaryShort.noteLines}-line note rendered ${summaryShort.visibleLines} visual line(s); the summary no longer keeps line breaks`,
    );
  }
  if (summaryLong.noteLines <= SUMMARY_CLAMP_LINES) {
    failures.push(`collapsed summary: fixture note has only ${summaryLong.noteLines} lines; it no longer exceeds the clamp`);
  }
  if (summaryLong.visibleLines !== SUMMARY_CLAMP_LINES) {
    failures.push(
      `collapsed summary: ${summaryLong.noteLines}-line note shows ${summaryLong.visibleLines} visual line(s), expected the ${SUMMARY_CLAMP_LINES}-line clamp`,
    );
  }
  if (summaryLong.clipped <= 0) {
    failures.push(`collapsed summary: ${summaryLong.noteLines}-line note is not clipped (scrollHeight - clientHeight = ${summaryLong.clipped})`);
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason:
      `agent note ${agent.visibleLines}/${agent.noteLines} lines (${agent.height.toFixed(1)}px); ` +
      `summary ${summaryShort.visibleLines}/${summaryShort.noteLines} lines; ` +
      `long summary clamped to ${summaryLong.visibleLines}/${summaryLong.noteLines} lines, ${summaryLong.clipped}px clipped`,
  };
}
