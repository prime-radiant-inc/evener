const TAP_MIN = 44;
const TOLERANCE = 0.5;
// The fixture records the baseline at 466x678 before compacting. The compact
// empty/live dock must recover at least 48px of vertical chrome.
export const DUO_EMPTY_LIVE_BASELINE = 215.2;
export const DUO_EMPTY_LIVE_MAX = DUO_EMPTY_LIVE_BASELINE - 48;

export default function assert(m) {
  const failures = [];
  if (!m.viewport.coarse || m.viewport.width !== 466 || m.viewport.height !== 678) {
    failures.push("the fixture did not run at 466x678 with a coarse pointer");
  }
  if (m.totalFooter > DUO_EMPTY_LIVE_MAX + TOLERANCE) {
    failures.push(`the empty/live footer is ${m.totalFooter.toFixed(1)}px, above the ${DUO_EMPTY_LIVE_MAX}px compact budget (baseline ${DUO_EMPTY_LIVE_BASELINE}px)`);
  }
  if (m.editor.emptyEditorHeight > 26.5) {
    failures.push(`the empty editor is ${m.editor.emptyEditorHeight.toFixed(1)}px high, rather than one 16px line at the existing body leading`);
  }
  const focused = m.states.find((state) => state.state === "focused-empty");
  if (!focused || focused.editorHeight < 50) failures.push("the focused empty editor does not restore its ordinary two-line height");
  const filled = m.states.find((state) => state.state === "filled");
  if (!filled || filled.totalFooter < m.totalFooter) failures.push("a filled draft collapses below the empty composer's height");
  const attachment = m.states.find((state) => state.state === "attachment");
  if (!attachment?.attachment || attachment.attachment.height < 48) failures.push("the staged attachment does not retain its visible space");
  const question = m.states.find((state) => state.state === "question");
  if (!question?.cardHidden || !question.question || question.question.height < 100) failures.push("the pending question loses its response surface");
  for (const option of question?.optionTargets ?? []) {
    if (option.height < TAP_MIN - TOLERANCE) failures.push("a pending-question option falls below the touch floor");
  }
  if (m.counters.length !== 4) failures.push(`expected all four activity links, found ${m.counters.length}`);
  for (const [index, counter] of m.counters.entries()) {
    if (counter.width < TAP_MIN - TOLERANCE || counter.height < TAP_MIN - TOLERANCE) {
      failures.push(`activity link ${index + 1} is ${counter.width.toFixed(1)}x${counter.height.toFixed(1)}, below 44px`);
    }
  }
  for (const control of m.controls) {
    if (control.display === "none" || control.height === 0) continue;
    if (control.height < TAP_MIN - TOLERANCE) failures.push(`a composer control is only ${control.height.toFixed(1)}px high`);
  }
  if (m.statusChildren.some((el) => el.scrollWidth > el.clientWidth + TOLERANCE)) {
    failures.push("the path/branch and activity counters overflow the one-row footer");
  }
  if (m.transcript.lastBottom > m.transcript.dockTop + TOLERANCE) {
    failures.push(`the last transcript line ends at ${m.transcript.lastBottom.toFixed(1)}, under the dock at ${m.transcript.dockTop.toFixed(1)}`);
  }
  for (const prose of m.prose) {
    if (prose.fontSize !== "16px" || prose.lineHeight !== "23.2px") {
      failures.push(`prose measures ${prose.fontSize}/${prose.lineHeight}, not the compact 16px/23.2px ramp`);
    }
  }
  return failures.length === 0
    ? { pass: true, reason: `the empty/live dock is ${m.totalFooter.toFixed(1)}px, all four activity links and composer controls retain 44px targets, and the transcript clears the dock` }
    : { pass: false, reason: failures.join("; ") };
}
