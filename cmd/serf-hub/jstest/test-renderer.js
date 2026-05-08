// Test harness: load renderer.js into a JSDOM window, mock EventSource,
// fire a captured event stream, and assert what got rendered.
const fs = require("fs");
const path = require("path");
const { JSDOM, ResourceLoader } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

// Build a tiny app shell that the renderer expects.
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-replay-url="/past/01TEST/replay"
       data-events-url=""
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

// Mock EventSource — captured stream gets fed by .fire(events).
class MockEventSource {
  constructor(url) {
    this.url = url;
    this.listeners = new Map();
    MockEventSource.last = this;
  }
  addEventListener(name, fn) {
    if (!this.listeners.has(name)) this.listeners.set(name, []);
    this.listeners.get(name).push(fn);
  }
  set onerror(fn) { this._onerror = fn; }
  close() { this.closed = true; }
  fire(name, data) {
    const fns = this.listeners.get(name) || [];
    for (const fn of fns) fn({ data: JSON.stringify(data) });
  }
}
window.EventSource = MockEventSource;

// Also stub htmx hook events.
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

// Eval renderer.js inside the JSDOM window.
window.eval(rendererSrc);

// Initialize the renderer on #conversation explicitly.
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const es = MockEventSource.last;
if (!es) {
  console.error("FAIL: renderer didn't open an EventSource");
  process.exit(2);
}

// ───────────────────────────── replay the captured event stream
const events = [
  ["SESSION_START", { model: "test", profile: "test", restored: true, session_id: "01TEST" }],
  ["USER_INPUT", { text: "Hi! How are you?" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"1\">\n<TITLE>Understand task</TITLE>\n<INSTRUCTIONS>Understand the task.</INSTRUCTIONS>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { arguments_json: JSON.stringify({ action: "update", updates: [{ id: 1, status: "done" }] }), call_id: "call_a", tool_name: "task_list" }],
  ["TOOL_CALL_END", { call_id: "call_a", output: "Updated.", tool_name: "task_list" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"2\">\n<TITLE>Do the work</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { arguments_json: JSON.stringify({ message: "Hello! I'm doing well." }), call_id: "call_b", tool_name: "communicate" }],
  ["TOOL_CALL_END", { call_id: "call_b", output: "{}", tool_name: "communicate" }],
];

for (const [name, data] of events) {
  es.fire(name, data);
}

// ───────────────────────────── assertions
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// 1. The conversation contains exactly one user message.
const users = conv.querySelectorAll(".user-message");
pass(users.length === 1, "expected 1 user message, got " + users.length);
pass(users[0] && users[0].textContent.includes("Hi! How are you?"), "user message text wrong");

// 2. Zero steering dividers (current-task should be suppressed).
const steerings = conv.querySelectorAll(".steering");
pass(steerings.length === 0, "expected 0 steering dividers, got " + steerings.length);

// 3. Exactly one .system-line with "marked \"Understand task\" done".
const sysLines = conv.querySelectorAll(".system-line");
pass(sysLines.length === 1, "expected 1 system-line, got " + sysLines.length + ": " + Array.from(sysLines).map(e => e.textContent).join(" | "));
if (sysLines.length === 1) {
  const t = sysLines[0].textContent;
  pass(t.includes("marked"), "system-line should say 'marked', got: " + t);
  pass(t.includes("Understand task"), "system-line should name the task, got: " + t);
  pass(t.includes("done"), "system-line should say 'done', got: " + t);
  // Should NOT show "#1" since description is known.
  pass(!t.includes("#1"), "system-line should use description, not #id, got: " + t);
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
