// Simulate a realistic multi-turn agent run and dump the rendered DOM
// for visual inspection. Doesn't assert; meant for eyeballing what a
// reader would see.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: t => t.replace(/^# /m, "<h1>").replace(/$/m, "</h1>") };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
window.eval(rendererSrc);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

// Realistic 7-task plan with steering noise interleaved.
const events = [
  ["SESSION_START", { session_id: "01TEST", model: "test", profile: "test" }],
  ["USER_INPUT", { text: "Add JWT auth middleware to /api routes." }],

  // Plan: agent appends 4 tasks
  ["TOOL_CALL_START", { call_id: "p1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "append", tasks: [
      { type: "research", description: "Audit existing auth", prompt: "..." },
      { type: "implement", description: "Write JWT verify helper", prompt: "..." },
      { type: "implement", description: "Wire helper into /api routes", prompt: "..." },
      { type: "verify", description: "Add integration tests", prompt: "..." },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "p1", output: "ok", tool_name: "task_list" }],

  ["STEERING_INJECTED", { text: '<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Audit existing auth\n  [open] #2: Write JWT verify helper\n  [open] #3: Wire helper into /api routes\n  [open] #4: Add integration tests\n</SYSTEM-REMINDER>' }],

  // Steering: now on task 1 (suppressed)
  ["STEERING_INJECTED", { text: '<SYSTEM-REMINDER>\n<CURRENT-TASK id="1">\n<TITLE>Audit existing auth</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>' }],

  // Agent reads files (cheap cluster)
  ["TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "middleware/session.ts" }) }],
  ["TOOL_CALL_END", { call_id: "r1", output: Array(142).fill("line").join("\n"), tool_name: "read_file" }],
  ["TOOL_CALL_START", { call_id: "r2", tool_name: "grep_files", arguments_json: JSON.stringify({ pattern: "verify", path: "src/" }) }],
  ["TOOL_CALL_END", { call_id: "r2", output: Array(8).fill("hit").join("\n"), tool_name: "grep_files" }],

  // Agent thinks
  ["ASSISTANT_TEXT_START", {}],
  ["ASSISTANT_TEXT_DELTA", { delta: "Session uses opaque tokens. " }],
  ["ASSISTANT_TEXT_DELTA", { delta: "JWT will live beside it." }],
  ["ASSISTANT_TEXT_END", { text: "Session uses opaque tokens. JWT will live beside it." }],

  // Mark #1 done, in_progress #2
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [
      { id: 1, status: "done", notes: "Found 3 files using session middleware." },
      { id: 2, status: "in_progress" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],

  // Steering: now on task 2 (suppressed)
  ["STEERING_INJECTED", { text: '<SYSTEM-REMINDER>\n<CURRENT-TASK id="2">\n<TITLE>Write JWT verify helper</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>' }],

  // Edit a file
  ["TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file", arguments_json: JSON.stringify({ file_path: "src/auth/jwt.ts" }) }],
  ["TOOL_CALL_END", { call_id: "e1", output: "+ export function verifyJwt(token: string)\n+   return jose.jwtVerify(token, key)\n", tool_name: "edit_file" }],

  // Mark #2 done, in_progress #3
  ["TOOL_CALL_START", { call_id: "u2", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 2, status: "done" }, { id: 3, status: "in_progress" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u2", output: "ok", tool_name: "task_list" }],

  // Steering for #3 (suppressed)
  ["STEERING_INJECTED", { text: '<SYSTEM-REMINDER>\n<CURRENT-TASK id="3">\n<TITLE>Wire helper into /api routes</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>' }],

  // Shell command (with output)
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "npm test -- auth" }) }],
  ["TOOL_CALL_END", { call_id: "s1", output: "PASS  src/auth/jwt.test.ts\n12 passing\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell" }],

  // Mark #3 done, in_progress #4
  ["TOOL_CALL_START", { call_id: "u3", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 3, status: "done" }, { id: 4, status: "in_progress" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u3", output: "ok", tool_name: "task_list" }],

  // Loop detection nudge (real steering, should render)
  ["STEERING_INJECTED", { text: "You are stuck in a loop. Stop and think about why your current approach is not working." }],

  // Mark all done
  ["TOOL_CALL_START", { call_id: "u4", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 4, status: "done", notes: "12 tests passing." }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u4", output: "ok", tool_name: "task_list" }],

  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nYou have completed all tasks on your task list.\n</SYSTEM-REMINDER>" }],

  // Communicate
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "communicate", arguments_json: JSON.stringify({
    message: "Done! Auth middleware is in place. Tests pass.",
  }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "{}", tool_name: "communicate" }],
];

(async () => {
await new Promise(r => setTimeout(r, 30));
for (const [t, d] of events) window.SerfRenderer.handleData(t, d);
await new Promise(r => setTimeout(r, 10));

// Print a readable summary of what got rendered.
console.log("=".repeat(70));
console.log("REALISTIC FLOW — what the reader sees:");
console.log("=".repeat(70));

for (const child of conv.children) {
  const cls = child.className.split(/\s+/)[0];
  let summary;
  if (child.classList.contains("user-message")) {
    summary = "USER       │ " + child.querySelector(".pill").textContent.trim();
  } else if (child.classList.contains("assistant-message")) {
    summary = "ASSISTANT  │ " + child.textContent.trim().slice(0, 80);
  } else if (child.classList.contains("system-line")) {
    summary = "SYSTEM     │ " + child.textContent.trim();
  } else if (child.classList.contains("steering")) {
    const verb = child.querySelector(".steering-verb");
    summary = "STEERING   │ " + (verb ? verb.textContent : "?");
  } else if (child.classList.contains("tool-call-cluster")) {
    const calls = child.querySelectorAll(".tool-call");
    summary = "TOOL-CHEAP │ " + calls.length + " calls (" + Array.from(calls).map(c => c.querySelector(".verb").textContent).join(", ") + ")";
  } else if (child.classList.contains("tool-call")) {
    summary = "TOOL-CARD  │ " + child.querySelector(".verb").textContent + " · " + child.querySelector(".target").textContent.slice(0, 50);
  } else if (child.classList.contains("diff-body") || child.classList.contains("tool-body")) {
    summary = "TOOL-BODY  │ " + child.className.replace(/^.*\b(\w+-body)\b.*$/, "$1");
  } else {
    summary = cls.toUpperCase().padEnd(10) + " │ " + child.textContent.trim().slice(0, 80);
  }
  console.log("  " + summary);
}

console.log("=".repeat(70));
console.log("Total elements: " + conv.children.length);
console.log("System-lines:   " + conv.querySelectorAll(".system-line").length);
console.log("Steerings kept: " + conv.querySelectorAll(".steering").length);
console.log("Tool cards:     " + conv.querySelectorAll(".tool-call").length);
process.exit(0);
})();
