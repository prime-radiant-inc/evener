// Simulate a realistic multi-turn agent run and assert the rendered DOM.
// Covers: user message, cheap-tool cluster, mid-turn assistant text,
// standalone tool cards, SYSTEM-REMINDER suppression, communicate rendering,
// and the living task card.
const fs = require("fs");
const { JSDOM } = require("jsdom");

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
require("./load-renderer").evalRenderer(window);
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

  // Steering: now on task 1 (suppressed — SYSTEM-REMINDER wrapper)
  ["STEERING_INJECTED", { text: '<SYSTEM-REMINDER>\n<CURRENT-TASK id="1">\n<TITLE>Audit existing auth</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>' }],

  // Agent reads files (cheap cluster)
  ["TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "middleware/session.ts" }) }],
  ["TOOL_CALL_END", { call_id: "r1", output: Array(142).fill("line").join("\n"), tool_name: "read_file" }],
  ["TOOL_CALL_START", { call_id: "r2", tool_name: "grep_files", arguments_json: JSON.stringify({ pattern: "verify", path: "src/" }) }],
  ["TOOL_CALL_END", { call_id: "r2", output: Array(8).fill("hit").join("\n"), tool_name: "grep_files" }],

  // Agent thinks — ASSISTANT_TEXT_END is the event under test
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

  // Loop detection nudge (real steering, no SYSTEM-REMINDER wrapper — must render)
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

  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  // 1. Exactly one user message with the prompt text in its pill.
  const users = conv.querySelectorAll(".user-message");
  pass(users.length === 1, "expected 1 .user-message, got " + users.length);
  const pill = users[0] && users[0].querySelector(".pill");
  pass(pill && pill.textContent.includes("Add JWT auth middleware"), "user pill should contain the prompt text");

  // 2. Exactly two assistant messages: one from ASSISTANT_TEXT_END, one from
  //    the communicate tool. Deleting .assistant-message from the
  //    ASSISTANT_TEXT_END rendering path drops the count to 1 and fails here.
  const assistants = conv.querySelectorAll(".assistant-message");
  pass(assistants.length === 2, "expected 2 .assistant-message, got " + assistants.length);

  // 3. First assistant message carries the mid-turn reasoning text.
  pass(
    assistants[0] && assistants[0].textContent.includes("Session uses opaque tokens"),
    "first assistant message should carry mid-turn text from ASSISTANT_TEXT_END"
  );

  // 4. Second assistant message carries the communicate output.
  pass(
    assistants[1] && assistants[1].textContent.includes("Done!"),
    "second assistant message should carry communicate output"
  );

  // 5. SYSTEM-REMINDER steerings are suppressed; the loop-detection nudge and
  //    the tasks-done nudge (also a SYSTEM-REMINDER) are the only ones kept.
  //    The loop-detection nudge has no SYSTEM-REMINDER wrapper, so it renders.
  //    "You have completed all tasks" is wrapped in SYSTEM-REMINDER — suppressed.
  const steerings = conv.querySelectorAll(".steering");
  pass(steerings.length === 2, "expected 2 .steering (SYSTEM-REMINDERs suppressed), got " + steerings.length);

  // 6. Cheap tool calls (read + grep) grouped into exactly one cluster.
  const clusters = conv.querySelectorAll(".tool-call-cluster");
  pass(clusters.length === 1, "expected 1 .tool-call-cluster, got " + clusters.length);

  // 7. Total .tool-call count: 2 inside the cluster (read, grep) + edit + shell.
  //    task_list updates are absorbed into the living task card, not tool-call cards.
  const toolCalls = conv.querySelectorAll(".tool-call");
  pass(toolCalls.length === 4, "expected 4 .tool-call (read+grep+edit+shell), got " + toolCalls.length);

  // 8. Exactly one living task card (task_list operations fold into a single card).
  const taskCards = conv.querySelectorAll(".task-card");
  pass(taskCards.length === 1, "expected 1 .task-card, got " + taskCards.length);

  // 9. Total top-level conversation children.
  pass(conv.children.length === 9, "expected 9 conversation children, got " + conv.children.length);

  if (failures.length) {
    for (const f of failures) console.error(f);
    process.exit(1);
  }
  console.log("PASS realistic-flow — user message, assistant text (×2), tool cluster, task card, steering suppression");
  process.exit(0);
})();
