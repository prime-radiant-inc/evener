// ask_user question card (spec §6/§6.1): a completed-but-unanswered ask_user
// call renders as one interactive amber card — recommended-first option
// chips, "Something else…"/"You decide"/"do that"/skip, a dim `why` line —
// aggregating every ask_user call in the turn under one global numbering.
// Pending is a pure transcript-shape rule (spec §6): a completed ack with no
// later user turn. Cold attach (a tight replay loop) and live attach (one
// event at a time) must agree, so every scenario below is exercised both
// ways.
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

// askCallEvents builds the realistic wire event pair for one completed
// ask_user call. arguments_json is deliberately placed ONLY on
// TOOL_CALL_START: the live item/completed push does not carry it
// (internal/appprojector/appwire_projection.go's EventToolCallEnd omits
// ArgumentsJSON), so a renderer that read it from TOOL_CALL_END would work in
// cold-replay tests and silently fail to render a live-arriving question —
// exactly the gap this fixture is shaped to catch.
function askCallEvents(callId, questions, opts) {
  opts = opts || {};
  const start = ["TOOL_CALL_START", { call_id: callId, tool_name: "ask_user", arguments_json: JSON.stringify({ questions }) }];
  if (opts.noEnd) return [start];
  const end = ["TOOL_CALL_END", { call_id: callId, tool_name: "ask_user", output: opts.error ? "" : "questions posted; answers arrive in the user's reply after your turn ends", error: opts.error || "" }];
  return [start, end];
}

async function settle() { await new Promise((r) => setTimeout(r, 30)); }

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

async function testPendingCardRenders() {
  const { conv, R } = newHarness();
  await settle();
  const question = {
    header: "DB choice",
    question: "Which datastore for the ingest path?",
    options: [
      { label: "SQLite", detail: "zero setup; diverges from prod" },
      { label: "Postgres", detail: "matches prod; heavier local setup", recommended: true },
    ],
    multi_select: false,
    why: "the writer refactor depends on it",
    if_unanswered: "default to Postgres and note the assumption in the PR",
  };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);

  const card = conv.querySelector(".ask-card");
  pass(!!card, "pending ask renders a .ask-card");
  pass(card && card.classList.contains("agent-question"), "the card carries the amber .agent-question frame");
  pass(!!conv.querySelector(".agent-question-head"), "markAgentQuestion's amber head is present");

  const options = card ? Array.from(card.querySelectorAll("[data-ask-option]")) : [];
  pass(options.length === 2, "renders one chip per option, got " + options.length);
  pass(options[0] && options[0].dataset.optionLabel === "Postgres", "the recommended option renders FIRST regardless of input order, got " + (options[0] && options[0].dataset.optionLabel));
  pass(options[0] && options[0].classList.contains("recommended"), "the first (recommended) chip carries .recommended");
  pass(options[0] && !!options[0].querySelector(".ask-option-tag"), "the recommended chip shows a tag");
  pass(options[1] && options[1].dataset.optionLabel === "SQLite" && !options[1].classList.contains("recommended"), "the non-recommended option is not tagged");

  pass(!!card.querySelector("[data-ask-free-toggle]"), "a 'Something else…' free-text row is offered");
  pass(/something else/i.test(card.querySelector("[data-ask-free-toggle]").textContent), "free-text toggle reads 'Something else…'");
  pass(!!card.querySelector("[data-ask-decide-toggle]"), "a 'You decide' row is offered");
  pass(/you decide/i.test(card.querySelector("[data-ask-decide-toggle]").textContent), "decide toggle reads 'You decide'");
  pass(!!card.querySelector("[data-ask-decide-leaning]"), "the leaning field exists for 'You decide'");
  const fallbackBtn = card.querySelector("[data-ask-fallback-btn]");
  pass(!!fallbackBtn, "a 'do that' fallback button renders when if_unanswered is present");
  pass(fallbackBtn && fallbackBtn.textContent.includes("default to Postgres"), "the fallback button surfaces the model's stated fallback text");
  pass(!!card.querySelector("[data-ask-skip-btn]"), "a skip affordance is offered");
  const why = card.querySelector("[data-ask-why]");
  pass(!!why && why.textContent === "the writer refactor depends on it", "the `why` context line renders dim/verbatim");
}

// A question with no if_unanswered must NOT get a fallback button.
async function testNoFallbackWithoutIfUnanswered() {
  const { conv, R } = newHarness();
  await settle();
  const question = { header: "Naming", question: "What should we call it?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  pass(!conv.querySelector("[data-ask-fallback-btn]"), "no fallback button when if_unanswered is absent");
}

async function testMultiCardAggregationAndGlobalNumbering() {
  const { conv, R } = newHarness();
  await settle();
  const q = (header) => ({ header, question: header + "?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] });
  // Two ask_user calls in the same round, no user turn in between: they must
  // aggregate into ONE card, numbered 1..4 across BOTH calls in call order.
  for (const [kind, data] of askCallEvents("call_A", [q("First"), q("Second")])) R.handleData(kind, data);
  for (const [kind, data] of askCallEvents("call_B", [q("Third"), q("Fourth")])) R.handleData(kind, data);

  const cards = conv.querySelectorAll(".ask-card");
  pass(cards.length === 1, "two ask_user calls with no intervening reply aggregate into ONE card, got " + cards.length);
  const nums = Array.from(conv.querySelectorAll("[data-ask-question] .ask-question-num")).map((n) => n.textContent);
  pass(nums.join(",") === "1.,2.,3.,4.", "questions are numbered globally 1..4 across both calls, got " + nums.join(","));
  const headers = Array.from(conv.querySelectorAll(".ask-question-header")).map((n) => n.textContent);
  pass(headers.join(",") === "First,Second,Third,Fourth", "questions keep call-then-question posting order, got " + headers.join(","));
}

async function testCollapsesToSettledLineOnUserTurn() {
  const { conv, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  pass(!!conv.querySelector(".ask-card"), "sanity: card is pending before the reply");

  // A reply — from THIS client or another, the wire event is the same
  // USER_INPUT push either way (spec §6.1).
  R.handleData("USER_INPUT", { text: "[answers]\n1. [DB choice] → \"Postgres\"" });

  pass(!conv.querySelector(".ask-card"), "the interactive card is gone once a user turn follows");
  const settledLine = conv.querySelector(".ask-settled-line");
  pass(!!settledLine, "a neutral settled line replaces the card");
  pass(settledLine && /DB choice/.test(settledLine.textContent), "the settled line still names what was asked, got: " + (settledLine && settledLine.textContent));
  pass(settledLine && /Postgres/.test(settledLine.textContent), "the settled line echoes the reply, got: " + (settledLine && settledLine.textContent));
  pass(R.pendingAsk === null, "pendingAsk is cleared once resolved");
}

// The composer is always the escape hatch (spec §6.1): typing prose directly
// into it — never touching the card's own chips — must resolve a pending ask
// immediately, the same as the card's own "Send answers" button, not just
// after a later round-trip echo.
async function testTypingIntoPlainComposerResolvesPendingCard() {
  const { window, conv, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  pass(!!conv.querySelector(".ask-card"), "sanity: card is pending before the reply");

  window.SerfAppwire = { startTurn: () => Promise.resolve({ turn: { id: "t1" } }) };
  const form = window.document.querySelector("form[data-input-form]");
  const ta = form.querySelector(".message-input");
  ta.value = "let's go with Postgres";
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await settle();

  pass(!conv.querySelector(".ask-card"), "typing straight into the composer resolves the card immediately, without waiting for a later USER_INPUT echo");
  pass(!!conv.querySelector(".ask-settled-line"), "a settled line is left behind");
  pass(R.pendingAsk === null, "pendingAsk is cleared");
}

// multi_select renders checkboxes (not radios) and accumulates every checked
// label into one "option" resolution; unchecking removes just that label,
// and unchecking the last one clears the resolution entirely.
async function testMultiSelectAccumulatesCheckedLabels() {
  const { conv, R } = newHarness();
  await settle();
  const question = {
    header: "Stack", question: "Which layers?", multi_select: true,
    options: [{ label: "API", detail: "" }, { label: "Worker", detail: "" }, { label: "CLI", detail: "" }],
  };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  const q = conv.querySelector("[data-ask-question]");
  const inputs = Array.from(q.querySelectorAll("[data-ask-option-input]"));
  pass(inputs.every((i) => i.type === "checkbox"), "multi_select renders checkbox inputs, got types " + inputs.map((i) => i.type).join(","));

  const byLabel = (label) => q.querySelector('[data-ask-option][data-option-label="' + label + '"] [data-ask-option-input]');
  const check = (label, value) => {
    const input = byLabel(label);
    input.checked = value;
    input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
  };

  check("API", true);
  check("Worker", true);
  const item1 = R.pendingAsk.items[0];
  pass(item1.resolution && item1.resolution.kind === "option" && item1.resolution.labels.slice().sort().join(",") === "API,Worker",
    "checking two boxes accumulates both labels, got " + JSON.stringify(item1.resolution));

  check("Worker", false);
  pass(item1.resolution && item1.resolution.labels.join(",") === "API", "unchecking one box removes just that label, got " + JSON.stringify(item1.resolution));

  check("API", false);
  pass(item1.resolution === null, "unchecking the last box clears the resolution entirely, got " + JSON.stringify(item1.resolution));
}

// Esc collapses the still-pending card to a "◆ question waiting" chip
// without discarding any in-progress answer; clicking the chip restores it
// (spec §6.1).
async function testEscCollapsesToChipAndExpandsBack() {
  const { window, conv, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  const card = conv.querySelector(".ask-card");
  pickOption(card, "Postgres");

  window.document.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  pass(card.querySelector("[data-ask-questions]").hidden === true, "Esc hides the questions list");
  pass(card.querySelector(".ask-footer").hidden === true, "Esc hides the footer");
  const chip = card.querySelector("[data-ask-collapsed-chip]");
  pass(!!chip && chip.hidden === false, "Esc reveals the collapsed '◆ question waiting' chip");
  pass(R.pendingAsk !== null, "collapsing is purely visual — the pending ask itself is untouched");
  pass(R.pendingAsk.items[0].resolution && R.pendingAsk.items[0].resolution.labels[0] === "Postgres",
    "the in-progress answer survives a collapse");

  chip.click();
  pass(card.querySelector("[data-ask-questions]").hidden === false, "clicking the chip restores the questions list");
  pass(card.querySelector(".ask-footer").hidden === false, "clicking the chip restores the footer");
  pass(chip.hidden === true, "clicking the chip hides itself again");
}

function pickOption(card, label) {
  const input = card.querySelector('[data-ask-option][data-option-label="' + label + '"] [data-ask-option-input]');
  input.checked = true;
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

// Cold-attach: pending vs resolved vs interrupted (ack-less), replayed as a
// tight loop the way eventsFromThread's output would be — must agree with
// the live, one-event-at-a-time behavior exercised by the tests above.
async function testColdAttachPendingResolvedInterrupted() {
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };

  // Pending: a completed ack, no later user turn.
  {
    const { conv, R } = newHarness();
    await settle();
    const events = askCallEvents("call_1", [question]);
    for (const [kind, data] of events) R.handleData(kind, data);
    pass(!!conv.querySelector(".ask-card"), "cold attach: a completed ask with no later reply renders pending");
    pass(R.pendingAsk !== null, "cold attach: pendingAsk is set");
  }

  // Resolved: a completed ack, replayed together with the resolving reply.
  {
    const { conv, R } = newHarness();
    await settle();
    const events = askCallEvents("call_1", [question]).concat([
      ["USER_INPUT", { text: "Postgres, please" }],
    ]);
    for (const [kind, data] of events) R.handleData(kind, data);
    pass(!conv.querySelector(".ask-card"), "cold attach: an already-answered ask does not render an interactive card");
    pass(!!conv.querySelector(".ask-settled-line"), "cold attach: an already-answered ask renders the settled line");
    pass(R.pendingAsk === null, "cold attach: pendingAsk is null once resolved");
  }

  // Interrupted / ack-less: TOOL_CALL_START with no matching TOOL_CALL_END
  // (the turn was interrupted before the ack was ever recorded) — no ghost
  // card (spec §6: "an ask whose turn was interrupted before the ack exists
  // is never pending").
  {
    const { conv, R } = newHarness();
    await settle();
    const events = askCallEvents("call_1", [question], { noEnd: true });
    for (const [kind, data] of events) R.handleData(kind, data);
    pass(!conv.querySelector(".ask-card"), "cold attach: an ack-less (interrupted) ask renders NO card");
    pass(!conv.querySelector(".ask-settled-line"), "cold attach: an ack-less ask renders no settled line either");
    pass(R.pendingAsk === null, "cold attach: pendingAsk stays null for an ack-less ask");
  }

  // A denied/errored ack (PreToolUse deny, or an invalid call) also posts
  // nothing (spec §5.1/§5.5) — same no-card outcome as ack-less.
  {
    const { conv, R } = newHarness();
    await settle();
    const events = askCallEvents("call_1", [question], { error: "denied by hook" });
    for (const [kind, data] of events) R.handleData(kind, data);
    pass(!conv.querySelector(".ask-card"), "cold attach: a denied/errored ask renders NO card");
    pass(R.pendingAsk === null, "cold attach: pendingAsk stays null for a denied ask");
  }
}

(async () => {
  await testPendingCardRenders();
  await testNoFallbackWithoutIfUnanswered();
  await testMultiCardAggregationAndGlobalNumbering();
  await testCollapsesToSettledLineOnUserTurn();
  await testTypingIntoPlainComposerResolvesPendingCard();
  await testMultiSelectAccumulatesCheckedLabels();
  await testEscCollapsesToChipAndExpandsBack();
  await testColdAttachPendingResolvedInterrupted();

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: ask_user question card renders, aggregates, and resolves per the transcript-shape rule");
  process.exit(0);
})();
