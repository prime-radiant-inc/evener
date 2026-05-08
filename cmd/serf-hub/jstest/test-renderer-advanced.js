// More scenarios: multi-update, append, full-list steering, loop, view.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

function newHarness() {
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
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

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
    close() {}
    fire(name, data) {
      const fns = this.listeners.get(name) || [];
      for (const fn of fns) fn({ data: JSON.stringify(data) });
    }
  }
  window.EventSource = MockEventSource;
  window.eval(rendererSrc);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv, es: MockEventSource.last };
}

let allPass = true;
function scenario(name, eventSeq, expectations) {
  const { window, conv, es } = newHarness();
  for (const [type, data] of eventSeq) es.fire(type, data);
  const sysLines = Array.from(conv.querySelectorAll(".system-line")).map(e => e.textContent);
  const steerings = Array.from(conv.querySelectorAll(".steering"));
  const toolCalls = Array.from(conv.querySelectorAll(".tool-call"));
  const result = expectations({ sysLines, steerings, toolCalls, conv, window });
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  HTML: " + conv.innerHTML);
  }
}

// 1. Append action with multiple tasks
scenario("append 3 tasks", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "append",
    tasks: [
      { type: "research", description: "investigate spec", prompt: "..." },
      { type: "implement", description: "write code", prompt: "..." },
      { type: "verify", description: "run tests", prompt: "..." },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ sysLines, steerings, toolCalls }) => {
  if (sysLines.length !== 1) return { ok: false, detail: "expected 1 system-line, got " + sysLines.length };
  const t = sysLines[0];
  if (!t.includes("added 3 tasks")) return { ok: false, detail: "missing 'added 3 tasks': " + t };
  if (!t.includes("investigate spec") || !t.includes("write code") || !t.includes("run tests"))
    return { ok: false, detail: "missing task names: " + t };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steerings" };
  if (toolCalls.length !== 0) return { ok: false, detail: "expected 0 tool-calls" };
  return { ok: true };
});

// 2. Multi-update in one call (collapses to one prose line). Description
//    seeding via STEERING full-list (the realistic post-compaction path
//    where the daemon emits the full task table including ids).
scenario("multi-update in one call (descriptions seeded via full-list)", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Phase 1\n  [open] #2: Phase 2\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update",
    updates: [
      { id: 1, status: "done", notes: "took longer than expected" },
      { id: 2, status: "in_progress" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  // We expect: pointer line from full-list + one update prose line.
  if (sysLines.length !== 2) return { ok: false, detail: "expected 2 system-lines (pointer + update), got " + sysLines.length + ": " + sysLines.join(" | ") };
  const updateLine = sysLines[1];
  if (!updateLine.includes("marked")) return { ok: false, detail: "no 'marked' verb: " + updateLine };
  if (!updateLine.includes("Phase 1")) return { ok: false, detail: "no Phase 1 description: " + updateLine };
  if (!updateLine.includes("Phase 2")) return { ok: false, detail: "no Phase 2 description: " + updateLine };
  if (!updateLine.includes("started")) return { ok: false, detail: "no 'started' verb: " + updateLine };
  if (!updateLine.includes("took longer")) return { ok: false, detail: "notes missing: " + updateLine };
  return { ok: true };
});

// 3. Full-list steering renders as a pointer line that opens the panel
scenario("full-list steering renders as pointer", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Plan the work\n  [in_progress] #2: Do the work\n  [done] #3: Test it\n</SYSTEM-REMINDER>" }],
], ({ conv }) => {
  const ptr = conv.querySelector(".system-line-pointer");
  if (!ptr) return { ok: false, detail: "no pointer rendered" };
  if (!ptr.textContent.includes("3 items")) return { ok: false, detail: "pointer wrong: " + ptr.textContent };
  return { ok: true };
});

// 4. Loop detection still renders as a steering divider
scenario("loop steering still renders", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "You are stuck in a loop. Stop and think." }],
], ({ steerings }) => {
  if (steerings.length !== 1) return { ok: false, detail: "expected 1 steering, got " + steerings.length };
  if (!steerings[0].textContent.toLowerCase().includes("loop")) return { ok: false, detail: "loop not in label" };
  return { ok: true };
});

// 5. action=view is suppressed entirely
scenario("view action suppressed", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({ action: "view" }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ sysLines, toolCalls, steerings }) => {
  if (sysLines.length !== 0) return { ok: false, detail: "expected 0 system-lines, got " + sysLines.length };
  if (toolCalls.length !== 0) return { ok: false, detail: "expected 0 tool-calls" };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steerings" };
  return { ok: true };
});

// 6. Update without seeded description falls back to #N
scenario("update with no seeded description falls back to #N", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 7, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  if (sysLines.length !== 1) return { ok: false, detail: "expected 1 system-line" };
  const t = sysLines[0];
  if (!t.includes("#7")) return { ok: false, detail: "should fall back to #7: " + t };
  if (!t.includes("done")) return { ok: false, detail: "should say done: " + t };
  return { ok: true };
});

process.exit(allPass ? 0 : 1);
