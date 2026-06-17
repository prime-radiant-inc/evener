// Inline plan/todo checklist (mockup #18, Alt C). When the agent declares or
// updates a plan via the task_list tool, the transcript renders an inline
// checklist block in ONE neutral box with the three glyph-paired states:
//   ✓ done (neutral, recedes), ⟳ in_progress (blue, breathes), ○ open (dim).
// A no-task / empty case renders NOTHING (no "no tasks" placeholder).
const fs = require("fs");
const { JSDOM } = require("jsdom");

const STYLE_PATH = "../assets/style.css";
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
    <form data-input-form data-session-id="01TEST">
      <div class="task-status-row">
        <button type="button" class="status-item tasks-status" data-tasks-trigger title="task list"><span class="status-key">tasks</span><span class="status-value" data-task-status-text>loading…</span></button>
      </div>
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
async function scenario(name, eventSeq, check) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  for (const [t, d] of eventSeq) window.SerfRenderer.handleData(t, d);
  await new Promise(r => setTimeout(r, 10));
  const result = check({ conv, window });
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  HTML: " + conv.innerHTML);
  }
}

(async () => {

// Declaring a plan with mixed statuses renders the inline checklist block with
// the right glyph per state and a progress count.
await scenario("task_list append renders an inline plan block with glyph-paired states + progress", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "t1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "append",
    tasks: [
      { id: 1, description: "Audit current Stripe client usage", status: "done" },
      { id: 2, description: "Pin the new SDK version", status: "done" },
      { id: 3, description: "Map old API surface to new SDK", status: "done" },
      { id: 4, description: "Port the charge and refund paths", status: "in_progress" },
      { id: 5, description: "Port the webhook signature verification", status: "open" },
      { id: 6, description: "Update the integration tests", status: "open" },
      { id: 7, description: "Remove the legacy client", status: "open" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "t1", tool_name: "task_list", output: "ok" }],
], ({ conv }) => {
  const block = conv.querySelector(".plan-block");
  if (!block) return { ok: false, detail: "no inline plan block" };
  // Header: "Plan" + progress count (3/7).
  const prog = block.querySelector(".plan-progress");
  if (!prog) return { ok: false, detail: "no progress count" };
  if (!/3\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "wrong progress count: " + prog.textContent };
  if (!/plan/i.test(block.textContent)) return { ok: false, detail: "header should say Plan" };

  const items = block.querySelectorAll(".plan-item");
  if (items.length !== 7) return { ok: false, detail: "expected 7 plan items, got " + items.length };

  // State classes per status.
  const done = block.querySelectorAll(".plan-item.done");
  if (done.length !== 3) return { ok: false, detail: "expected 3 done items, got " + done.length };
  const cur = block.querySelectorAll(".plan-item.current");
  if (cur.length !== 1) return { ok: false, detail: "expected 1 current item, got " + cur.length };
  const pend = block.querySelectorAll(".plan-item.pending");
  if (pend.length !== 3) return { ok: false, detail: "expected 3 pending items, got " + pend.length };

  // Glyph per state (✓ done, ⟳ current, ○ pending).
  const doneGlyph = done[0].querySelector(".plan-glyph");
  if (!doneGlyph || doneGlyph.textContent !== "✓") return { ok: false, detail: "done glyph wrong: " + (doneGlyph && doneGlyph.textContent) };
  const curGlyph = cur[0].querySelector(".plan-glyph");
  if (!curGlyph || curGlyph.textContent !== "⟳") return { ok: false, detail: "current glyph wrong: " + (curGlyph && curGlyph.textContent) };
  const pendGlyph = pend[0].querySelector(".plan-glyph");
  if (!pendGlyph || pendGlyph.textContent !== "○") return { ok: false, detail: "pending glyph wrong: " + (pendGlyph && pendGlyph.textContent) };

  // Item text is present.
  if (!cur[0].textContent.includes("Port the charge and refund paths")) return { ok: false, detail: "missing current item text" };

  // The current glyph reuses the EXISTING pulse-cycle breathe loop, not a new keyframe.
  if (!/\.plan-item\.current \.plan-glyph\s*\{[^}]*animation:[^;]*think-breathe[^;]*var\(--pulse-cycle\)/.test(styleSrc)) {
    return { ok: false, detail: "current glyph must reuse think-breathe at var(--pulse-cycle)" };
  }
  // No new perpetual keyframe was introduced for the plan.
  if (/@keyframes\s+plan-breathe/.test(styleSrc)) return { ok: false, detail: "must not add a new plan-breathe keyframe" };

  // Progress count uses tabular-nums.
  if (!/\.plan-progress\s*\{[^}]*font-variant-numeric:\s*tabular-nums/.test(styleSrc)) {
    return { ok: false, detail: "progress count should use tabular-nums" };
  }
  return { ok: true };
});

// An update call (only id+status) still re-renders the block from the merged
// cached task set, reflecting the new progress.
await scenario("task_list update re-renders the inline plan block with new progress", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "t1", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "append",
    tasks: [
      { id: 1, description: "Step one", status: "in_progress" },
      { id: 2, description: "Step two", status: "open" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "t1", tool_name: "task_list", output: "ok" }],
  ["TOOL_CALL_START", { call_id: "t2", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update",
    updates: [
      { id: 1, status: "done" },
      { id: 2, status: "in_progress" },
    ],
  }) }],
  ["TOOL_CALL_END", { call_id: "t2", tool_name: "task_list", output: "ok" }],
], ({ conv }) => {
  const blocks = conv.querySelectorAll(".plan-block");
  if (blocks.length < 1) return { ok: false, detail: "no plan block rendered" };
  // The most recent block reflects 1 done / 2 total.
  const latest = blocks[blocks.length - 1];
  const prog = latest.querySelector(".plan-progress");
  if (!prog || !/1\s*\/\s*2/.test(prog.textContent)) return { ok: false, detail: "update progress wrong: " + (prog && prog.textContent) };
  const done = latest.querySelectorAll(".plan-item.done");
  if (done.length !== 1) return { ok: false, detail: "expected 1 done after update, got " + done.length };
  if (!done[0].textContent.includes("Step one")) return { ok: false, detail: "done item should be Step one (description from cache)" };
  const cur = latest.querySelectorAll(".plan-item.current");
  if (cur.length !== 1 || !cur[0].textContent.includes("Step two")) return { ok: false, detail: "current item should be Step two" };
  return { ok: true };
});

// Empty state: a task_list with no tasks (action=view, or empty append)
// renders NOTHING — no plan block, no "no tasks" placeholder.
await scenario("empty task_list renders no plan block", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "t1", tool_name: "task_list", arguments_json: JSON.stringify({ action: "view" }) }],
  ["TOOL_CALL_END", { call_id: "t1", tool_name: "task_list", output: "ok" }],
  ["TOOL_CALL_START", { call_id: "t2", tool_name: "task_list", arguments_json: JSON.stringify({ action: "append", tasks: [] }) }],
  ["TOOL_CALL_END", { call_id: "t2", tool_name: "task_list", output: "ok" }],
], ({ conv }) => {
  if (conv.querySelector(".plan-block")) return { ok: false, detail: "empty task_list must not render a plan block" };
  if (/no tasks/i.test(conv.textContent)) return { ok: false, detail: "must not render a 'no tasks' placeholder" };
  return { ok: true };
});

process.exit(allPass ? 0 : 1);
})();
