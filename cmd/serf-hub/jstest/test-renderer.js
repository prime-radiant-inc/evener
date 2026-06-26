// Test harness: load renderer.js into a JSDOM window, fire captured
// renderer events, and assert what got rendered.
const fs = require("fs");
const path = require("path");
const { JSDOM, ResourceLoader } = require("jsdom");


// Build a tiny app shell that the renderer expects.
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="ended"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };

// Mock fetch (poller will hit /tasks).
window.fetch = (url) => Promise.resolve({
  ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve(""),
});

// Also stub htmx hook events.
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

// Eval renderer.js inside the JSDOM window.
require("./load-renderer").evalRenderer(window);

// Initialize the renderer on #conversation explicitly.
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

// ───────────────────────────── replay the captured event stream
const events = [
  ["SESSION_START", { model: "test", profile: "test", restored: true, session_id: "01TEST" }],
  ["USER_INPUT", { text: "Hi! How are you?", images: [
    { url: "data:image/png;base64,aW1n", name: "data-url.png" },
  ] }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"1\">\n<TITLE>Understand task</TITLE>\n<INSTRUCTIONS>Understand the task.</INSTRUCTIONS>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { arguments_json: JSON.stringify({ action: "update", updates: [{ id: 1, status: "done" }] }), call_id: "call_a", tool_name: "task_list" }],
  ["TOOL_CALL_END", { call_id: "call_a", output: "Updated.", tool_name: "task_list" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"2\">\n<TITLE>Do the work</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { arguments_json: JSON.stringify({ message: "Hello! I'm doing well." }), call_id: "call_b", tool_name: "communicate" }],
  ["TOOL_CALL_END", { call_id: "call_b", output: "{}", tool_name: "communicate" }],
];

// Wait for the cold-load /tasks fetch to resolve (a few microticks) so
// events render synchronously instead of getting buffered.
async function flushAndAssert() {
  await new Promise(r => setTimeout(r, 30));
  for (const [name, data] of events) {
    window.SerfRenderer.handleData(name, data);
  }
  await new Promise(r => setTimeout(r, 10));
  runAssertions();
}

function runAssertions() {
// ───────────────────────────── assertions
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// 1. The conversation contains exactly one user message.
const users = conv.querySelectorAll(".user-message");
pass(users.length === 1, "expected 1 user message, got " + users.length);
pass(users[0] && users[0].textContent.includes("Hi! How are you?"), "user message text wrong");
const userThumb = users[0] && users[0].querySelector(".user-image-thumb");
pass(userThumb && userThumb.getAttribute("src") === "data:image/png;base64,aW1n", "user data-url image did not render");
// Demoted user message carries the quiet "You" tag (mockup #3), and the tag
// lives OUTSIDE the pill so .pill text stays the clean prompt.
const userTag = users[0] && users[0].querySelector(".user-message-tag");
pass(userTag && userTag.textContent === "You", "user message should carry a 'You' tag");
const userPill = users[0] && users[0].querySelector(".pill");
pass(userPill && !userPill.textContent.includes("You"), "the 'You' tag must not be inside the pill text");

// 2. Zero steering dividers (current-task should be suppressed).
const steerings = conv.querySelectorAll(".steering");
pass(steerings.length === 0, "expected 0 steering dividers, got " + steerings.length);

// 3. The task_list update renders ONE task-update card — no separate
//    system-line prose, and no "now on" steer line (the card conveys both the
//    change and the current task). Both current-task steerings are suppressed.
const sysLines = conv.querySelectorAll(".system-line");
pass(sysLines.length === 0, "expected 0 system-lines (the card replaces them), got " + sysLines.length + ": " + Array.from(sysLines).map(e => e.textContent).join(" | "));
const taskCards = conv.querySelectorAll(".task-card");
pass(taskCards.length === 1, "expected 1 task-update card, got " + taskCards.length);
if (taskCards.length === 1) {
  const card = taskCards[0];
  pass(/Understand task/.test(card.textContent), "card should name the changed task by description, not #id: " + card.textContent);
  pass(!/#1\b/.test(card.querySelector(".plan-step").textContent), "row label should use description not #id");
  pass(!!card.querySelector(".task-card-row.touched.done"), "task #1 should render as a flagged (touched) done row");
  pass(!/now on/.test(conv.textContent), "no 'now on' line should be emitted — the card conveys the current task");
}

// 4. Zero task-list tool-call cards.
const toolCalls = conv.querySelectorAll(".tool-call");
pass(toolCalls.length === 0, "expected 0 tool-call cards (task_list should be suppressed), got " + toolCalls.length);

// 5. The communicate tool should produce exactly one assistant-message.
const assistants = conv.querySelectorAll(".assistant-message");
pass(assistants.length === 1, "expected 1 assistant-message (from communicate), got " + assistants.length);

// 6. Tasks badge should NOT be set (we mocked fetch to return empty []).
//    This just verifies updateTasksBadge handles empty state without throwing.
const badge = window.document.querySelector(".panel-toggle-badge");
pass(badge === null, "no badge expected for empty task list");

// ───────────────────────────── report
if (failures.length === 0) {
  console.log("PASS: all assertions");
  console.log("Rendered conversation HTML:");
  console.log(conv.innerHTML);
  process.exit(0);
} else {
  console.log("Rendered conversation HTML:");
  console.log(conv.innerHTML);
  console.log("");
  for (const f of failures) console.log(f);
  process.exit(1);
}
}

flushAndAssert();
