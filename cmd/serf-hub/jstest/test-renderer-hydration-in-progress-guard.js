// hydrationInProgress guard: while a chunked hydration replay is in flight,
// (F1) a reconnect landing mid-chunk must RESET the transcript before
// re-hydrating (not replay into the half-rendered prefix), (F3) the settings
// status-visibility toggle must not reset + re-render + declare hydration
// complete under the suspended replay, and (F4) a near-top scroll must not
// fetch the same older-turns cursor that loadOlderTurnsUntilPrimaryDialogue
// is about to page in.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
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

let notify = () => {};
let readThreadImpl = () => new Promise(() => {});
let listTurnsImpl = () => Promise.resolve({ turns: [], nextCursor: "" });

window.SerfAppwire = {
  tasks: () => new Promise(() => {}),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: (handler) => {
    notify = handler;
    return () => {};
  },
  onConnectionLost: () => () => {},
  readThread: (sessionId) => readThreadImpl(sessionId),
  listTurns: (sessionId, cursor, limit) => listTurnsImpl(sessionId, cursor, limit),
  eventsFromThread: (thread) => thread.testEvents || [],
  eventsFromTurns: (turns) => turns.map((t) => ["USER_INPUT", { text: t.text }]),
  eventsFromNotification: (method, params) => {
    if (method === "item/completed" && params && params.item && params.item.type === "userMessage") {
      return [["USER_INPUT", { text: params.item.text }]];
    }
    return [];
  },
};

require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const TOTAL = 300; // crosses the 150-event chunk boundary exactly once
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
const userMessageTexts = (el) =>
  Array.from(el.querySelectorAll(".user-message"))
    .map((m) => { const t = m.querySelector(".user-message-text"); return (t || m).textContent; });
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
  return el;
};
const deferred = () => {
  let resolve, reject;
  const p = new Promise((res, rej) => { resolve = res; reject = rej; });
  p.resolve = resolve;
  p.reject = reject;
  return p;
};

(async () => {
  // ── F1: reconnect landing mid-chunk must not duplicate the transcript ────
  // Hydration H1 renders its first slice and suspends. A reconnect (new
  // readThread for the SAME conversation) lands before H1 resumes: it must
  // reset the half-rendered transcript, not append a full replay on top of it.
  const conversationA = window.document.getElementById("conversation");
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("f1-event-", TOTAL)));
  R.init(conversationA);

  let reconnectFiredMidReplay = false;
  setTimeout(() => {
    // Fires after H1's first slice: the replay is suspended at its yield.
    reconnectFiredMidReplay = !R.appwireHydrated;
    R.connectAppwire(); // reconnect: same session, same conversation element
  }, 0);

  const f1Done = await waitFor(() =>
    R.appwireHydrated === true &&
    conversationA.querySelectorAll(".user-message").length >= TOTAL, 30000);
  await pump(20); // let any abandoned replay's tail settle before counting
  pass(f1Done, "F1: re-hydration after the mid-chunk reconnect completed");
  pass(reconnectFiredMidReplay, "F1: reconnect landed while the first hydration was still in progress");
  const f1Texts = userMessageTexts(conversationA);
  pass(f1Texts.length === TOTAL,
    "F1: no duplicated prefix after mid-chunk reconnect (got " + f1Texts.length + " messages, want " + TOTAL + ")");
  let f1InOrder = f1Texts.length === TOTAL;
  for (let i = 0; i < TOTAL && f1InOrder; i++) f1InOrder = f1Texts[i] === "f1-event-" + i;
  pass(f1InOrder, "F1: each transcript item appears exactly once, in hydration order");

  // ── F3: settings status-visibility toggle mid-chunk must not corrupt ─────
  // The toggle path resets + re-renders synchronously + sets appwireHydrated
  // with no chunking and no guard; the suspended chunked replay then resumes
  // and appends its remaining slices a second time.
  const conversationB = freshConversation("01F3");
  conversationA.replaceWith(conversationB);
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("f3-event-", TOTAL)));
  R.init(conversationB);

  let toggleFiredMidReplay = false;
  let toggleReturn = null;
  setTimeout(() => {
    toggleFiredMidReplay = !R.appwireHydrated;
    toggleReturn = R.refreshTranscriptStatusVisibility();
  }, 0);

  const f3Done = await waitFor(() => R.appwireHydrated === true, 30000);
  await pump(20); // the resumed replay's tail appends after the flag flips
  pass(f3Done, "F3: hydration completed around the mid-chunk settings toggle");
  pass(toggleFiredMidReplay, "F3: settings toggle fired while the chunked replay was in progress");
  pass(toggleReturn === false, "F3: the toggle defers to the in-progress hydration (returns false)");
  const f3Texts = userMessageTexts(conversationB);
  pass(f3Texts.length === TOTAL,
    "F3: no duplicated tail after the mid-chunk settings toggle (got " + f3Texts.length + " messages, want " + TOTAL + ")");
  let f3InOrder = f3Texts.length === TOTAL;
  for (let i = 0; i < TOTAL && f3InOrder; i++) f3InOrder = f3Texts[i] === "f3-event-" + i;
  pass(f3InOrder, "F3: each transcript item appears exactly once, in hydration order");

  // ── F4: near-top scroll mid-chunk must not double-prepend an older page ──
  // The replay seeds olderTurnsCursor up front; loadOlderTurnsUntilPrimary-
  // Dialogue owns it once the replay drains. A scroll fetch in between races
  // it on the SAME cursor: both prepend the same page.
  const conversationC = freshConversation("01F4");
  conversationB.replaceWith(conversationC);
  const page1 = { turns: [{ text: "f4-older-page-1" }], nextCursor: "" };
  const listTurnCalls = [];
  const listTurnDeferreds = [];
  listTurnsImpl = (sessionId, cursor) => {
    const d = deferred();
    listTurnCalls.push(cursor);
    listTurnDeferreds.push(d);
    return d;
  };
  // No primary dialogue in the hydrated window + an older cursor: hydration
  // pages older turns in after the replay drains.
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, noopEvents(TOTAL), "cursor-1"));
  const prependCalls = [];
  const origPrepend = R.prependOlderTurns.bind(R);
  R.prependOlderTurns = (turns) => { prependCalls.push(turns); return origPrepend(turns); };
  R.init(conversationC);

  let scrollFiredMidReplay = false;
  setTimeout(() => {
    scrollFiredMidReplay = !R.appwireHydrated;
    R.maybeLoadOlderTurns(); // reader near the top while the replay is suspended
  }, 0);

  const f4Fetched = await waitFor(() => listTurnCalls.length >= 1, 30000);
  pass(f4Fetched, "F4: older-turns paging started during hydration");
  await pump(20); // give any racing second fetch time to be issued
  for (const d of listTurnDeferreds) d.resolve(page1);
  const f4Done = await waitFor(() => R.appwireHydrated === true, 30000);
  await pump(20);
  pass(f4Done, "F4: hydration completed around the near-top scroll");
  pass(scrollFiredMidReplay, "F4: near-top scroll fired while the chunked replay was in progress");
  pass(listTurnCalls.length === 1,
    "F4: exactly one older-turns fetch (the scroll fetch defers to hydration's paging) — got " +
    listTurnCalls.length + " fetches for cursors " + JSON.stringify(listTurnCalls));
  const page1PrependCount = prependCalls.filter((turns) =>
    turns && turns.length === 1 && turns[0].text === "f4-older-page-1").length;
  pass(page1PrependCount === 1,
    "F4: the older page is prepended exactly once (got " + page1PrependCount + " prepends)");
  const page1Rendered = userMessageTexts(conversationC).filter((t) => t === "f4-older-page-1").length;
  pass(page1Rendered === 1,
    "F4: the older page renders into the transcript exactly once (got " + page1Rendered + ")");
  R.prependOlderTurns = origPrepend;

  if (failures.length) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: hydration-in-progress guard (reconnect reset, settings toggle, older-turns paging)");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
