// ask_user submit discipline (spec §6.1, "round 4"): before sending, re-check
// whether a reply has already resolved the card — if so, collapse instead of
// sending. A turn/start Conflict (another client's reply won the race) is
// never auto-retried: the composed text drops into the composer for the user
// to decide. Both paths reuse the exact function the ordinary composer uses
// to send a turn (window.SerfAppwire.startTurn), never a parallel send path.
const { JSDOM } = require("jsdom");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="awaiting"></div>
    <form data-input-form data-session-id="01TEST">
      <div class="task-status-row">
        <button type="button" class="status-item tasks-status" data-tasks-trigger title="task list"><span class="status-key">tasks</span><span class="status-value" data-task-status-text>loading…</span></button>
      </div>
      <textarea class="message-input"></textarea>
      <button type="submit" class="btn btn-primary send-btn" data-capability-send="true" data-capability-queue="false">send</button>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: (t) => String(t == null ? "" : t) };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv, R: window.SerfRenderer };
}

function askCallEvents(callId, questions) {
  return [
    ["TOOL_CALL_START", { call_id: callId, tool_name: "ask_user", arguments_json: JSON.stringify({ questions }) }],
    ["TOOL_CALL_END", { call_id: callId, tool_name: "ask_user", output: "questions posted; answers arrive in the user's reply after your turn ends" }],
  ];
}

async function settle() { await new Promise((r) => setTimeout(r, 30)); }

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const ONE_QUESTION = [{ header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] }];

function pickFirstOption(card) {
  card.querySelector("[data-ask-option-input]").checked = true;
  card.querySelector("[data-ask-option-input]").dispatchEvent(new card.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

// A user turn already followed the ask (this client saw the USER_INPUT that
// resolved it — a suspended tab waking up, or another client's reply pushed
// over the live stream) by the time Send is finally pressed: the recheck
// must collapse instead of sending, and startTurn must never be called.
async function testRecheckCollapsesInsteadOfSending() {
  const { conv, window, R } = newHarness();
  await settle();
  for (const [kind, data] of askCallEvents("call_1", ONE_QUESTION)) R.handleData(kind, data);
  const card = conv.querySelector(".ask-card");
  pickFirstOption(card);
  // Hold a reference to the Send button as it existed while still pending —
  // exactly what a slow human hand does: the click lands after the reply
  // already arrived and collapsed the card underneath it.
  const sendBtn = card.querySelector("[data-ask-send-btn]");

  const startTurnCalls = [];
  window.SerfAppwire = { startTurn: (ref, text) => { startTurnCalls.push({ ref, text }); return Promise.resolve({ turn: { id: "t1" } }); } };

  // The resolving reply lands first (spec §6.1: "from this client or
  // another" — the wire event is identical either way).
  R.handleData("USER_INPUT", { text: "Postgres" });
  pass(!conv.querySelector(".ask-card"), "sanity: the card already collapsed before Send was pressed");

  sendBtn.click();
  await settle();
  pass(startTurnCalls.length === 0, "a stale Send press after the ask already resolved must NOT call startTurn, got " + startTurnCalls.length + " call(s)");
}

// A turn/start Conflict (another client's reply won the daemon's atomic
// reservation) must never be retried automatically, and the composed text
// must land in the composer for the user to decide (spec §6.1).
async function testConflictDropsTextIntoComposerNoRetry() {
  const { conv, window, R } = newHarness();
  await settle();
  for (const [kind, data] of askCallEvents("call_1", ONE_QUESTION)) R.handleData(kind, data);
  const card = conv.querySelector(".ask-card");
  pickFirstOption(card);

  const startTurnCalls = [];
  window.SerfAppwire = {
    startTurn: (ref, text) => {
      startTurnCalls.push({ ref, text });
      const err = new Error("turn is not active");
      err.serfErrorInfo = "conflict";
      err.code = -32013;
      return Promise.reject(err);
    },
  };

  card.querySelector("[data-ask-send-btn]").click();
  await settle();

  pass(startTurnCalls.length === 1, "Conflict must not be retried — expected exactly 1 startTurn call, got " + startTurnCalls.length);
  const ta = window.document.querySelector(".message-input");
  const expected = "[answers]\n1. [DB choice] → \"Postgres\"";
  pass(ta.value === expected, "the composed text must land in the composer on Conflict.\n  want: " + JSON.stringify(expected) + "\n  got:  " + JSON.stringify(ta.value));
  pass(!!conv.querySelector(".ask-card"), "the card stays pending/interactive after a Conflict (no reply has actually resolved it yet from this client's view)");

  // No automatic retry happens on its own.
  await settle();
  pass(startTurnCalls.length === 1, "no automatic second send happens on its own after the Conflict, got " + startTurnCalls.length);

  // The user's OWN decision to try again — a manual second click — must
  // still work and must not be blocked by the failed attempt's bookkeeping
  // (e.g. a "sending" flag left stuck true).
  window.SerfAppwire.startTurn = (ref, text) => { startTurnCalls.push({ ref, text }); return Promise.resolve({ turn: { id: "t2" } }); };
  card.querySelector("[data-ask-send-btn]").click();
  await settle();
  pass(startTurnCalls.length === 2, "a manual second Send press after a Conflict must go through, got " + startTurnCalls.length + " call(s)");
  pass(!conv.querySelector(".ask-card"), "the manual retry resolves the card once it succeeds");
}

// Submit reuses the exact function/endpoint the ordinary composer uses.
async function testSendReusesComposerSendPath() {
  const { conv, window, R } = newHarness();
  await settle();
  for (const [kind, data] of askCallEvents("call_1", ONE_QUESTION)) R.handleData(kind, data);
  const card = conv.querySelector(".ask-card");
  pickFirstOption(card);

  const startTurnCalls = [];
  R.appwireRef = "ref:01TEST";
  window.SerfAppwire = { startTurn: (ref, text, items) => { startTurnCalls.push({ ref, text, items }); return Promise.resolve({ turn: { id: "t1" } }); } };

  card.querySelector("[data-ask-send-btn]").click();
  await settle();

  pass(startTurnCalls.length === 1, "expected exactly one startTurn call, got " + startTurnCalls.length);
  const call = startTurnCalls[0];
  pass(call && call.ref === "ref:01TEST", "startTurn must be called with the session's appwire ref, got " + (call && call.ref));
  pass(call && call.text === "[answers]\n1. [DB choice] → \"Postgres\"", "startTurn must carry the composed §4.3 text verbatim, got " + JSON.stringify(call && call.text));
  pass(!conv.querySelector(".ask-card"), "a successful send resolves (collapses) the card just like any accepted reply");
  pass(!!conv.querySelector(".ask-settled-line"), "a successful send leaves the settled line behind");
}

(async () => {
  await testRecheckCollapsesInsteadOfSending();
  await testConflictDropsTextIntoComposerNoRetry();
  await testSendReusesComposerSendPath();

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: ask_user submit discipline — recheck-collapse, Conflict never retried, same send path as the composer");
  process.exit(0);
})();
