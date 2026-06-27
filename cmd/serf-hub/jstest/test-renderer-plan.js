// The living plan card (Design B). The agent maintains a task list via the
// task_list tool; instead of a fresh "Tasks" card on every edit (which littered
// the transcript with repeated near-identical checklists), there is ONE living
// card per session: it is rebuilt to the current plan state and floated to the
// live frontier. It leads with progress + the active task (⟳ blue, breathing),
// the done pile recedes to a count, and the rest folds behind "show all". The
// full list lives in the sidebar. An empty task_list renders NOTHING.
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

function click(el, conv) {
  el.dispatchEvent(new (conv.ownerDocument.defaultView).MouseEvent("click", { bubbles: true }));
}

// A task_list append carries the State snapshot (full list with statuses).
function appendTask(callId, tasks) {
  return [
    ["TOOL_CALL_START", { call_id: callId, tool_name: "task_list", arguments_json: JSON.stringify({ action: "append", tasks }) }],
    ["TOOL_CALL_END", { call_id: callId, tool_name: "task_list", output: "ok", tool_state: JSON.stringify(tasks) }],
  ];
}

const PLAN = [
  { id: 1, description: "Audit current Stripe client usage", status: "done" },
  { id: 2, description: "Pin the new SDK version", status: "done" },
  { id: 3, description: "Map old API surface to new SDK", status: "done" },
  { id: 4, description: "Port the charge and refund paths", status: "in_progress" },
  { id: 5, description: "Port the webhook signature verification", status: "open" },
  { id: 6, description: "Update the integration tests", status: "open" },
  { id: 7, description: "Remove the legacy client", status: "open" },
];

(async () => {

await scenario("living plan card leads with progress + active task, collapsed by default", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", PLAN),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 1) return { ok: false, detail: "expected exactly one living card, got " + cards.length };
  const card = cards[0];

  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/3\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "progress should be 3 / 7, got " + (prog && prog.textContent) };
  if (!card.querySelector(".task-card-meter-fill")) return { ok: false, detail: "missing progress meter" };

  // The active task is the frontier: visible, current (blue, breathing ⟳).
  const active = card.querySelector(".task-card-active");
  if (!active) return { ok: false, detail: "missing active task row" };
  if (!active.classList.contains("current")) return { ok: false, detail: "active row should carry the .current plan state" };
  if (active.querySelector(".plan-glyph").textContent !== "⟳") return { ok: false, detail: "active glyph should be ⟳" };
  if (!active.textContent.includes("Port the charge and refund paths")) return { ok: false, detail: "wrong active task text" };

  // Collapsed: the done pile + what's left fold into one quiet summary line; the
  // expanded body and the individual rows are not shown yet.
  if (card.dataset.expanded !== "false") return { ok: false, detail: "card should start collapsed" };
  const summary = card.querySelector(".task-card-summary-line");
  if (!summary || !/3 done/.test(summary.textContent) || !/3 up next/.test(summary.textContent)) {
    return { ok: false, detail: "summary should read '3 done · 3 up next', got " + (summary && summary.textContent) };
  }
  const toggle = card.querySelector(".task-card-toggle");
  if (!toggle || !/show all/.test(toggle.textContent)) return { ok: false, detail: "missing 'show all' toggle" };

  // Visual contract: the active glyph reuses the shared breathe; progress is tabular.
  if (!/\.plan-item\.current \.plan-glyph\s*\{[^}]*animation:[^;]*think-breathe[^;]*var\(--pulse-cycle\)/.test(styleSrc)) {
    return { ok: false, detail: "current glyph must reuse think-breathe at var(--pulse-cycle)" };
  }
  if (/@keyframes\s+plan-breathe/.test(styleSrc)) return { ok: false, detail: "must not add a new plan-breathe keyframe" };
  if (!/\.task-card-progress\s*\{[^}]*font-variant-numeric:\s*tabular-nums/.test(styleSrc)) {
    return { ok: false, detail: "progress count should use tabular-nums" };
  }
  // The card is a rail, not a box (one containment device).
  if (!/\.task-card\s*\{[^}]*border-left:[^;]*var\(--rule\)/.test(styleSrc)) {
    return { ok: false, detail: ".task-card should use a left rail, not a box" };
  }
  if (/\.task-card\s*\{[^}]*\bborder:\s/.test(styleSrc)) {
    return { ok: false, detail: ".task-card must not draw a full box border" };
  }
  return { ok: true };
});

await scenario("repeated task_list edits keep ONE living card, updated in place", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", PLAN),
  ["TOOL_CALL_START", { call_id: "t2", tool_name: "task_list", arguments_json: JSON.stringify({
    action: "update", updates: [{ id: 4, status: "done", notes: "shipped it" }, { id: 5, status: "in_progress" }],
  }) }],
  ["TOOL_CALL_END", { call_id: "t2", tool_name: "task_list", output: "ok", tool_state: JSON.stringify([
    { id: 1, description: "Audit current Stripe client usage", status: "done" },
    { id: 2, description: "Pin the new SDK version", status: "done" },
    { id: 3, description: "Map old API surface to new SDK", status: "done" },
    { id: 4, description: "Port the charge and refund paths", status: "done" },
    { id: 5, description: "Port the webhook signature verification", status: "in_progress", notes: ["watch the webhook secret"] },
    { id: 6, description: "Update the integration tests", status: "open" },
    { id: 7, description: "Remove the legacy client", status: "open" },
  ]) }],
], ({ conv }) => {
  // The disease cured: still exactly ONE card after two edits — no per-edit spam.
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 1) return { ok: false, detail: "repeated edits must reuse one card, got " + cards.length };
  const card = cards[0];
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/4\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "progress should advance to 4 / 7, got " + (prog && prog.textContent) };
  const active = card.querySelector(".task-card-active");
  if (!active || !active.textContent.includes("Port the webhook signature verification")) {
    return { ok: false, detail: "active task should now be the webhook step" };
  }
  // The active task's note rides under it.
  const note = card.querySelector(".task-card-note");
  if (!note || !/watch the webhook secret/.test(note.textContent)) return { ok: false, detail: "active note missing" };
  return { ok: true };
});

await scenario("expanding reveals Up next; the done pile stays folded behind its count", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", PLAN),
], ({ conv }) => {
  const card = conv.querySelector(".task-card");
  const toggle = card.querySelector(".task-card-toggle");
  click(toggle, conv);
  if (card.dataset.expanded !== "true") return { ok: false, detail: "toggle should expand the card" };
  if (!/collapse/.test(toggle.textContent)) return { ok: false, detail: "toggle should now offer collapse" };

  // Up next lists the open tasks in full.
  const groups = Array.from(card.querySelectorAll(".task-card-group")).map(g => g.textContent);
  if (!groups.some(g => /Up next · 3/.test(g))) return { ok: false, detail: "expected 'Up next · 3' group, got " + groups.join(" | ") };
  const body = card.querySelector(".task-card-body");
  if (!body || !body.textContent.includes("Remove the legacy client")) return { ok: false, detail: "open tasks should be listed when expanded" };

  // The done pile is a fold: its count shows, its rows are hidden until clicked.
  const fold = card.querySelector(".task-card-fold");
  if (!fold) return { ok: false, detail: "done pile should be a fold group" };
  if (!/3 done/.test(fold.querySelector(".task-card-fold-head").textContent)) return { ok: false, detail: "fold head should count the done pile" };
  const doneRows = fold.querySelector(".task-card-fold-rows");
  if (fold.classList.contains("open")) return { ok: false, detail: "done pile should start folded" };
  click(fold.querySelector(".task-card-fold-head"), conv);
  if (!fold.classList.contains("open")) return { ok: false, detail: "clicking the count should reveal the done rows" };
  if (!doneRows.textContent.includes("Audit current Stripe client usage")) return { ok: false, detail: "done rows missing after reveal" };
  return { ok: true };
});

await scenario("a finished plan shows a quiet 'all done' line, no active row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...appendTask("t1", [
    { id: 1, description: "One", status: "done" },
    { id: 2, description: "Two", status: "done" },
    { id: 3, description: "Three", status: "done" },
  ]),
], ({ conv }) => {
  const card = conv.querySelector(".task-card");
  if (!card) return { ok: false, detail: "missing card" };
  if (card.querySelector(".task-card-active")) return { ok: false, detail: "a finished plan has no active task row" };
  const complete = card.querySelector(".task-card-complete");
  if (!complete || !/all 3 done/.test(complete.textContent)) return { ok: false, detail: "should show 'all 3 done', got " + (complete && complete.textContent) };
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/3\s*\/\s*3/.test(prog.textContent)) return { ok: false, detail: "progress should be 3 / 3" };
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
