// Per-call task update cards: each successful mutation appends one card that
// contains only the changes established by that call. An empty task_list view
// or failed mutation renders nothing.
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
  window.__taskFetches = 0;
  window.fetch = (url) => {
    if (String(url).includes("/tasks")) window.__taskFetches++;
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  };
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;

// CSS contract: verified synchronously so a DOM regression in the scenario below
// cannot short-circuit these checks and hide a concurrent CSS regression.
(function testCSSContract() {
  const checks = [
    [
      /\.plan-item\.current \.plan-glyph\s*\{[^}]*animation:[^;]*think-breathe[^;]*var\(--pulse-cycle\)/.test(styleSrc),
      "current glyph must reuse think-breathe at var(--pulse-cycle)",
    ],
    [
      !/@keyframes\s+plan-breathe/.test(styleSrc),
      "must not add a new plan-breathe keyframe",
    ],
    [
      /\.task-card-progress\s*\{[^}]*font-variant-numeric:\s*tabular-nums/.test(styleSrc),
      "progress count should use tabular-nums",
    ],
    [
      /\.task-card\s*\{[^}]*border-left:[^;]*var\(--line\)/.test(styleSrc),
      ".task-card should use a left rail, not a box",
    ],
    [
      !/\.task-card\s*\{[^}]*\bborder:\s/.test(styleSrc),
      ".task-card must not draw a full box border",
    ],
    [
      !/\.task-card-summary-line\s*\{/.test(styleSrc),
      "task cards must not retain aggregate summary styles",
    ],
    [
      !/\.task-card-toggle\s*\{/.test(styleSrc),
      "task cards must not retain full-plan disclosure styles",
    ],
    [
      !/\.task-card-fold(?:\s|\.|\{)/.test(styleSrc),
      "task cards must not retain done-pile fold styles",
    ],
  ];
  let ok = true;
  for (const [cond, msg] of checks) {
    if (!cond) { console.log("FAIL — CSS contract: " + msg); ok = false; }
  }
  if (ok) console.log("PASS — CSS contract: plan-item animation, tabular-nums, left-rail");
  if (!ok) allPass = false;
})();

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

function taskCall(callId, args, stateTasks, error) {
  return [
    ["TOOL_CALL_START", {
      call_id: callId,
      tool_name: "task_list",
      arguments_json: JSON.stringify(args),
    }],
    ["TOOL_CALL_END", {
      call_id: callId,
      tool_name: "task_list",
      output: error ? "failed" : "ok",
      error: error || undefined,
      tool_state: stateTasks === undefined ? undefined : JSON.stringify(stateTasks),
    }],
  ];
}

function rawTaskCall(callId, argumentsJson, stateTasks) {
  return [
    ["TOOL_CALL_START", {
      call_id: callId,
      tool_name: "task_list",
      arguments_json: argumentsJson,
    }],
    ["TOOL_CALL_END", {
      call_id: callId,
      tool_name: "task_list",
      output: "ok",
      tool_state: stateTasks === undefined ? undefined : JSON.stringify(stateTasks),
    }],
  ];
}

function cardRows(card) {
  return Array.from(card.querySelectorAll(".task-card-row"));
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

function sessionStart() {
  return ["SESSION_START", { session_id: "01TEST" }];
}

function stateWith(changes) {
  return PLAN.map(task => Object.assign({}, task, changes[task.id] || {}));
}

const APPEND_INPUTS = PLAN.map((task, index) => ({
  description: task.description,
  prompt: "Implement " + task.description,
  type: ["research", "implement", "implement", "implement", "implement", "verify", "implement"][index],
  depends_on: index > 0 ? [index] : [],
  reasoning_effort: "medium",
}));

(async () => {

await scenario("append shows only newly added tasks with progress", [
  sessionStart(),
  ...taskCall("seed", { action: "append", tasks: [APPEND_INPUTS[0]] }, [PLAN[0]]),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 2) return { ok: false, detail: "expected two cards, got " + cards.length };
  const card = cards[1];
  const title = card.querySelector(".task-card-title");
  if (!title || title.textContent !== "Tasks") return { ok: false, detail: "expected Tasks title" };
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/3\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "expected 3 / 7 progress" };
  const fill = card.querySelector(".task-card-meter-fill");
  if (!fill || fill.style.width !== "43%") return { ok: false, detail: "expected 43% meter fill, got " + (fill && fill.style.width) };
  if (cardRows(card).length !== 6) return { ok: false, detail: "expected six newly added rows" };
  if (card.textContent.includes(PLAN[0].description)) return { ok: false, detail: "known task must not be repeated" };
  if (/show all|more/i.test(card.textContent)) return { ok: false, detail: "card must not mention show all or more" };
  return { ok: true };
});

await scenario("fresh append renders all seven tasks", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 1) return { ok: false, detail: "expected one card, got " + cards.length };
  if (cardRows(cards[0]).length !== 7) return { ok: false, detail: "expected seven rows" };
  return { ok: true };
});

await scenario("empty append fields render from authoritative state", [
  sessionStart(),
  ...taskCall("t1", {
    action: "append",
    tasks: [{ type: "implement", description: "", prompt: "" }],
  }, [{ id: 8, type: "implement", description: "", prompt: "", status: "open" }]),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 1) return { ok: false, detail: "expected one empty-text append card, got " + cards.length };
  if (cardRows(cards[0]).length !== 1) return { ok: false, detail: "expected one authoritative row" };
  return { ok: true };
});

await scenario("malformed nested task mutations render and refresh no card", [
  sessionStart(),
  ...rawTaskCall("bad-json", "{not valid json", PLAN),
  ...rawTaskCall("bad-append-missing-type", JSON.stringify({ action: "append", tasks: [{ description: "desc", prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-type-number", JSON.stringify({ action: "append", tasks: [{ type: 1, description: "desc", prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-type-invalid", JSON.stringify({ action: "append", tasks: [{ type: "plan", description: "desc", prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-type-empty", JSON.stringify({ action: "append", tasks: [{ type: "", description: "desc", prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-description-missing", JSON.stringify({ action: "append", tasks: [{ type: "implement", prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-description-number", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: 1, prompt: "prompt" }] }), PLAN),
  ...rawTaskCall("bad-append-prompt-missing", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc" }] }), PLAN),
  ...rawTaskCall("bad-append-prompt-number", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc", prompt: 1 }] }), PLAN),
  ...rawTaskCall("bad-append-depends-number", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc", prompt: "prompt", depends_on: 1 }] }), PLAN),
  ...rawTaskCall("bad-append-depends-string", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc", prompt: "prompt", depends_on: ["1"] }] }), PLAN),
  ...rawTaskCall("bad-append-effort-number", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc", prompt: "prompt", reasoning_effort: 1 }] }), PLAN),
  ...rawTaskCall("bad-append-unknown", JSON.stringify({ action: "append", tasks: [{ type: "implement", description: "desc", prompt: "prompt", extra: true }] }), PLAN),
  ...rawTaskCall("bad-append-null", JSON.stringify({ action: "append", tasks: [APPEND_INPUTS[0], null] }), PLAN),
  ...rawTaskCall("bad-append-primitive", JSON.stringify({ action: "append", tasks: [APPEND_INPUTS[0], "bad"] }), PLAN),
  ...rawTaskCall("bad-append-empty", JSON.stringify({ action: "append", tasks: [APPEND_INPUTS[0], {}] }), PLAN),
  ...rawTaskCall("bad-update-null", JSON.stringify({ action: "update", updates: [{ id: 4 }, null] }), PLAN),
  ...rawTaskCall("bad-update-primitive", JSON.stringify({ action: "update", updates: [{ id: 4 }, "bad"] }), PLAN),
  ...rawTaskCall("bad-update-id", JSON.stringify({ action: "update", updates: [{ id: 4 }, { status: "done" }] }), PLAN),
  ...rawTaskCall("bad-update-string-id", JSON.stringify({ action: "update", updates: [{ id: "4", status: "done" }] }), PLAN),
  ...rawTaskCall("bad-update-null-status", JSON.stringify({ action: "update", updates: [{ id: 4, status: null }] }), PLAN),
  ...rawTaskCall("bad-update-notes-number", JSON.stringify({ action: "update", updates: [{ id: 4, notes: 1 }] }), PLAN),
  ...rawTaskCall("bad-update-depends-number", JSON.stringify({ action: "update", updates: [{ id: 4, depends_on: 1 }] }), PLAN),
  ...rawTaskCall("bad-update-depends-string", JSON.stringify({ action: "update", updates: [{ id: 4, depends_on: ["1"] }] }), PLAN),
  ...rawTaskCall("bad-update-effort-number", JSON.stringify({ action: "update", updates: [{ id: 4, reasoning_effort: 1 }] }), PLAN),
  ...rawTaskCall("bad-update-unknown", JSON.stringify({ action: "update", updates: [{ id: 4, extra: true }] }), PLAN),
  ...rawTaskCall("bad-update-status", JSON.stringify({ action: "update", updates: [{ id: 4, status: "paused" }] }), PLAN),
  ...rawTaskCall("unknown-action", JSON.stringify({ action: "replace", tasks: PLAN }), PLAN),
  ...rawTaskCall("bad-append", JSON.stringify({ action: "append", tasks: {} }), PLAN),
  ...rawTaskCall("bad-update", JSON.stringify({ action: "update", updates: {} }), PLAN),
  ...rawTaskCall("bad-top-level-append-extra", JSON.stringify({ action: "append", tasks: APPEND_INPUTS, updates: [] }), PLAN),
  ...rawTaskCall("bad-top-level-update-extra", JSON.stringify({ action: "update", updates: [{ id: 4 }], tasks: [] }), PLAN),
  ...rawTaskCall("bad-top-level-append-missing", JSON.stringify({ action: "append" }), PLAN),
], ({ conv, window }) => {
  if (conv.querySelector(".task-card")) return { ok: false, detail: "malformed successful mutations must not render cards" };
  if (window.__taskFetches !== 1) return { ok: false, detail: "malformed mutations must not refresh the task badge; fetches: " + window.__taskFetches };
  window.SerfRenderer.appendTaskListSystemLine(
    { action: "update", updates: [{ id: 4, status: undefined }] },
    PLAN,
    new Set(),
  );
  if (conv.querySelector(".task-card")) return { ok: false, detail: "explicit undefined status must not render a card" };
  return { ok: true };
});

await scenario("completion shows completed and automatically activated tasks", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
  ...taskCall("t2", {
    action: "update",
    updates: [{ id: 4, status: "done", notes: "shipped it" }],
  }, stateWith({
    4: { status: "done" },
    5: { status: "in_progress" },
  })),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 2) return { ok: false, detail: "expected two cards, got " + cards.length };
  const card = cards[1];
  const prog = card.querySelector(".task-card-progress");
  if (!prog || !/4\s*\/\s*7/.test(prog.textContent)) return { ok: false, detail: "expected 4 / 7 progress" };
  const rows = cardRows(card);
  if (rows.length !== 2) return { ok: false, detail: "expected exactly two changed rows, got " + rows.length };
  const row4 = rows.find(row => /Port the charge and refund paths/.test(row.textContent));
  const row5 = rows.find(row => /Port the webhook signature verification/.test(row.textContent));
  if (!row4 || !row5) return { ok: false, detail: "completion and activation rows missing" };
  if (!row4.classList.contains("done")) return { ok: false, detail: "task 4 should be done" };
  if (!row5.classList.contains("current")) return { ok: false, detail: "task 5 should be current" };
  if (PLAN.some(task => task.id !== 4 && task.id !== 5 && card.textContent.includes(task.description))) {
    return { ok: false, detail: "unchanged task title occurred in completion card" };
  }
  return { ok: true };
});

await scenario("explicit activation shows only the activated task", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
  ...taskCall("t2", {
    action: "update",
    updates: [{ id: 5, status: "in_progress" }],
  }, stateWith({ 5: { status: "in_progress" } })),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  const card = cards[1];
  if (!card) return { ok: false, detail: "missing mutation card" };
  const rows = cardRows(card);
  if (rows.length !== 1 || !rows[0].classList.contains("current") ||
      !rows[0].textContent.includes("Port the webhook signature verification")) {
    return { ok: false, detail: "expected one current row for task 5" };
  }
  return { ok: true };
});

await scenario("non-status update shows only the progress header", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
  ...taskCall("t2", {
    action: "update",
    updates: [{ id: 4, notes: "checkpoint" }],
  }, PLAN),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  const card = cards[1];
  if (!card) return { ok: false, detail: "missing mutation card" };
  if (!/3\s*\/\s*7/.test(card.querySelector(".task-card-progress").textContent)) {
    return { ok: false, detail: "progress should remain 3 / 7" };
  }
  if (cardRows(card).length !== 0) return { ok: false, detail: "non-status update should have no rows" };
  return { ok: true };
});

await scenario("consecutive mutations create cards for their own changes", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
  ...taskCall("t2", {
    action: "update",
    updates: [{ id: 5, status: "in_progress" }],
  }, stateWith({ 5: { status: "in_progress" } })),
  ...taskCall("t3", {
    action: "update",
    updates: [{ id: 6, status: "cancelled" }],
  }, stateWith({
    5: { status: "in_progress" },
    6: { status: "cancelled" },
  })),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 3) return { ok: false, detail: "expected three cards, got " + cards.length };
  const secondRows = cardRows(cards[1]);
  const thirdRows = cardRows(cards[2]);
  if (secondRows.length !== 1 || !secondRows[0].textContent.includes("Port the webhook signature verification")) {
    return { ok: false, detail: "first update card contains the wrong changes" };
  }
  if (thirdRows.length !== 1 || !thirdRows[0].textContent.includes("Update the integration tests")) {
    return { ok: false, detail: "second update card contains the wrong changes" };
  }
  if (thirdRows[0].classList.contains("current")) return { ok: false, detail: "second update card inherited task 5" };
  return { ok: true };
});

await scenario("degraded replay renders explicit changes without inventing auto-activation", [
  sessionStart(),
  ...taskCall("t1", { action: "append", tasks: APPEND_INPUTS }, PLAN),
  ...taskCall("t2", {
    action: "update",
    updates: [{ id: 4, status: "done", notes: "shipped it" }],
  }),
], ({ conv }) => {
  const cards = conv.querySelectorAll(".task-card");
  if (cards.length !== 2) return { ok: false, detail: "expected two cards, got " + cards.length };
  const rows = cardRows(cards[1]);
  if (rows.length !== 1 || !rows[0].classList.contains("done") ||
      !rows[0].textContent.includes("Port the charge and refund paths")) {
    return { ok: false, detail: "expected only explicit completion row" };
  }
  return { ok: true };
});

await scenario("view empty append and failed mutation render no card", [
  sessionStart(),
  ...taskCall("t1", { action: "view" }),
  ...taskCall("t2", { action: "append", tasks: [] }, []),
  ...taskCall("t3", {
    action: "update",
    updates: [{ id: 4, status: "done" }],
  }, undefined, "write failed"),
], ({ conv }) => {
  if (conv.querySelector(".task-card")) return { ok: false, detail: "view, empty append, and failed mutation must render no card" };
  return { ok: true };
});

process.exit(allPass ? 0 : 1);
})();
