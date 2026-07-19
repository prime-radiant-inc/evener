// Reader scroll intent during hydration: the hydration-end settle must not
// yank a reader who scrolled mid-replay back to the bottom. A scroll event
// while hydrationInProgress marks reader intent; the final scrollToBottom is
// skipped for them. With no reader scroll the settle still parks at the
// bottom. jsdom does no layout, so scrollHeight is stubbed to a fixed value
// and scroll events are dispatched by hand.
// N1: prependOlderTurns' scrollTop writes (the sync compensation and the rAF
// drift correction) fire their scroll events ASYNC — while multi-page
// loadOlderTurnsUntilPrimaryDialogue runs mid-hydration those events must not
// read as reader intent, or the hydration-end settle is wrongly suppressed.
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

let readThreadImpl = () => new Promise(() => {});
let listTurnsImpl = () => Promise.resolve({ turns: [], nextCursor: "" });
window.SerfAppwire = {
  tasks: () => new Promise(() => {}),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: () => () => {},
  onConnectionLost: () => () => {},
  readThread: (sessionId) => readThreadImpl(sessionId),
  listTurns: (sessionId, cursor, limit) => listTurnsImpl(sessionId, cursor, limit),
  eventsFromThread: (thread) => thread.testEvents || [],
  eventsFromTurns: (turns) => turns.map((t) => [t.kind || "USER_INPUT", { text: t.text }]),
  eventsFromNotification: () => [],
};

require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
const host = window.document.getElementById("conversation-host");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const STUB_HEIGHT = 5000;
const userEvents = (prefix, count) => {
  const events = [];
  for (let i = 0; i < count; i++) events.push(["USER_INPUT", { text: prefix + i }]);
  return events;
};
const noopEvents = (count) => {
  const events = [];
  for (let i = 0; i < count; i++) events.push(["NOOP_KIND", { i }]);
  return events;
};
const threadWith = (sessionId, events, olderCursor) => ({
  thread: {
    id: sessionId,
    sessionId,
    status: "inProgress",
    serf: { ref: "local:" + sessionId, activeTurnId: "turn_active" },
    turns: [],
    testEvents: events,
  },
  olderCursor: olderCursor || "",
});
const waitFor = async (cond, timeoutMs) => {
  const deadline = Date.now() + (timeoutMs || 5000);
  while (!cond()) {
    if (Date.now() > deadline) return false;
    await new Promise((r) => setTimeout(r, 5));
  }
  return true;
};
const pump = async (ticks) => {
  for (let i = 0; i < (ticks || 10); i++) await new Promise((r) => setTimeout(r, 5));
};
const freshConversation = (sessionId) => {
  const el = window.document.createElement("div");
  el.className = "conversation";
  el.dataset.sessionId = sessionId;
  el.dataset.state = "active";
  Object.defineProperty(el, "scrollHeight", { value: STUB_HEIGHT, configurable: true });
  return el;
};

(async () => {
  // ── (a) no reader scroll: the hydration-end settle parks at the bottom ───
  const TOTAL = 300; // crosses one chunk boundary so the replay yields
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("event-", TOTAL)));
  const convA = freshConversation("01TEST");
  window.document.getElementById("conversation").replaceWith(convA);
  R.init(convA);
  pass(await waitFor(() => R.appwireHydrated === true, 30000), "hydration (no scroll) completed");
  pass(R.hydrationInProgress === false, "hydrationInProgress cleared (no-scroll case)");
  pass(convA.scrollTop === STUB_HEIGHT,
    "with no reader scroll the final settle scrolls to bottom (scrollTop=" + convA.scrollTop + ", want " + STUB_HEIGHT + ")");

  // ── (b) reader scrolls mid-replay: completion must NOT yank them ─────────
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("re-event-", TOTAL)));
  const convB = freshConversation("01TEST");
  convA.replaceWith(convB);
  R.init(convB); // reconnect-style re-hydration on a fresh element
  // Mid-replay (after the first chunk, before completion) the reader scrolls up.
  let depthAtReaderScroll = null;
  let flagAfterReaderScroll = null;
  setTimeout(() => {
    convB.scrollTop = 1200;
    depthAtReaderScroll = R.programmaticScrollDepth;
    convB.dispatchEvent(new window.Event("scroll"));
    flagAfterReaderScroll = R.readerScrolledDuringHydration;
  }, 0);
  pass(await waitFor(() => R.appwireHydrated === true, 30000), "hydration (reader scrolled) completed");
  pass(R.hydrationInProgress === false, "hydrationInProgress cleared (scrolled case)");
  pass(depthAtReaderScroll === 0,
    "converse: the reader scroll was dispatched at programmatic depth 0 (got " + depthAtReaderScroll + ")");
  pass(flagAfterReaderScroll === true,
    "converse: a real reader scroll event at depth 0 still sets readerScrolledDuringHydration");
  pass(convB.scrollTop === 1200,
    "reader scrolled mid-replay — final settle did NOT yank to bottom (scrollTop=" + convB.scrollTop + ", want 1200)");

  // ── (c) intent resets: a later hydration with no scroll sticks again ─────
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("re2-event-", 5)));
  R.connectAppwire(); // re-hydrate; the stale reader-intent flag must not leak
  pass(await waitFor(() => R.appwireHydrated === true && convB.querySelectorAll(".user-message").length === 5, 30000),
    "re-hydration completed");
  pass(convB.scrollTop === STUB_HEIGHT,
    "reader-intent flag was cleared at hydration start — settle scrolls to bottom again (scrollTop=" + convB.scrollTop + ")");

  // ── (d) N1: prepend scroll writes mid-hydration must not read as intent ──
  // Multi-page hydration: no primary dialogue in the hydrated window + an
  // older cursor, so loadOlderTurnsUntilPrimaryDialogue pages older turns in
  // AFTER the chunked replay drains — still mid-hydration. prependOlderTurns'
  // scrollTop writes (the sync compensation and the rAF drift correction)
  // fire their scroll events ASYNC; with the old synchronous marker those
  // events landed after the marker cleared and falsely set the reader-intent
  // flag, suppressing the hydration-end settle.
  const convD = freshConversation("01TEST");
  convB.replaceWith(convD);
  const n1Pages = {
    "cursor-1": { turns: [{ kind: "NOOP_KIND", text: "n1-page-1" }], nextCursor: "cursor-2" },
    "cursor-2": { turns: [{ kind: "NOOP_KIND", text: "n1-page-2" }], nextCursor: "" },
  };
  // Resolve each page fetch on a timer so hydration awaits across macrotasks —
  // the window in which the browser would fire the prepends' async scroll
  // events.
  listTurnsImpl = (sessionId, cursor) => new Promise((resolve) =>
    setTimeout(() => resolve(n1Pages[cursor] || { turns: [], nextCursor: "" }), 0));
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, noopEvents(TOTAL), "cursor-1"));
  const prependCalls = [];
  const dispatchedWhileHydrating = [];
  const origPrependD = R.prependOlderTurns.bind(R);
  R.prependOlderTurns = (turns) => {
    const result = origPrependD(turns);
    prependCalls.push(turns);
    // The browser fires the scroll event for a programmatic scrollTop write
    // ASYNC — simulate it on the next task, while hydration is still paging.
    setTimeout(() => {
      dispatchedWhileHydrating.push(R.hydrationInProgress);
      convD.dispatchEvent(new window.Event("scroll"));
    }, 0);
    return result;
  };
  R.init(convD);
  pass(await waitFor(() => R.appwireHydrated === true, 30000), "N1: multi-page hydration completed");
  R.prependOlderTurns = origPrependD;
  await pump(40); // let the prepend settles and their depth releases drain
  pass(prependCalls.length === 2,
    "N1: both older pages were prepended mid-hydration (got " + prependCalls.length + ")");
  pass(dispatchedWhileHydrating.length >= 1 && dispatchedWhileHydrating[0] === true,
    "N1: the prepend's async scroll event fired while hydration was still in progress");
  pass(convD.scrollTop === STUB_HEIGHT,
    "N1: prepend scroll writes did not read as reader intent — final settle scrolls to bottom (scrollTop=" + convD.scrollTop + ", want " + STUB_HEIGHT + ")");
  pass(R.programmaticScrollDepth === 0,
    "N1: programmatic scroll depth returned to 0 after the settles (got " + R.programmaticScrollDepth + ")");

  if (failures.length) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: hydration-end settle honors reader scroll intent (no yank; sticks when untouched)");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
