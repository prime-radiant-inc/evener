// ask_user answer composition (spec §4.3): the exact [answers] reply format,
// byte-for-byte, plus the quoteGoString() helper that keeps embedded quotes/
// commas/newlines from corrupting the framing. Also covers, end to end
// through the real card UI: mutual exclusion (picking one resolution clears
// every other control for that question) and that the question-level note
// attaches to whichever resolution was chosen (an option, skip, or "you
// decide").
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
function setValue(el, value) { el.value = value; el.dispatchEvent(new el.ownerDocument.defaultView.Event("input", { bubbles: true })); }
function questionEl(card, key) { return card.querySelector('[data-ask-question][data-ask-key="' + key + '"]'); }
function optionInput(qEl, label) { return qEl.querySelector('[data-ask-option][data-option-label="' + label + '"] [data-ask-option-input]'); }
function checkOption(qEl, label) {
  const input = optionInput(qEl, label);
  input.checked = true;
  input.dispatchEvent(new input.ownerDocument.defaultView.Event("change", { bubbles: true }));
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── Pure golden-string tests: spec §4.3's own worked example, byte-exact ──
function testGoldenExampleExact() {
  const { R } = newHarness();
  const items = [
    { header: "DB choice", resolution: { kind: "option", labels: ["Postgres"] }, note: "only the primary" },
    { header: "Naming", resolution: { kind: "decide", leaning: "short names" }, note: "re-ask if it gets weird" },
    { header: "CI matrix", resolution: { kind: "skip" }, note: "irrelevant after #2" },
    { header: "Endpoint", resolution: { kind: "free", text: "use RDS, not self-hosted" }, note: "" },
  ];
  const expected = [
    "[answers]",
    "1. [DB choice] → \"Postgres\" — note: \"only the primary\"",
    "2. [Naming] → you decide — leaning: \"short names\" — note: \"re-ask if it gets weird\"",
    "3. [CI matrix] → skipped (no answer) — note: \"irrelevant after #2\"",
    "4. [Endpoint] → free text: \"use RDS, not self-hosted\"",
  ].join("\n");
  const got = R.composeAskAnswers(items);
  pass(got === expected, "spec §4.3 golden example must match byte-for-byte.\n  want: " + JSON.stringify(expected) + "\n  got:  " + JSON.stringify(got));
}

// An unresolved question (no click at all) composes identically to an
// explicit skip — spec §4.3 lists exactly 5 resolution kinds, no 6th
// "unanswered" kind.
function testUnresolvedComposesAsSkipped() {
  const { R } = newHarness();
  const items = [{ header: "X", resolution: null, note: "" }];
  pass(R.composeAskAnswers(items) === "[answers]\n1. [X] → skipped (no answer)", "an untouched question composes as 'skipped (no answer)', got: " + JSON.stringify(R.composeAskAnswers(items)));
}

// The fallback resolution embeds the model's stated if_unanswered text.
function testFallbackResolution() {
  const { R } = newHarness();
  const items = [{ header: "X", resolution: { kind: "fallback" }, ifUnanswered: "default to Postgres and note the assumption in the PR", note: "" }];
  const expected = "[answers]\n1. [X] → do your stated fallback (\"default to Postgres and note the assumption in the PR\")";
  pass(R.composeAskAnswers(items) === expected, "fallback resolution wrong, got: " + JSON.stringify(R.composeAskAnswers(items)));
}

// Multi-select joins QUOTED labels — unambiguous even when a label itself
// contains a comma (spec §4.3's stated reason for %q-quoting).
function testMultiSelectJoinsQuotedLabels() {
  const { R } = newHarness();
  const items = [{ header: "Pick", resolution: { kind: "option", labels: ["A, B", "C"] }, note: "" }];
  const expected = "[answers]\n1. [Pick] → \"A, B\", \"C\"";
  pass(R.composeAskAnswers(items) === expected, "multi-select join wrong, got: " + JSON.stringify(R.composeAskAnswers(items)));
}

// quoteGoString: the realistic set (printables + \n \t \r " \\) plus other C0
// control chars as \xHH, matching Go's %q for the characters this feature's
// goldens cover.
function testQuoteGoStringRealisticSet() {
  const { R } = newHarness();
  pass(R.quoteGoString('He said "hi", ok?\nBye') === '"He said \\"hi\\", ok?\\nBye"',
    "embedded quote + comma + newline, got: " + JSON.stringify(R.quoteGoString('He said "hi", ok?\nBye')));
  pass(R.quoteGoString("back\\slash") === "\"back\\\\slash\"", "backslash escapes to \\\\, got: " + JSON.stringify(R.quoteGoString("back\\slash")));
  pass(R.quoteGoString("a\tb") === "\"a\\tb\"", "tab escapes to \\t, got: " + JSON.stringify(R.quoteGoString("a\tb")));
  pass(R.quoteGoString("a\rb") === "\"a\\rb\"", "carriage return escapes to \\r, got: " + JSON.stringify(R.quoteGoString("a\rb")));
  pass(R.quoteGoString("a\x01b") === "\"a\\x01b\"", "other C0 control chars escape to \\xHH, got: " + JSON.stringify(R.quoteGoString("a\x01b")));
  pass(R.quoteGoString("plain") === "\"plain\"", "plain text is just quoted, got: " + JSON.stringify(R.quoteGoString("plain")));
}

// ── Mutual exclusion + note attachment, driven through the real card UI ──
async function testMutualExclusionAndNoteAttachment() {
  const { conv, window, R } = newHarness();
  await settle();
  const questions = [
    { header: "Pick", question: "Pick one?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] },
    { header: "Skip me", question: "Skip this?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] },
    { header: "Decide", question: "Who decides?", options: [{ label: "A", detail: "" }, { label: "B", detail: "" }] },
  ];
  for (const [kind, data] of askCallEvents("call_1", questions)) R.handleData(kind, data);
  const card = conv.querySelector(".ask-card");
  const q1 = questionEl(card, "call_1:0");
  const q2 = questionEl(card, "call_1:1");
  const q3 = questionEl(card, "call_1:2");

  // Mutual exclusion, both directions: picking an option while free-text is
  // active clears free-text, and switching to free-text clears a checked
  // option (exactly one resolution per line, spec §4.3/§6.1).
  checkOption(q1, "A");
  pass(optionInput(q1, "A").checked === true, "sanity: option A is checked after clicking it");
  q1.querySelector("[data-ask-free-toggle]").click();
  setValue(q1.querySelector("[data-ask-free-input]"), "custom answer");
  pass(optionInput(q1, "A").checked === false, "switching to free-text clears the previously-checked option");
  checkOption(q1, "B");
  pass(q1.querySelector("[data-ask-free-input]").hidden === true, "switching to an option hides/clears the free-text row");
  pass(optionInput(q1, "A").checked === false && optionInput(q1, "B").checked === true, "only the newly-picked option (B) ends up checked");

  // Note attaches to whichever resolution is finally chosen — here, option B.
  q1.querySelector("[data-ask-note-toggle]").click();
  setValue(q1.querySelector("[data-ask-note-field]"), "only the primary");

  // Note attaches to an explicit skip.
  q2.querySelector("[data-ask-skip-btn]").click();
  q2.querySelector("[data-ask-note-toggle]").click();
  setValue(q2.querySelector("[data-ask-note-field]"), "irrelevant");

  // Note attaches to "you decide" (plus its own optional leaning).
  q3.querySelector("[data-ask-decide-toggle]").click();
  setValue(q3.querySelector("[data-ask-decide-leaning]"), "short names");
  q3.querySelector("[data-ask-note-toggle]").click();
  setValue(q3.querySelector("[data-ask-note-field]"), "re-ask if it gets weird");

  const startTurnCalls = [];
  window.SerfAppwire = {
    startTurn: (ref, text) => { startTurnCalls.push({ ref, text }); return Promise.resolve({ turn: { id: "t1" } }); },
  };
  card.querySelector("[data-ask-send-btn]").click();
  await settle();

  pass(startTurnCalls.length === 1, "Send answers calls startTurn exactly once, got " + startTurnCalls.length);
  const text = startTurnCalls[0] && startTurnCalls[0].text;
  const expected = [
    "[answers]",
    "1. [Pick] → \"B\" — note: \"only the primary\"",
    "2. [Skip me] → skipped (no answer) — note: \"irrelevant\"",
    "3. [Decide] → you decide — leaning: \"short names\" — note: \"re-ask if it gets weird\"",
  ].join("\n");
  pass(text === expected, "composed text after mutual-exclusion + notes wrong.\n  want: " + JSON.stringify(expected) + "\n  got:  " + JSON.stringify(text));
}

(async () => {
  testGoldenExampleExact();
  testUnresolvedComposesAsSkipped();
  testFallbackResolution();
  testMultiSelectJoinsQuotedLabels();
  testQuoteGoStringRealisticSet();
  await testMutualExclusionAndNoteAttachment();

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: ask_user compose format is byte-exact per spec §4.3, and mutual exclusion/notes work through the real card UI");
  process.exit(0);
})();
