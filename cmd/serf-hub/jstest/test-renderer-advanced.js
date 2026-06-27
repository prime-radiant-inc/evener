// More scenarios: multi-update, append, full-list steering, loop, view.
const fs = require("fs");
const { JSDOM } = require("jsdom");


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
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

  require("./load-renderer").evalRenderer(window);
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

await scenario("system transcript blocks", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["SYSTEM_MESSAGE", { title: "System prompt", text: "You are Serf." }],
  ["SYSTEM_MESSAGE", { title: "Prompt loaded", text: "Loaded prompt system.md (2048 B)" }],
  ["SYSTEM_MESSAGE", { title: "Tools (2)", text: "- read_file\n- apply_patch" }],
], ({ conv }) => {
  const blocks = Array.from(conv.querySelectorAll(".system-message"));
  if (blocks.length !== 1) return { ok: false, detail: "expected 1 system block, got " + blocks.length };
  if (blocks[0].textContent.includes("Prompt Loaded") || blocks[0].textContent.includes("You are Serf.") || blocks[0].textContent.includes("Loaded prompt")) return { ok: false, detail: "prompt loaded should default hidden" };
  if (!blocks[0].textContent.includes("Tools (2)") || !blocks[0].textContent.includes("apply_patch")) return { ok: false, detail: "missing tools block" };
  return { ok: true };
});

await scenario("slim system line", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["SYSTEM_LINE", { text: "SessionStart hook superpowers using-superpowers command exit 0" }],
], ({ sysLines, steerings }) => {
  if (sysLines.length !== 0) return { ok: false, detail: "hook exit system-line should default hidden, got " + sysLines.length };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steering blocks" };
  return { ok: true };
});

await scenario("system status preferences reveal saved transcript statuses", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["SYSTEM_MESSAGE", { title: "System prompt", text: "You are Serf." }],
  ["SYSTEM_MESSAGE", { title: "Prompt loaded", text: "Loaded prompt system.md (2048 B)" }],
  ["SYSTEM_MESSAGE", { title: "Round timings", text: "model=12ms tools=3ms" }],
  ["SYSTEM_LINE", { text: "SessionStart hook superpowers using-superpowers command exit 0" }],
  ["SYSTEM_LINE", { text: "PreToolUse hook guard command exit 2" }],
], ({ conv, sysLines, window }) => {
  const initialBlocks = Array.from(conv.querySelectorAll(".system-message"));
  if (initialBlocks.length !== 0) return { ok: false, detail: "prompt and round timings should default hidden, got " + initialBlocks.length };
  if (sysLines.length !== 0) return { ok: false, detail: "hook exits should default hidden" };

  window.localStorage.setItem("serf-hub.transcript.systemStatus", JSON.stringify({
    promptLoaded: true,
    roundTimings: true,
    hookExitsNormal: true,
    hookExitsAll: false,
  }));
  conv.innerHTML = "";
  for (const [type, data] of [
    ["SESSION_START", { session_id: "01TEST" }],
    ["SYSTEM_MESSAGE", { title: "System prompt", text: "You are Serf." }],
    ["SYSTEM_MESSAGE", { title: "Prompt loaded", text: "Loaded prompt system.md (2048 B)" }],
    ["SYSTEM_MESSAGE", { title: "Round timings", text: "model=12ms tools=3ms" }],
    ["SYSTEM_LINE", { text: "SessionStart hook superpowers using-superpowers command exit 0" }],
    ["SYSTEM_LINE", { text: "PreToolUse hook guard command exit 2" }],
  ]) window.SerfRenderer.handleData(type, data);

  const blocks = Array.from(conv.querySelectorAll(".system-message"));
  const lines = Array.from(conv.querySelectorAll(".system-line")).map(e => e.textContent);
  if (blocks.length !== 3) return { ok: false, detail: "expected prompt + prompt-loaded + timings blocks, got " + blocks.length };
  if (!blocks[0].querySelector("summary").textContent.includes("Prompt Loaded")) return { ok: false, detail: "prompt disclosure label missing: " + blocks[0].textContent };
  if (!blocks[0].querySelector(".steering-body").textContent.includes("You are Serf.")) return { ok: false, detail: "full prompt body missing" };
  if (!blocks[1].querySelector("summary").textContent.includes("Prompt Loaded") || !blocks[1].textContent.includes("Loaded prompt system.md")) return { ok: false, detail: "prompt loaded status missing" };
  if (!blocks[2].textContent.includes("Round timings")) return { ok: false, detail: "round timings block missing" };
  if (lines.length !== 1 || !lines[0].includes("exit 0") || lines[0].includes("exit 2")) return { ok: false, detail: "normal-only hook preference wrong: " + lines.join(" | ") };

  window.localStorage.setItem("serf-hub.transcript.systemStatus", JSON.stringify({ hookExitsAll: true }));
  conv.innerHTML = "";
  window.SerfRenderer.handleData("SYSTEM_LINE", { text: "PreToolUse hook guard command exit 2" });
  const allHookLines = Array.from(conv.querySelectorAll(".system-line")).map(e => e.textContent);
  if (allHookLines.length !== 1 || !allHookLines[0].includes("exit 2")) return { ok: false, detail: "all hook exits preference wrong: " + allHookLines.join(" | ") };
  return { ok: true };
});

// 1. Append renders a task-update card with one row per new task. The append's
//    State snapshot carries the store-assigned ids + statuses.
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
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list", tool_state: JSON.stringify([
    { id: 1, description: "investigate spec", status: "open" },
    { id: 2, description: "write code", status: "open" },
    { id: 3, description: "run tests", status: "open" },
  ]) }],
], ({ steerings, toolCalls, conv }) => {
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no task-update card" };
  const rows = card.querySelectorAll(".task-card-row");
  if (rows.length !== 3) return { ok: false, detail: "expected 3 rows, got " + rows.length };
  const t = card.textContent;
  if (!t.includes("investigate spec") || !t.includes("write code") || !t.includes("run tests"))
    return { ok: false, detail: "missing task names: " + t };
  if (steerings.length !== 0) return { ok: false, detail: "expected 0 steerings" };
  if (toolCalls.length !== 0) return { ok: false, detail: "expected 0 tool-calls" };
  return { ok: true };
});

// 2. Multi-update in one call collapses to one prose summary on the card.
//    Descriptions are seeded via the STEERING full-list (the realistic
//    post-compaction path where the daemon emits the full task table).
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
], ({ conv }) => {
  // The full-list no longer renders a pointer; the update refreshes the living
  // card to the current state: Phase 2 is the active frontier, Phase 1 recedes
  // into the done pile.
  if (conv.querySelector(".system-line-pointer")) return { ok: false, detail: "full-list must not render a pointer" };
  if (conv.querySelector(".task-card-summary")) return { ok: false, detail: "no prose summary — the card reads from state" };
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no living plan card" };
  const active = card.querySelector(".task-card-active");
  if (!active || !active.textContent.includes("Phase 2") || !active.classList.contains("current"))
    return { ok: false, detail: "Phase 2 should be the active (current) task: " + (active && active.className) };
  const done = Array.from(card.querySelectorAll(".plan-item.done")).find(r => r.textContent.includes("Phase 1"));
  if (!done) return { ok: false, detail: "Phase 1 should recede into the done pile" };
  return { ok: true };
});

// 3. A bare full-list steering seeds the cache but renders nothing on its own —
//    no pointer, no card (the card is reserved for actual task_list changes).
await scenario("full-list steering seeds the cache without rendering", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #1: Plan the work\n  [in_progress] #2: Do the work\n  [done] #3: Test it\n</SYSTEM-REMINDER>" }],
], ({ conv }) => {
  if (conv.querySelector(".system-line-pointer")) return { ok: false, detail: "pointer should be gone" };
  if (conv.querySelector(".task-card")) return { ok: false, detail: "bare full-list should not render a card" };
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

// 7. Several dones in one call each render as a flagged (touched-done) row.
await scenario("multiple dones render as flagged rows", [
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
], ({ conv }) => {
  // All three done → the card reads "all 3 done" and the three settled tasks
  // live in the (folded) done pile.
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no living plan card" };
  if (!/all 3 done/.test(card.textContent)) return { ok: false, detail: "finished plan should read 'all 3 done'" };
  const doneRows = Array.from(card.querySelectorAll(".plan-item.done .plan-step")).map(s => s.textContent);
  if (doneRows.length !== 3) return { ok: false, detail: "expected 3 done rows, got " + doneRows.length };
  if (!(doneRows.includes("Phase 1") && doneRows.includes("Phase 2") && doneRows.includes("Phase 3")))
    return { ok: false, detail: "missing description: " + doneRows.join(",") };
  return { ok: true };
});

// 8. A cancelled task renders as a touched-cancelled row, carrying its note.
await scenario("cancelled tasks render as a cancelled row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #5: Future feature\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 5, status: "cancelled", notes: "out of scope" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ conv }) => {
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no living plan card" };
  const row = Array.from(card.querySelectorAll(".plan-item.cancelled")).find(r => r.textContent.includes("Future feature"));
  if (!row) return { ok: false, detail: "cancelled task should render as a cancelled row" };
  if (!/1 cancelled/.test(card.textContent)) return { ok: false, detail: "card should count the cancelled task" };
  return { ok: true };
});

// 9. Bracketed descriptions in full-list shouldn't get clipped (regex bug). The
//    cleaned description surfaces in the row label.
await scenario("full-list parses descriptions ending in [WIP]", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #9: Migrate users [WIP]\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 9, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ conv }) => {
  const step = conv.querySelector(".task-card-row .plan-step");
  if (!step || !step.textContent.includes("Migrate users [WIP]")) return { ok: false, detail: "description got clipped: " + (step && step.textContent) };
  return { ok: true };
});

// 10. Reasoning-effort suffix [high] DOES get stripped from the full-list parse.
await scenario("full-list strips [high] reasoning-effort suffix", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #10: Deep work [high]\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 10, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list" }],
], ({ conv }) => {
  const step = conv.querySelector(".task-card-row .plan-step");
  const t = step && step.textContent;
  if (!t || t.includes("[high]")) return { ok: false, detail: "reasoning suffix should be stripped: " + t };
  if (!t.includes("Deep work")) return { ok: false, detail: "description should remain: " + t };
  return { ok: true };
});

// 10b. The expanded effort vocab ([minimal] and [max]) is stripped too.
await scenario("full-list strips [minimal]/[max] reasoning-effort suffixes", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\nTask list:\n  [open] #11: Quick fix [minimal]\n  [open] #12: Hard problem [max]\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u2", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 11, status: "done" }, { id: 12, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u2", output: "ok", tool_name: "task_list" }],
], ({ conv }) => {
  const steps = Array.from(conv.querySelectorAll(".task-card-row .plan-step")).map(s => s.textContent).join(" | ");
  if (steps.includes("[minimal]") || steps.includes("[max]")) return { ok: false, detail: "new-vocab effort suffix should be stripped: " + steps };
  if (!steps.includes("Quick fix") || !steps.includes("Hard problem")) return { ok: false, detail: "descriptions should remain: " + steps };
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

// 12. The daemon auto-advances the next task to in_progress inside the same
//     task_list handler, so the returned State already names the new current
//     task. The card shows it as current; the trailing current-task steering is
//     suppressed (no separate "now on X" line).
await scenario("auto-advance: card shows the new current task, steer is suppressed", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["USER_INPUT", { text: "go" }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"1\">\n<TITLE>First</TITLE>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
  ["TOOL_CALL_START", { call_id: "u1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 1, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "u1", output: "ok", tool_name: "task_list", tool_state: JSON.stringify([
    { id: 1, description: "First", status: "done" },
    { id: 2, description: "Second", status: "in_progress" },
  ]) }],
  ["STEERING_INJECTED", { text: "<SYSTEM-REMINDER>\n<CURRENT-TASK id=\"2\">\n<TITLE>Second</TITLE>\n<INSTRUCTIONS>Do the second task carefully.</INSTRUCTIONS>\n</CURRENT-TASK>\n</SYSTEM-REMINDER>" }],
], ({ conv }) => {
  if (/now on/.test(conv.textContent)) return { ok: false, detail: "no separate 'now on' line should be emitted" };
  if (conv.querySelector(".system-line-now")) return { ok: false, detail: "no .system-line-now should be rendered" };
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no task-update card" };
  const cur = card.querySelector(".task-card-row.current");
  if (!cur || !cur.textContent.includes("Second")) return { ok: false, detail: "card should show Second as the current task" };
  return { ok: true };
});

// 6. Update with no seeded description and no State still shows the task by id.
	await scenario("update with no seeded description falls back to #N", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 7, status: "done" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "ok", tool_name: "task_list" }],
], ({ conv }) => {
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "expected a degraded-path card" };
  const row = card.querySelector(".task-card-row");
  if (!row || !row.textContent.includes("#7")) return { ok: false, detail: "should fall back to #7: " + (row && row.textContent) };
  if (!row.classList.contains("done")) return { ok: false, detail: "row should be done" };
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
