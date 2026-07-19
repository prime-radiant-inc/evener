// Hydration replay must not run as one giant blocking task: the readThread
// .then loop is chunked, yielding a macrotask every slice so a setTimeout(0)
// scheduled before driving hydration fires mid-replay. Live notifications
// arriving during the (now wider) hydration window must buffer and replay
// AFTER the hydrated content, and a session swap mid-chunk must abort cleanly.
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
  eventsFromThread: (thread) => thread.testEvents || [],
  eventsFromNotification: (method, params) => {
    if (method === "item/completed" && params && params.item && params.item.type === "userMessage") {
      return [["USER_INPUT", { text: params.item.text }]];
    }
    return [];
  },
};

require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
const conversation = window.document.getElementById("conversation");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const userEvents = (prefix, count) => {
  const events = [];
  for (let i = 0; i < count; i++) events.push(["USER_INPUT", { text: prefix + i }]);
  return events;
};
const threadWith = (sessionId, events) => ({
  thread: {
    id: sessionId,
    sessionId,
    status: "inProgress",
    serf: { ref: "local:" + sessionId, activeTurnId: "turn_active" },
    turns: [],
    testEvents: events,
  },
});
const userMessageTexts = (el) =>
  Array.from(el.querySelectorAll(".user-message"))
    .map((m) => { const t = m.querySelector(".user-message-text"); return (t || m).textContent; });
// Wait for condition with a macrotask pump so chunked replay can proceed.
const waitFor = async (cond, timeoutMs) => {
  const deadline = Date.now() + (timeoutMs || 5000);
  while (!cond()) {
    if (Date.now() > deadline) return false;
    await new Promise((r) => setTimeout(r, 5));
  }
  return true;
};

(async () => {
  // ── (a) responsiveness + (b) ordering ────────────────────────────────────
  const TOTAL = 2000;
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("event-", TOTAL)));
  R.init(conversation);

  let childrenAtTimer = -1;
  let hydratedFlagAtTimer = null;
  // Scheduled BEFORE the hydration replay can finish: with a chunked replay
  // this fires between slices; with one giant blocking task it fires only
  // after every event has rendered.
  setTimeout(() => {
    childrenAtTimer = conversation.querySelectorAll(".user-message").length;
    hydratedFlagAtTimer = R.appwireHydrated;
    // (b) a live notification landing mid-replay must buffer and appear
    // AFTER the hydrated content, never interleaved into it.
    notify("item/completed", {
      ref: "local:01TEST",
      threadId: "01TEST",
      turnId: "turn_live",
      item: { id: "user_live", type: "userMessage", text: "live-during-hydration" },
    });
  }, 0);

  const finished = await waitFor(() => R.appwireHydrated === true, 60000);
  pass(finished, "initial hydration completed");
  pass(childrenAtTimer > 0, "timer fired after the replay started (some events rendered)");
  pass(childrenAtTimer >= 0 && childrenAtTimer < TOTAL,
    "setTimeout(0) fired BEFORE all " + TOTAL + " events replayed (rendered at timer fire: " + childrenAtTimer + ") — replay is one blocking task");
  pass(hydratedFlagAtTimer === false,
    "live notifications are gated (appwireHydrated false) while the replay is in progress");

  const texts = userMessageTexts(conversation);
  pass(texts.length === TOTAL + 1, "transcript holds every hydrated event plus the live one (got " + texts.length + ")");
  let inOrder = texts.length >= TOTAL;
  for (let i = 0; i < TOTAL && inOrder; i++) inOrder = texts[i] === "event-" + i;
  pass(inOrder, "final transcript order matches the hydration event order");
  pass(texts[texts.length - 1] === "live-during-hydration",
    "live event during hydration appears only AFTER the hydrated content");

  // ── (c) abort: swapping the session mid-chunk stops the replay ───────────
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId,
    sessionId === "session-C" ? userEvents("c-event-", 1) : userEvents("event-", TOTAL)));
  const conversationB = window.document.createElement("div");
  conversationB.dataset.sessionId = "session-B";
  conversationB.dataset.state = "active";
  conversation.replaceWith(conversationB);
  R.init(conversationB);

  const conversationC = window.document.createElement("div");
  conversationC.dataset.sessionId = "session-C";
  conversationC.dataset.state = "active";
  let swapThrew = null;
  // Fires after session-B's first slice: swap the session out from under the
  // suspended replay. The post-yield staleness guard must abort it cleanly.
  setTimeout(() => {
    try {
      conversationB.replaceWith(conversationC);
      R.init(conversationC);
    } catch (err) {
      swapThrew = err;
    }
  }, 0);

  const finishedC = await waitFor(() => R.appwireHydrated === true && R.sessionId === "session-C", 30000);
  pass(finishedC, "session-C hydration completed after the mid-chunk swap");
  pass(swapThrew === null, "session swap mid-chunk threw: " + (swapThrew && swapThrew.stack));
  const bRendered = conversationB.querySelectorAll(".user-message").length;
  pass(bRendered > 0 && bRendered < TOTAL,
    "abandoned session-B replay stopped mid-chunk without completing (rendered: " + bRendered + ")");
  assert.deepStrictEqual(userMessageTexts(conversationC), ["c-event-0"],
    "session-C transcript is exactly its own hydration");

  // ── (d) reconnect: buffered live events replay after re-hydration ────────
  const RE_EVENTS = 400;
  readThreadImpl = (sessionId) => Promise.resolve(threadWith(sessionId, userEvents("re-event-", RE_EVENTS)));
  pass(R.appwireHydrated === true, "precondition: stream hydrated before reconnect");
  R.connectAppwire(); // reconnect: re-reads the thread and re-hydrates

  let gatedDuringRehydration = null;
  let renderedDuringRehydration = -1;
  setTimeout(() => {
    gatedDuringRehydration = R.appwireHydrated;
    renderedDuringRehydration = conversationC.querySelectorAll(".user-message").length;
    notify("item/completed", {
      ref: "local:session-C",
      threadId: "session-C",
      turnId: "turn_live_2",
      item: { id: "user_live_2", type: "userMessage", text: "live-after-reconnect" },
    });
  }, 0);

  const reFinished = await waitFor(() =>
    R.appwireHydrated === true &&
    conversationC.querySelectorAll(".user-message").length >= RE_EVENTS + 1, 30000);
  pass(reFinished, "re-hydration after reconnect completed");
  pass(gatedDuringRehydration === false,
    "re-hydration gated live delivery (appwireHydrated false during the chunked replay)");
  pass(renderedDuringRehydration >= 0 && renderedDuringRehydration < RE_EVENTS,
    "re-hydration replay was still in progress when the live event arrived (rendered: " + renderedDuringRehydration + ")");

  const reTexts = userMessageTexts(conversationC);
  pass(reTexts.length === RE_EVENTS + 1,
    "single coherent transcript after reconnect, no duplicates (got " + reTexts.length + ")");
  let reInOrder = reTexts.length >= RE_EVENTS;
  for (let i = 0; i < RE_EVENTS && reInOrder; i++) reInOrder = reTexts[i] === "re-event-" + i;
  pass(reInOrder, "re-hydrated transcript order matches the event order");
  pass(reTexts[reTexts.length - 1] === "live-after-reconnect",
    "buffered live event replayed after the re-hydrated content");
  pass(reTexts.filter((t) => t === "live-after-reconnect").length === 1,
    "buffered live event delivered exactly once");

  if (failures.length) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: hydration replay is chunked (responsive), ordered, abort-safe, and reconnect-coherent");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
