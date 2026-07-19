// N1-drift-gap: the prepend settle's drift correction writes scrollTop inside
// a rAF callback; per the HTML spec's update-the-rendering steps the browser
// fires that write's scroll event in the NEXT frame's scroll steps — which run
// BEFORE that frame's rAF callbacks — not in a later task. The
// programmatic-scroll depth hold must therefore be released one FRAME after
// the correction (a one-task release lands before the event and lets it read
// as reader intent, suppressing the hydration-end settle).
// jsdom has no real rAF/scroll-event pipeline, so scheduleFrame is stubbed to
// capture the keyed callbacks (preserving per-key cancellation) and the test
// runs them by hand to assert:
//   (a) the release is scheduled on the "prepend-settle-release" key only
//       AFTER the settle callback runs (ordering),
//   (b) a scroll event dispatched between the settle and the release does NOT
//       set readerScrolledDuringHydration (depth still held), while one
//       dispatched after the release DOES,
//   (c) a cancelled settle's holds are drained by the surviving release
//       (accumulator semantics — no stranded depth),
//   (d) a cancelled "prepend-settle-release" cannot strand depth either.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div id="conversation-host">
    <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  </div>
  <div id="input-status" class="input-status">
    <span class="status-item liveness-inline" data-liveness hidden></span>
  </div>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (text) => text };
window.fetch = () => Promise.resolve({
  ok: true,
  json: () => Promise.resolve([]),
  text: () => Promise.resolve(""),
});
window.SerfAppwire = {
  tasks: () => new Promise(() => {}),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: () => () => {},
  onConnectionLost: () => () => {},
  readThread: () => new Promise(() => {}),
  listTurns: () => Promise.resolve({ turns: [], nextCursor: "" }),
  eventsFromThread: (thread) => thread.testEvents || [],
  eventsFromTurns: (turns) => turns.map((t) => [t.kind || "USER_INPUT", { text: t.text }]),
  eventsFromNotification: () => [],
};

require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const STUB_HEIGHT = 5000;
const conv = window.document.getElementById("conversation");
Object.defineProperty(conv, "scrollHeight", { value: STUB_HEIGHT, configurable: true });

// Stub scheduleFrame: capture the keyed callbacks instead of running them on
// jsdom's timer-rAF, preserving the real per-key cancellation semantics
// (scheduling on a key cancels that key's pending callback).
const pendingFrames = new Map();
const frameEntries = [];
R.scheduleFrame = (cb, key) => {
  const slot = key == null ? "default" : key;
  const prev = pendingFrames.get(slot);
  if (prev) prev.cancelled = true;
  const entry = { slot, cb, cancelled: false };
  pendingFrames.set(slot, entry);
  frameEntries.push(entry);
};
const runFrame = (slot) => {
  const entry = pendingFrames.get(slot);
  pendingFrames.delete(slot);
  if (!entry || entry.cancelled) return false;
  entry.cb();
  return true;
};
// The prepend's internal replay may stick-to-bottom (its own scrollToBottom),
// which holds depth under a "scroll-bottom-release" frame of its own; flush it
// so assertions on programmaticScrollDepth isolate the prepend holds.
const flushScrollBottomRelease = () => {
  while (pendingFrames.has("scroll-bottom-release")) runFrame("scroll-bottom-release");
};
const dispatchScroll = () => conv.dispatchEvent(new window.Event("scroll"));

R.init(conv);
R.hydrationInProgress = true; // simulate the mid-hydration paging window

// ── (a) ordering: the release is chained one FRAME after the settle ────────
R.prependOlderTurns([{ kind: "NOOP_KIND", text: "older-a" }]);
pass(pendingFrames.has("prepend-settle"),
  "(a) the settle frame is scheduled on the 'prepend-settle' key");
pass(!pendingFrames.has("prepend-settle-release"),
  "(a) the release is NOT scheduled before the settle callback runs");
pass(R.prependSettleHolds === 1 && R.programmaticScrollDepth >= 1,
  "(a) the prepend holds a depth count from the sync compensation onward (holds=" +
  R.prependSettleHolds + ", depth=" + R.programmaticScrollDepth + ")");

pass(runFrame("prepend-settle"), "(a) the settle callback ran");
pass(pendingFrames.has("prepend-settle-release"),
  "(a) the release is scheduled on the 'prepend-settle-release' key AFTER the settle callback");
const settleIdx = frameEntries.map((e) => e.slot).lastIndexOf("prepend-settle");
const releaseIdx = frameEntries.map((e) => e.slot).lastIndexOf("prepend-settle-release");
pass(settleIdx !== -1 && releaseIdx > settleIdx,
  "(a) scheduling order: 'prepend-settle' (" + settleIdx + ") precedes 'prepend-settle-release' (" + releaseIdx + ")");

// ── (b) depth held across the correction's scroll-event window ─────────────
pass(R.programmaticScrollDepth >= 1,
  "(b) depth is still held after the settle — the release frame is pending (depth=" +
  R.programmaticScrollDepth + ")");
dispatchScroll();
pass(R.readerScrolledDuringHydration === false,
  "(b) a scroll event dispatched between the settle and the release does NOT read as reader intent");

pass(runFrame("prepend-settle-release"), "(b) the release frame ran");
pass(R.prependSettleHolds === 0,
  "(b) the release drained the holds accumulator (prependSettleHolds=" + R.prependSettleHolds + ")");
flushScrollBottomRelease();
pass(R.programmaticScrollDepth === 0,
  "(b) depth returned to 0 after the release (depth=" + R.programmaticScrollDepth + ")");
dispatchScroll();
pass(R.readerScrolledDuringHydration === true,
  "(b) a scroll event dispatched after the release DOES read as reader intent");
R.readerScrolledDuringHydration = false;

// ── (c) a cancelled settle's hold is drained by the surviving release ──────
const entriesBeforeC = frameEntries.length;
R.prependOlderTurns([{ kind: "NOOP_KIND", text: "older-c1" }]);
R.prependOlderTurns([{ kind: "NOOP_KIND", text: "older-c2" }]); // reschedules 'prepend-settle', cancelling the first
const settleEntriesC = frameEntries.slice(entriesBeforeC).filter((e) => e.slot === "prepend-settle");
pass(settleEntriesC.length === 2 && settleEntriesC[0].cancelled && !settleEntriesC[1].cancelled,
  "(c) the second prepend cancelled the first settle (entries=" + settleEntriesC.length + ")");
pass(R.prependSettleHolds === 2,
  "(c) both holds accumulate (prependSettleHolds=" + R.prependSettleHolds + ")");
pass(runFrame("prepend-settle"), "(c) the surviving settle ran");
pass(runFrame("prepend-settle-release"), "(c) the surviving release ran");
pass(R.prependSettleHolds === 0,
  "(c) the surviving release drained BOTH holds (prependSettleHolds=" + R.prependSettleHolds + ")");
flushScrollBottomRelease();
pass(R.programmaticScrollDepth === 0,
  "(c) no stranded depth after a cancelled settle (depth=" + R.programmaticScrollDepth + ")");

// ── (d) a cancelled 'prepend-settle-release' cannot strand depth ───────────
R.prependOlderTurns([{ kind: "NOOP_KIND", text: "older-d1" }]);
pass(runFrame("prepend-settle"), "(d) first settle ran — release now pending");
const pendingReleaseD = pendingFrames.get("prepend-settle-release");
R.prependOlderTurns([{ kind: "NOOP_KIND", text: "older-d2" }]); // settle B will reschedule the release key
pass(runFrame("prepend-settle"), "(d) second settle ran — it rescheduled the release");
pass(pendingReleaseD && pendingReleaseD.cancelled,
  "(d) the first release was cancelled by the second settle's release");
pass(R.prependSettleHolds === 2,
  "(d) the cancelled release's holds stay in the accumulator (prependSettleHolds=" + R.prependSettleHolds + ")");
pass(runFrame("prepend-settle-release"), "(d) the surviving release ran");
pass(R.prependSettleHolds === 0,
  "(d) the surviving release drained every hold (prependSettleHolds=" + R.prependSettleHolds + ")");
flushScrollBottomRelease();
pass(R.programmaticScrollDepth === 0,
  "(d) no stranded depth after a cancelled release (depth=" + R.programmaticScrollDepth + ")");
dispatchScroll();
pass(R.readerScrolledDuringHydration === true,
  "(d) after the surviving release a scroll event reads as reader intent again");

if (failures.length) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: prepend-settle depth release is frame-chained (ordering, hold window, accumulator drain)");
process.exit(0);
