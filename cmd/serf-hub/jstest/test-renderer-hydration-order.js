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
let tasksImpl = () => tasksPromise;
let readThreadImpl = () => readThreadPromise;

window.SerfAppwire = {
  tasks: (sessionId) => tasksImpl(sessionId),
  refForSession: (sessionId) => "local:" + sessionId,
  activeTurnIDFromThread: () => "turn_active",
  onNotification: (handler) => {
    notify = handler;
    return () => {};
  },
  onConnectionLost: () => () => {},
  readThread: (sessionId) => readThreadImpl(sessionId),
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

const deferred = () => {
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  return { promise, resolve };
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
  const planCard = conversation.querySelector(".task-card");
  const liveMessage = Array.from(conversation.querySelectorAll(".user-message"))
    .find((el) => el.textContent.includes("live text"));
  assert.strictEqual(planCard.querySelector(".plan-step").textContent, "#1", "task should use its numeric label until descriptions arrive");
  assert.strictEqual(planCard.nextElementSibling, liveMessage, "live transcript item should initially follow the snapshot plan card");
  const planStateBeforeTasks = {
    className: planCard.querySelector(".task-card-row").className,
    progress: planCard.querySelector(".task-card-progress").textContent,
    complete: !!planCard.querySelector(".task-card-complete"),
  };

  resolveTasks([{ id: 1, description: "Indexed task", status: "done" }]);
  await drainMicrotasks();

  assert.strictEqual(conversation.querySelector(".task-card"), planCard, "task hydration replaced the transcript plan card node");
  assert.strictEqual(planCard.nextElementSibling, liveMessage, "task hydration moved the historical plan card past later transcript items");
  assert.strictEqual(planCard.querySelector(".plan-step").textContent, "Indexed task", "resolved task description did not replace the numeric label");
  assert.deepStrictEqual({
    className: planCard.querySelector(".task-card-row").className,
    progress: planCard.querySelector(".task-card-progress").textContent,
    complete: !!planCard.querySelector(".task-card-complete"),
  }, planStateBeforeTasks, "task hydration replaced transcript-derived plan status or progress");
  assert.strictEqual(conversation.children.length, itemCountBeforeTasks, "task hydration duplicated transcript items");
  assert.strictEqual(conversation.querySelectorAll(".task-card").length, 1, "task hydration duplicated the task card");
  assert.strictEqual(conversation.querySelectorAll(".user-message").length, 2, "task hydration duplicated user transcript items");

  const initialTasks = deferred();
  const newerTasks = deferred();
  let sameSessionTaskReads = 0;
  tasksImpl = () => (++sameSessionTaskReads === 1 ? initialTasks.promise : newerTasks.promise);
  readThreadImpl = (sessionId) => Promise.resolve({
    thread: { id: sessionId, sessionId, serf: { ref: "local:" + sessionId }, turns: [] },
  });
  const orderedConversation = window.document.createElement("div");
  orderedConversation.dataset.sessionId = "ordered-session";
  conversation.replaceWith(orderedConversation);
  window.SerfRenderer.init(orderedConversation);
  window.SerfRenderer.handleData("TASKS_CHANGED", { done: 0, total: 1 });
  newerTasks.resolve([{ id: 1, description: "Newest task metadata", status: "in_progress" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.get(1), "Newest task metadata", "newer same-session task metadata did not apply");

  initialTasks.resolve([{ id: 1, description: "Older initial metadata", status: "open" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.get(1), "Newest task metadata", "older same-session task response overwrote newer metadata");

  const sessionATasks = deferred();
  const sessionBTasks = deferred();
  tasksImpl = (sessionId) => sessionId === "session-A" ? sessionATasks.promise : sessionBTasks.promise;
  readThreadImpl = (sessionId) => Promise.resolve({
    thread: { id: sessionId, sessionId, serf: { ref: "local:" + sessionId }, turns: [] },
  });

  const conversationA = window.document.createElement("div");
  conversationA.dataset.sessionId = "session-A";
  conversation.replaceWith(conversationA);
  window.SerfRenderer.init(conversationA);

  const conversationB = window.document.createElement("div");
  conversationB.dataset.sessionId = "session-B";
  conversationA.replaceWith(conversationB);
  window.SerfRenderer.init(conversationB);
  sessionBTasks.resolve([{ id: 1, description: "Session B task", status: "in_progress" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.get(1), "Session B task", "session B task metadata did not apply");

  sessionATasks.resolve([{ id: 1, description: "Stale session A task", status: "done" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.get(1), "Session B task", "stale session A task metadata mutated session B");

  console.log("PASS: transcript paints before task descriptions and task labels hydrate in place");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
