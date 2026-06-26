// Task-update card. When the agent declares or changes its task list via the
// task_list tool, the transcript renders ONE coherent card: the changed rows
// (flagged), the now-current task, and a little surrounding context, with the
// three glyph-paired states reused from the plan grammar:
//   ✓ done (neutral, recedes), ⟳ in_progress (blue, breathes), ○ open (dim).
// Completed rows carry a "checked off at" time when the tool State provides it.
// A no-task / empty case renders NOTHING.
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

// A task_list append carries the State snapshot (full list with statuses), so
// every task is "new" and the card shows the whole plan with glyph-paired
// states + a progress count.
function appendTask(callId, tasks) {
  return [
    ["TOOL_CALL_START", { call_id: callId, tool_name: "task_list", arguments_json: JSON.stringify({ action: "append", tasks }) }],
    ["TOOL_CALL_END", { call_id: callId, tool_name: "task_list", output: "ok", tool_state: JSON.stringify(tasks) }],
  ];
}

(async () => {

await scenario("task_list append renders a task-update card with glyph-paired states + progress", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", [
    { id: 1, description: "Audit current Stripe client usage", status: "done" },
    { id: 2, description: "Pin the new SDK version", status: "done" },
    { id: 3, description: "Map old API surface to new SDK", status: "done" },
    { id: 4, description: "Port the charge and refund paths", status: "in_progress" },
    { id: 5, description: "Port the webhook signature verification", status: "open" },
    { id: 6, description: "Update the integration tests", status: "open" },
    { id: 7, description: "Remove the legacy client", status: "open" },
  ]),
], ({ conv }) => {
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "no task-update card" };
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/3\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "wrong progress: " + (prog && prog.textContent) };

  // An append makes every task new → all rows visible (none hidden), no show-all.
  const items = card.querySelectorAll(".task-card-row");
  if (items.length !== 7) return { ok: false, detail: "expected 7 rows, got " + items.length };
  if (card.querySelector(".task-card-hidden")) return { ok: false, detail: "append should not hide any rows" };
  if (card.querySelector(".task-card-showall")) return { ok: false, detail: "append should not show a show-all control" };

  // State classes + glyphs per status (reused plan grammar).
  const done = card.querySelectorAll(".task-card-row.done");
  if (done.length !== 3) return { ok: false, detail: "expected 3 done rows, got " + done.length };
  const cur = card.querySelectorAll(".task-card-row.current");
  if (cur.length !== 1) return { ok: false, detail: "expected 1 current row, got " + cur.length };
  const pend = card.querySelectorAll(".task-card-row.pending");
  if (pend.length !== 3) return { ok: false, detail: "expected 3 pending rows, got " + pend.length };
  if (done[0].querySelector(".plan-glyph").textContent !== "✓") return { ok: false, detail: "done glyph wrong" };
  if (cur[0].querySelector(".plan-glyph").textContent !== "⟳") return { ok: false, detail: "current glyph wrong" };
  if (pend[0].querySelector(".plan-glyph").textContent !== "○") return { ok: false, detail: "pending glyph wrong" };
  if (!cur[0].textContent.includes("Port the charge and refund paths")) return { ok: false, detail: "missing current item text" };

  // The current glyph reuses the existing pulse-cycle breathe loop.
  if (!/\.plan-item\.current \.plan-glyph\s*\{[^}]*animation:[^;]*think-breathe[^;]*var\(--pulse-cycle\)/.test(styleSrc)) {
    return { ok: false, detail: "current glyph must reuse think-breathe at var(--pulse-cycle)" };
  }
  if (/@keyframes\s+plan-breathe/.test(styleSrc)) return { ok: false, detail: "must not add a new plan-breathe keyframe" };
  if (!/\.task-card-progress\s*\{[^}]*font-variant-numeric:\s*tabular-nums/.test(styleSrc)) {
    return { ok: false, detail: "progress count should use tabular-nums" };
  }
  return { ok: true };
});

// An update flags only the changed rows, elevates the new current task, shows
// the surrounding context, folds the rest behind "show all", and surfaces the
// completion time + the progress note that rode with the update.
await scenario("task_list update flags changes, keeps context, folds the rest", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", [
    { id: 1, description: "One", status: "done", completed_at: "2026-06-25T14:00:00Z" },
    { id: 2, description: "Two", status: "done", completed_at: "2026-06-25T14:05:00Z" },
    { id: 3, description: "Three", status: "in_progress" },
    { id: 4, description: "Four", status: "open" },
    { id: 5, description: "Five", status: "open" },
    { id: 6, description: "Six", status: "open" },
  ]),
  ["TOOL_CALL_START", { call_id: "t2", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update",
    updates: [{ id: 3, status: "done", notes: "shipped it" }, { id: 4, status: "in_progress" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "t2", tool_name: "task_list", output: "ok", tool_state: JSON.stringify([
    { id: 1, description: "One", status: "done", completed_at: "2026-06-25T14:00:00Z" },
    { id: 2, description: "Two", status: "done", completed_at: "2026-06-25T14:05:00Z" },
    { id: 3, description: "Three", status: "done", completed_at: "2026-06-25T14:10:00Z", notes: ["shipped it"] },
    { id: 4, description: "Four", status: "in_progress" },
    { id: 5, description: "Five", status: "open" },
    { id: 6, description: "Six", status: "open" },
  ]) }],
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 2) return { ok: false, detail: "expected 2 cards (append + update), got " + cards.length };
  const card = cards[1];
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/3\s*\/\s*6/.test(prog.textContent)) return { ok: false, detail: "update progress wrong: " + (prog && prog.textContent) };

  // Changed rows (#3, #4) are flagged.
  const changed = Array.from(card.querySelectorAll(".task-card-row.changed .plan-step")).map(s => s.textContent);
  if (!(changed.includes("Three") && changed.includes("Four") && changed.length === 2))
    return { ok: false, detail: "changed rows should be exactly Three+Four, got " + changed.join(",") };

  // Current task is #4 (now in_progress).
  const cur = card.querySelectorAll(".task-card-row.current");
  if (cur.length !== 1 || !cur[0].textContent.includes("Four")) return { ok: false, detail: "current should be Four" };

  // Context: the neighbor before the current (#3, just done) and after (#5) are
  // visible without being flagged; the far tasks (#1, #2) fold away.
  const visibleSteps = Array.from(card.querySelectorAll(".task-card-row:not(.task-card-hidden) .plan-step")).map(s => s.textContent);
  if (!visibleSteps.includes("Five")) return { ok: false, detail: "next task (Five) should be visible context: " + visibleSteps.join(",") };
  if (visibleSteps.includes("One") || visibleSteps.includes("Two")) return { ok: false, detail: "far done tasks should fold: " + visibleSteps.join(",") };

  // The folded tasks hide behind a show-all control.
  if (!card.querySelector(".task-card-hidden")) return { ok: false, detail: "expected some rows hidden" };
  const showAll = card.querySelector(".task-card-showall");
  if (!showAll || !/show all \(6\)/.test(showAll.textContent)) return { ok: false, detail: "missing show-all (6): " + (showAll && showAll.textContent) };

  // The note that rode with the update is shown under its row.
  if (!/shipped it/.test(card.textContent)) return { ok: false, detail: "update note 'shipped it' missing" };

  // A completed row carries a checked-off time.
  const doneTime = card.querySelector(".task-card-row.done .task-card-time");
  if (!doneTime || !doneTime.textContent.trim()) return { ok: false, detail: "completed row should show a checked-off time" };

  // Expanding show-all reveals the folded rows.
  showAll.dispatchEvent(new (conv.ownerDocument.defaultView).MouseEvent("click", { bubbles: true }));
  if (card.querySelector(".task-card-hidden")) return { ok: false, detail: "show-all should reveal all rows" };
  return { ok: true };
});

// Empty task_list (view, or empty append) renders NOTHING.
await scenario("empty task_list renders no card", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "t1", tool_name: "task_list", arguments_json: JSON.stringify({ action: "view" }) }],
  ["TOOL_CALL_END", { call_id: "t1", tool_name: "task_list", output: "ok" }],
  ["TOOL_CALL_START", { call_id: "t2", tool_name: "task_list", arguments_json: JSON.stringify({ action: "append", tasks: [] }) }],
  ["TOOL_CALL_END", { call_id: "t2", tool_name: "task_list", output: "ok", tool_state: JSON.stringify([]) }],
], ({ conv }) => {
  if (conv.querySelector(".task-card")) return { ok: false, detail: "empty task_list must not render a card" };
  if (/no tasks/i.test(conv.textContent)) return { ok: false, detail: "must not render a 'no tasks' placeholder" };
  return { ok: true };
});

process.exit(allPass ? 0 : 1);
})();
