// hydrationKeysFromThread's completedItemKeys must recognize a "failed" item
// as settled, not just "completed" (Go follow-up: the projector/transcript
// now stamp an honest "failed" status on an errored tool result instead of
// always "completed" — see internal/appprojector, internal/apptranscript).
//
// The race this guards: a tool call inside an in-progress turn already
// settled (with an error) by the time readThread's snapshot was taken, so
// the snapshot's hydration replay renders its TOOL_CALL_START/END once. A
// live turn/completed notification for that same turn arrives and is
// buffered (hydration hasn't flipped appwireHydrated yet). After hydration
// completes, the buffered notification replays; notificationForHydrationReplay
// must filter the already-rendered item out of it using completedItemKeys.
// If the item's status ("failed") isn't recognized as terminal, the filter
// keeps it, the buffered turn/completed re-emits TOOL_CALL_START for the same
// call_id, activeTools has no entry (its first pass already reached
// TOOL_CALL_END and deleted it), and beginToolCall appends a second card.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <div id="input-status" class="input-status">
    <span class="status-item liveness-inline" data-liveness hidden></span>
  </div>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (text) => text };
window.fetch = () => Promise.resolve({
  ok: true,
  json: () => Promise.resolve([]),
  text: () => Promise.resolve(""),
});

let resolveReadThread;
const readThreadPromise = new Promise((resolve) => { resolveReadThread = resolve; });
let notify;

window.SerfAppwire = {
  tasks: () => new Promise(() => {}),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "",
  onNotification: (handler) => {
    notify = handler;
    return () => {};
  },
  onConnectionLost: () => () => {},
  readThread: () => readThreadPromise,
  eventsFromThread: (thread) => thread.testEvents,
  // Mirrors appwire.js's eventsFromItem translation of a commandExecution
  // item into TOOL_CALL_START/END closely enough to reproduce the
  // duplicate-card mechanism under test: a real turn/completed replay
  // synthesizes a start/end pair for every item it carries.
  eventsFromNotification: (method, params) => {
    if (method !== "turn/completed" || !params || !params.turn) return [];
    const events = [];
    for (const item of params.turn.items || []) {
      if (item.type !== "commandExecution") continue;
      events.push(["TOOL_CALL_START", {
        call_id: item.callId, tool_name: item.toolName || "shell", arguments_json: "{}",
      }]);
      events.push(["TOOL_CALL_END", {
        call_id: item.callId, tool_name: item.toolName || "shell", error: item.error || "",
      }]);
    }
    return events;
  },
};

require("./load-renderer").evalRenderer(window);
const conversation = window.document.getElementById("conversation");
window.SerfRenderer.init(conversation);

const drainMicrotasks = async () => {
  for (let i = 0; i < 8; i++) await Promise.resolve();
};

(async () => {
  // The turn itself is still in progress (more tool calls could follow) —
  // only the ONE tool call inside it has already settled, with an error.
  // Keeping the turn non-terminal isolates the item-level status check from
  // the turn-level one (hydrationKeysFromThread ORs the two).
  const failedItem = { id: "item_fail", callId: "call_fail", type: "commandExecution", toolName: "shell", status: "failed", error: "boom" };

  // Buffered BEFORE the snapshot resolves, so it lands in pendingNotifications
  // (appwireHydrated is still false) rather than being delivered live.
  notify("turn/completed", {
    ref: "local:01TEST",
    threadId: "01TEST",
    turn: { id: "turn_1", status: "completed", items: [failedItem] },
  });

  resolveReadThread({
    thread: {
      id: "01TEST",
      sessionId: "01TEST",
      status: "inProgress",
      serf: { ref: "local:01TEST" },
      turns: [{ id: "turn_1", status: "inProgress", items: [failedItem] }],
      testEvents: [
        ["USER_INPUT", { text: "hello" }],
        ["TOOL_CALL_START", { call_id: "call_fail", tool_name: "shell", arguments_json: "{}" }],
        ["TOOL_CALL_END", { call_id: "call_fail", tool_name: "shell", error: "boom" }],
      ],
    },
  });
  await drainMicrotasks();

  assert.strictEqual(window.SerfRenderer.appwireHydrated, true, "hydration did not complete");
  const cards = conversation.querySelectorAll(".tool-call.shell");
  assert.strictEqual(cards.length, 1,
    "a settled-but-errored item must render exactly once: the buffered turn/completed's " +
    "re-emitted TOOL_CALL_START for the same call_id must be suppressed by " +
    "completedItemKeys recognizing status \"failed\" as terminal (got " + cards.length + " cards)");

  console.log("PASS: a \"failed\" item lands in the hydration-dedup suppression set (no duplicate card)");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
