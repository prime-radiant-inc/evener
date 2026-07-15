const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
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

let resolveTasks;
const tasksPromise = new Promise((resolve) => { resolveTasks = resolve; });
let resolveReadThread;
const readThreadPromise = new Promise((resolve) => { resolveReadThread = resolve; });
let notify;

window.SerfAppwire = {
  tasks: () => tasksPromise,
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: (handler) => {
    notify = handler;
    return () => {};
  },
  onConnectionLost: () => () => {},
  readThread: () => readThreadPromise,
  eventsFromThread: (thread) => {
    const user = thread.turns[0].items[0];
    return [
      ["USER_INPUT", { text: user.text }],
      ["TOOL_CALL_START", {
        call_id: "task_update_1",
        tool_name: "task_list",
        arguments_json: JSON.stringify({
          action: "update",
          updates: [{ id: 1, status: "in_progress" }],
        }),
      }],
      ["TOOL_CALL_END", {
        call_id: "task_update_1",
        tool_name: "task_list",
        output: "Updated.",
        tool_state: JSON.stringify([{ id: 1, status: "in_progress" }]),
      }],
    ];
  },
  eventsFromNotification: (method, params) => {
    if (method !== "item/completed") return [];
    return [["USER_INPUT", { text: params.item.text }]];
  },
};

require("./load-renderer").evalRenderer(window);
const conversation = window.document.getElementById("conversation");
window.SerfRenderer.init(conversation);

const drainMicrotasks = async () => {
  for (let i = 0; i < 8; i++) await Promise.resolve();
};

(async () => {
  const snapshot = {
    thread: {
      id: "01TEST",
      sessionId: "01TEST",
      status: "inProgress",
      serf: { ref: "local:01TEST", activeTurnId: "turn_active" },
      turns: [{
        id: "turn_snapshot",
        status: "inProgress",
        items: [{ id: "user_snapshot", type: "userMessage", text: "snapshot text" }],
      }],
    },
  };

  resolveReadThread(snapshot);
  notify("item/completed", {
    ref: "local:01TEST",
    threadId: "01TEST",
    turnId: "turn_live",
    item: { id: "user_live", type: "userMessage", text: "live text" },
  });
  await drainMicrotasks();

  assert(conversation.textContent.includes("snapshot text"), "snapshot transcript stayed gated on pending task descriptions");
  assert(conversation.textContent.includes("live text"), "buffered live transcript stayed gated on pending task descriptions");
  const itemCountBeforeTasks = conversation.children.length;
  assert.strictEqual(conversation.querySelectorAll(".task-card").length, 1, "snapshot should render one task card");
  assert.strictEqual(conversation.querySelector(".task-card .plan-step").textContent, "#1", "task should use its numeric label until descriptions arrive");

  resolveTasks([{ id: 1, description: "Indexed task", status: "in_progress" }]);
  await drainMicrotasks();

  assert.strictEqual(conversation.querySelector(".task-card .plan-step").textContent, "Indexed task", "resolved task description did not replace the numeric label");
  assert.strictEqual(conversation.children.length, itemCountBeforeTasks, "task hydration duplicated transcript items");
  assert.strictEqual(conversation.querySelectorAll(".task-card").length, 1, "task hydration duplicated the task card");
  assert.strictEqual(conversation.querySelectorAll(".user-message").length, 2, "task hydration duplicated user transcript items");

  console.log("PASS: transcript paints before task descriptions and task labels hydrate in place");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
