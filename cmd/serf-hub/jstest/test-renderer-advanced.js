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
         data-state="ended"></div>
	    <form data-input-form data-session-id="01TEST">
	      <textarea class="message-input"></textarea>
	      <button class="send-btn" type="submit"></button>
	    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

  window.eval(rendererSrc);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
async function scenario(name, eventSeq, expectations) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  for (const [type, data] of eventSeq) window.SerfRenderer.handleData(type, data);
  await new Promise(r => setTimeout(r, 10));
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

(async () => {

// 1. Append action with multiple tasks
await scenario("append 3 tasks", [
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
await scenario("multi-update in one call (descriptions seeded via full-list)", [
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
await scenario("full-list steering renders as pointer", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Plan the work\n  [in_progress] #2: Do the work\n  [done] #3: Test it\n</SYSTEM-REMINDER>" }],
], ({ conv }) => {
  const ptr = conv.querySelector(".system-line-pointer");
  if (!ptr) return { ok: false, detail: "no pointer rendered" };
  if (!ptr.textContent.includes("3 items")) return { ok: false, detail: "pointer wrong: " + ptr.textContent };
  return { ok: true };
});

// 4. Loop detection still renders as a steering divider
await scenario("loop steering still renders", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "You are stuck in a loop. Stop and think." }],
], ({ steerings }) => {
  if (steerings.length !== 1) return { ok: false, detail: "expected 1 steering, got " + steerings.length };
  if (!steerings[0].textContent.toLowerCase().includes("loop")) return { ok: false, detail: "loop not in label" };
  return { ok: true };
});

// 5. action=view is suppressed entirely
await scenario("view action suppressed", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({ action: "view" }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ sysLines, toolCalls, steerings }) => {
  if (sysLines.length !== 0) return { ok: false, detail: "expected 0 system-lines, got " + sysLines.length };
  if (toolCalls.length !== 0) return { ok: false, detail: "expected 0 tool-calls" };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steerings" };
  return { ok: true };
});

// 7. Same-verb runs collapse: 3 dones in one call → "marked A, B, C done"
await scenario("same-verb runs collapse", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Phase 1\n  [open] #2: Phase 2\n  [open] #3: Phase 3\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update",
    updates: [
      { id: 1, status: "done" },
      { id: 2, status: "done" },
      { id: 3, status: "done" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  const update = sysLines.find(l => l.includes("marked"));
  if (!update) return { ok: false, detail: "no 'marked' line: " + sysLines.join(" | ") };
  // Should be ONE clause: "marked A, B, C done", not three "marked X done" clauses.
  const matches = update.match(/marked /g);
  if (!matches || matches.length !== 1) return { ok: false, detail: "expected single 'marked' verb, got: " + update };
  if (!update.includes("Phase 1") || !update.includes("Phase 2") || !update.includes("Phase 3"))
    return { ok: false, detail: "missing description: " + update };
  if (!update.endsWith("done")) return { ok: false, detail: "should end with 'done': " + update };
  return { ok: true };
});

// 8. Cancelled verb
await scenario("cancelled tasks get distinct verb", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #5: Future feature\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 5, status: "cancelled", notes: "out of scope" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  const line = sysLines.find(l => l.includes("Future feature"));
  if (!line) return { ok: false, detail: "no line names task: " + sysLines.join(" | ") };
  if (!line.includes("cancelled")) return { ok: false, detail: "missing 'cancelled': " + line };
  if (!line.includes("out of scope")) return { ok: false, detail: "missing notes: " + line };
  return { ok: true };
});

// 9. Bracketed descriptions in full-list shouldn't get clipped (regex bug)
await scenario("full-list parses descriptions ending in [TBD]", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #9: Migrate users [WIP]\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 9, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  const line = sysLines.find(l => l.includes("marked"));
  if (!line) return { ok: false, detail: "no 'marked' line: " + sysLines.join(" | ") };
  if (!line.includes("Migrate users [WIP]")) return { ok: false, detail: "description got clipped: " + line };
  return { ok: true };
});

// 10. Reasoning-effort suffix [high] DOES get stripped from full-list parse
await scenario("full-list strips [high] reasoning-effort suffix", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #10: Deep work [high]\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 10, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ sysLines }) => {
  const line = sysLines.find(l => l.includes("marked"));
  if (!line) return { ok: false, detail: "no marked line" };
  if (line.includes("[high]")) return { ok: false, detail: "reasoning suffix should be stripped: " + line };
  if (!line.includes("Deep work")) return { ok: false, detail: "description should remain: " + line };
  return { ok: true };
});

// 11. task-nudge steering is suppressed
await scenario("task-nudge steering suppressed", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nYou have a task_list tool available for organizing multi-step work.\n</SYSTEM-REMINDER>" }],
], ({ sysLines, steerings }) => {
  if (sysLines.length !== 0) return { ok: false, detail: "expected 0 system-lines" };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steering dividers (task-nudge should be suppressed)" };
  return { ok: true };
});

// 12. "now on X" emitted on auto-advance after a done update
await scenario("auto-advance steering becomes 'now on X'", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["USER_INPUT", { text: "go" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"1\">\n<TITLE>First</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 1, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"2\">\n<TITLE>Second</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
], ({ sysLines }) => {
  const nowOn = sysLines.find(l => l.includes("now on"));
  if (!nowOn) return { ok: false, detail: "expected a 'now on' line on auto-advance: " + sysLines.join(" | ") };
  if (!nowOn.includes("Second")) return { ok: false, detail: "should name the new task: " + nowOn };
  // Make sure the LEADING current-task steering didn't emit "now on First"
  if (sysLines.some(l => l.includes("now on") && l.includes("First")))
    return { ok: false, detail: "leading steering should NOT emit 'now on First'" };
  return { ok: true };
});

// 6. Update without seeded description falls back to #N
	await scenario("update with no seeded description falls back to #N", [
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

	// 7. A status change applies immediately without carrying stale idle
	// send capability into an active turn.
	{
	  const { window } = newHarness();
	  await new Promise(r => setTimeout(r, 30));
	  const btn = window.document.querySelector(".send-btn");
	  window.SerfRenderer.handleData("SESSION_START", {
	    session_id: "01TEST",
	    status: "idle",
	    capabilities: { queue: false },
	  });
	  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "active" });
	  const ok = btn.getAttribute("data-capability-send") === "false" &&
	    btn.getAttribute("data-capability-queue") === "false";
	  console.log((ok ? "PASS" : "FAIL") + " — active status avoids stale idle send capability");
	  if (!ok) {
	    allPass = false;
	    console.log("  detail: queue attr=" + btn.getAttribute("data-capability-queue"));
	  }
	}

	process.exit(allPass ? 0 : 1);
})();
