// ask_user responses (spec §6/§6.1): a completed-but-unanswered ask_user
// call leaves a compact transcript anchor and puts the one interactive response
// form in the composer dock. Pending remains a transcript-shape rule: a
// completed ack with no later user turn. Cold attach (a tight replay loop) and
// live attach must therefore agree.
const { JSDOM } = require("jsdom");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="awaiting"></div>
    <form data-input-form data-session-id="01TEST">
      <div data-composer-surface>
        <div class="task-status-row">
          <button type="button" class="status-item tasks-status" data-tasks-trigger title="task list"><span class="status-key">tasks</span><span class="status-value" data-task-status-text>loading…</span></button>
        </div>
        <textarea class="message-input"></textarea>
        <button type="submit" class="btn btn-primary send-btn" data-capability-send="true" data-capability-queue="false">send</button>
      </div>
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

// arguments_json deliberately exists only on TOOL_CALL_START: the live END
// event does not carry it, so this verifies the renderer caches live starts.
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
const dock = (window) => window.document.querySelector("form[data-input-form] [data-ask-response-dock]");
const questionEls = (window) => Array.from((dock(window) || window.document).querySelectorAll("[data-ask-question]"));

async function testPendingDockRenders() {
  const { window, conv, R } = newHarness();
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

  const anchor = conv.querySelector("[data-ask-anchor]");
  const responseDock = dock(window);
  const form = window.document.querySelector("form[data-input-form]");
  const composer = form.querySelector("[data-composer-surface]");
  const input = form.querySelector(".message-input");
  pass(!!anchor, "pending ask leaves a compact transcript [data-ask-anchor]");
  pass(!conv.querySelector(".ask-card"), "the transcript has no retired interactive .ask-card");
  pass(!!responseDock, "the one interactive response form is [data-ask-response-dock] inside the input form");
  pass(responseDock && responseDock.parentElement === form, "the response dock is owned by form[data-input-form]");
  pass(composer.hidden && composer.inert, "the normal [data-composer-surface] is hidden and inert while an ask is pending");
  pass(input.hidden && input.inert, "the normal textarea is unavailable while the dock owns the response");
  if (!responseDock) return;

  const options = Array.from(responseDock.querySelectorAll("[data-ask-option]"));
  pass(options.length === 2, "the dock renders one chip per option, got " + options.length);
  pass(options[0] && options[0].dataset.optionLabel === "Postgres", "the recommended option renders FIRST regardless of input order");
  pass(options[0] && options[0].classList.contains("recommended"), "the first option carries .recommended");
  pass(options[0] && !!options[0].querySelector(".ask-option-tag"), "the recommended option shows a tag");
  pass(options[1] && options[1].dataset.optionLabel === "SQLite" && !options[1].classList.contains("recommended"), "the non-recommended option is not tagged");
  pass(!!responseDock.querySelector("[data-ask-free-toggle]"), "the dock offers 'Something else…'");
  pass(!!responseDock.querySelector("[data-ask-decide-toggle]"), "the dock offers 'You decide'");
  pass(!!responseDock.querySelector("[data-ask-decide-leaning]"), "the dock has the optional leaning field");
  const fallbackBtn = responseDock.querySelector("[data-ask-fallback-btn]");
  pass(!!fallbackBtn, "the dock renders a fallback when if_unanswered is present");
  pass(fallbackBtn && fallbackBtn.textContent.includes("default to Postgres"), "the fallback exposes the stated fallback text");
  pass(!!responseDock.querySelector("[data-ask-skip-btn]"), "the dock offers skip");
  const why = responseDock.querySelector("[data-ask-why]");
  pass(!!why && why.textContent === "the writer refactor depends on it", "the dock renders the why line verbatim");
}

async function testAskDockFocusAnnouncementsAndInputNames() {
  const { window, R } = newHarness();
  await settle();
  const question = {
    header: "DB choice",
    question: "Which datastore?",
    options: [{ label: "Postgres", detail: "" }],
  };
  for (const [kind, data] of askCallEvents("call_focus", [question])) R.handleData(kind, data);

  const responseDock = dock(window);
  const qEl = questionEls(window)[0];
  const questionText = qEl && qEl.querySelector(".ask-question-text");
  const firstOption = qEl && qEl.querySelector("[data-ask-option-input]");
  const freeInput = qEl && qEl.querySelector("[data-ask-free-input]");
  const leaningInput = qEl && qEl.querySelector("[data-ask-decide-leaning]");
  const noteInput = qEl && qEl.querySelector("[data-ask-note-field]");
  const status = window.document.querySelector("[data-composer-mode-status]");

  pass(!!responseDock, "sanity: pending ask mounts a response dock for focus and accessibility checks");
  pass(window.document.activeElement === firstOption,
    "entering ask mode focuses the first available answer control");
  pass(status && status.getAttribute("aria-live") === "polite" && /answer the agent.?s questions/i.test(status.textContent),
    "entering ask mode announces the response mode through a concise polite live region");

  for (const [input, kind] of [[freeInput, "free answer"], [leaningInput, "leaning"], [noteInput, "note"]]) {
    const ids = input && (input.getAttribute("aria-labelledby") || "").trim().split(/\s+/);
    pass(!!(ids && questionText && questionText.id && ids.includes(questionText.id)),
      kind + " input programmatically names its question with aria-labelledby");
    pass(!!(ids && ids.length > 1 && ids.every((id) => window.document.getElementById(id))),
      kind + " input aria-labelledby references existing label elements");
  }

  qEl.querySelector("[data-ask-free-toggle]").click();
  pass(window.document.activeElement === freeInput, "free-answer activation focuses the editable free-text input");
  for (const [kind, data] of askCallEvents("call_focus_more", [{ header: "Follow-up", question: "Any constraint?", options: [{ label: "No", detail: "" }] }])) {
    R.handleData(kind, data);
  }
  pass(window.document.activeElement === window.document.querySelector('[data-ask-question][data-ask-key="call_focus:0"] [data-ask-free-input]'),
    "adding questions does not steal focus away from an answer input being edited");

  R.resolvePendingAsk("[answers]");
  pass(status && /message composer (is )?ready|composer restored/i.test(status.textContent),
    "restoring the normal composer announces the mode transition through the live region");
}

async function testPendingAskBlocksNormalComposerSubmission() {
  const { window, R } = newHarness();
  await settle();
  const form = window.document.querySelector("form[data-input-form]");
  const composer = form.querySelector(".message-input");
  composer.value = "old composer draft";
  for (const [kind, data] of askCallEvents("call_submit_guard", [{ header: "Choice", question: "Choose", options: [{ label: "A", detail: "" }] }])) {
    R.handleData(kind, data);
  }
  const calls = [];
  window.SerfAppwire = { startTurn: (ref, text) => { calls.push({ ref, text }); return Promise.resolve({ turn: { id: "bad" } }); } };
  const accepted = form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await settle();

  pass(accepted === false, "implicit form submission from an ask input is prevented");
  pass(calls.length === 0, "a pending ask blocks the normal composer startTurn path instead of sending an old draft");
  pass(R.pendingAsk !== null && !!dock(window), "blocked implicit submission leaves the pending ask dock active");
  pass(composer.value === "old composer draft", "blocked implicit submission preserves the old normal composer draft without settling the ask");
}

async function testReplayAndSessionReplacementTearDownAskDock() {
  const question = { header: "Choice", question: "Choose", options: [{ label: "A", detail: "" }] };
  {
    const { window, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_replay", [question])) R.handleData(kind, data);
    R.resetTranscriptReplay();
    const form = window.document.querySelector("form[data-input-form]");
    pass(R.pendingAsk === null && !dock(window), "resetTranscriptReplay removes a pending ask dock rather than only clearing its state");
    pass(!form.querySelector("[data-composer-surface]").hidden && !form.querySelector(".message-input").hidden,
      "resetTranscriptReplay restores the normal composer after tearing down an ask dock");
  }
  {
    const { window, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_session", [question])) R.handleData(kind, data);
    R.handleData("SESSION_START", { session_id: "01REPLACED", status: "idle" });
    const form = window.document.querySelector("form[data-input-form]");
    pass(R.pendingAsk === null && !dock(window), "a changed-session SESSION_START removes a pending ask dock rather than only clearing its state");
    pass(!form.querySelector("[data-composer-surface]").hidden && !form.querySelector(".message-input").hidden,
      "a changed-session SESSION_START restores the normal composer after tearing down an ask dock");
  }
}

async function testNoFallbackWithoutIfUnanswered() {
  const { window, R } = newHarness();
  await settle();
  const question = { header: "Naming", question: "What should we call it?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  const responseDock = dock(window);
  pass(!!responseDock, "sanity: pending ask provides the response dock");
  if (responseDock) pass(!responseDock.querySelector("[data-ask-fallback-btn]"), "no dock fallback when if_unanswered is absent");
}

async function testAggregationAndGlobalNumbering() {
  const { window, conv, R } = newHarness();
  await settle();
  const q = (header) => ({ header, question: header + "?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] });
  for (const [kind, data] of askCallEvents("call_A", [q("First"), q("Second")])) R.handleData(kind, data);
  for (const [kind, data] of askCallEvents("call_B", [q("Third"), q("Fourth")])) R.handleData(kind, data);

  pass(conv.querySelectorAll("[data-ask-anchor]").length === 1, "two calls with no reply keep one transcript anchor");
  pass(window.document.querySelectorAll("[data-ask-response-dock]").length === 1, "two calls share one response dock");
  if (!dock(window)) return;
  const nums = questionEls(window).map((qEl) => qEl.querySelector(".ask-question-num").textContent);
  pass(nums.join(",") === "1.,2.,3.,4.", "questions are globally numbered 1..4 across both calls, got " + nums.join(","));
  const headers = questionEls(window).map((qEl) => qEl.querySelector(".ask-question-header").textContent);
  pass(headers.join(",") === "First,Second,Third,Fourth", "questions keep call-then-question posting order, got " + headers.join(","));
}

async function testUserInputSettlesAnchorAndRestoresComposer() {
  const { window, conv, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  pass(!!dock(window), "sanity: the response dock is pending before the reply");

  R.handleData("USER_INPUT", { text: "[answers]\n1. [DB choice] → \"Postgres\"" });

  pass(!dock(window), "USER_INPUT removes the interactive response dock");
  pass(!conv.querySelector("[data-ask-anchor]"), "USER_INPUT removes the compact pending anchor");
  const settledLine = conv.querySelector(".ask-settled-line");
  pass(!!settledLine, "a neutral settled line replaces the anchor");
  pass(settledLine && /DB choice/.test(settledLine.textContent), "the settled line names what was asked");
  pass(settledLine && /Postgres/.test(settledLine.textContent), "the settled line echoes the reply");
  const composer = window.document.querySelector("[data-composer-surface]");
  const input = window.document.querySelector(".message-input");
  pass(!composer.hidden && !composer.inert && !input.hidden && !input.inert, "settlement restores normal composer behavior");
  pass(R.pendingAsk === null, "pendingAsk is cleared once resolved");
}

async function testDockSendsAnswersWhileComposerIsGated() {
  const { window, conv, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  const responseDock = dock(window);
  pass(!!responseDock, "sanity: the response dock is pending before its send action");
  if (!responseDock) return;
  pickOption(responseDock, "Postgres");
  const calls = [];
  window.SerfAppwire = { startTurn: (ref, text) => { calls.push({ ref, text }); return Promise.resolve({ turn: { id: "t1" } }); } };
  responseDock.querySelector("[data-ask-send-btn]").click();
  await settle();

  pass(calls.length === 1, "the dock's shared Send answers action invokes the normal startTurn path once");
  pass(calls[0] && /\[answers\]/.test(calls[0].text) && /Postgres/.test(calls[0].text), "the dock sends the composed answer through startTurn");
  pass(!dock(window) && !!conv.querySelector(".ask-settled-line"), "successful dock send resolves the pending ask");
  pass(!window.document.querySelector("[data-composer-surface]").hidden, "normal composer returns after the docked send");
}

async function testMultiSelectAccumulatesCheckedLabels() {
  const { window, R } = newHarness();
  await settle();
  const question = { header: "Stack", question: "Which layers?", multi_select: true, options: [{ label: "API", detail: "" }, { label: "Worker", detail: "" }, { label: "CLI", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  if (!dock(window)) {
    pass(false, "sanity: pending multi-select ask provides the response dock");
    return;
  }
  const qEl = questionEls(window)[0];
  const inputs = Array.from(qEl.querySelectorAll("[data-ask-option-input]"));
  pass(inputs.every((input) => input.type === "checkbox"), "multi_select renders checkboxes, got " + inputs.map((input) => input.type).join(","));

  const check = (label, value) => {
    const input = qEl.querySelector('[data-ask-option][data-option-label="' + label + '"] [data-ask-option-input]');
    input.checked = value;
    input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
  };
  check("API", true);
  check("Worker", true);
  const item = R.pendingAsk.items[0];
  pass(item.resolution && item.resolution.kind === "option" && item.resolution.labels.slice().sort().join(",") === "API,Worker", "checking two dock boxes accumulates both labels");
  check("Worker", false);
  pass(item.resolution && item.resolution.labels.join(",") === "API", "unchecking one box removes just that label");
  check("API", false);
  pass(item.resolution === null, "unchecking the last box clears the resolution");
}

async function testEscapeDoesNotHideOrDiscardDockedResponse() {
  const { window, R } = newHarness();
  await settle();
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
  const before = dock(window);
  pass(!!before, "sanity: pending ask has a dock before Escape");
  if (!before) return;
  pickOption(before, "Postgres");
  window.document.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  const after = dock(window);
  pass(after === before && !after.hidden, "Escape does not hide, collapse, or replace the only docked response form");
  pass(!window.document.querySelector("[data-ask-collapsed-chip]"), "Escape does not create the retired collapse chip");
  pass(R.pendingAsk !== null && R.pendingAsk.items[0].resolution && R.pendingAsk.items[0].resolution.labels[0] === "Postgres", "Escape preserves the in-progress docked answer");
}

function pickOption(root, label) {
  const input = root.querySelector('[data-ask-option][data-option-label="' + label + '"] [data-ask-option-input]');
  input.checked = true;
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

async function testColdAttachPendingResolvedInterruptedDenied() {
  const question = { header: "DB choice", question: "Which datastore?", options: [{ label: "Postgres", detail: "" }, { label: "SQLite", detail: "" }] };
  {
    const { window, conv, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_1", [question])) R.handleData(kind, data);
    pass(!!conv.querySelector("[data-ask-anchor]") && !!dock(window), "cold attach: completed unanswered ask has anchor plus dock");
    pass(R.pendingAsk !== null, "cold attach: pendingAsk is set");
  }
  {
    const { window, conv, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_1", [question]).concat([["USER_INPUT", { text: "Postgres, please" }]])) R.handleData(kind, data);
    pass(!conv.querySelector("[data-ask-anchor]") && !dock(window), "cold attach: answered ask has no interactive anchor or dock");
    pass(!!conv.querySelector(".ask-settled-line") && R.pendingAsk === null, "cold attach: answered ask is settled");
  }
  {
    const { window, conv, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_1", [question], { noEnd: true })) R.handleData(kind, data);
    pass(!conv.querySelector("[data-ask-anchor]") && !dock(window) && !conv.querySelector(".ask-settled-line") && R.pendingAsk === null, "cold attach: ack-less interrupted ask creates no anchor, dock, or settled line");
  }
  {
    const { window, conv, R } = newHarness();
    await settle();
    for (const [kind, data] of askCallEvents("call_1", [question], { error: "denied by hook" })) R.handleData(kind, data);
    pass(!conv.querySelector("[data-ask-anchor]") && !dock(window) && R.pendingAsk === null, "cold attach: denied ask creates no anchor or dock");
  }
}

(async () => {
  await testPendingDockRenders();
  await testAskDockFocusAnnouncementsAndInputNames();
  await testPendingAskBlocksNormalComposerSubmission();
  await testReplayAndSessionReplacementTearDownAskDock();
  await testNoFallbackWithoutIfUnanswered();
  await testAggregationAndGlobalNumbering();
  await testUserInputSettlesAnchorAndRestoresComposer();
  await testDockSendsAnswersWhileComposerIsGated();
  await testMultiSelectAccumulatesCheckedLabels();
  await testEscapeDoesNotHideOrDiscardDockedResponse();
  await testColdAttachPendingResolvedInterruptedDenied();
  if (failures.length > 0) {
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }
  console.log("PASS: ask_user responses use a compact transcript anchor and one docked form");
  process.exit(0);
})();
