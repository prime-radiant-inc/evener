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
    const task = thread.testTask || { id: 1, status: "in_progress" };
    return [
      ["USER_INPUT", { text: user.text }],
      ["TOOL_CALL_START", {
        call_id: "task_update_1",
        tool_name: "task_list",
        arguments_json: JSON.stringify({
          action: "update",
          updates: [{
            id: task.id,
            status: task.status,
            ...(Array.isArray(task.notes) && task.notes.length ? { notes: task.notes[0] } : {}),
          }],
        }),
      }],
      ["TOOL_CALL_END", {
        call_id: "task_update_1",
        tool_name: "task_list",
        output: "Updated.",
        tool_state: JSON.stringify([task]),
      }],
      ...(thread.testTrailingText ? [["USER_INPUT", { text: thread.testTrailingText }]] : []),
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
  let reject;
  const promise = new Promise((r, j) => { resolve = r; reject = j; });
  return { promise, resolve, reject };
};

const renderUnresolvedTask = (callId, taskId) => {
  window.SerfRenderer.handleData("TOOL_CALL_START", {
    call_id: callId,
    tool_name: "task_list",
    arguments_json: JSON.stringify({ action: "update", updates: [{ id: taskId, status: "in_progress" }] }),
  });
  window.SerfRenderer.handleData("TOOL_CALL_END", {
    call_id: callId,
    tool_name: "task_list",
    output: "Updated.",
    tool_state: JSON.stringify([{ id: taskId, status: "in_progress" }]),
  });
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

  const issuedFirst = deferred();
  const issuedLatest = deferred();
  let issuedOrderReads = 0;
  tasksImpl = () => (++issuedOrderReads === 1 ? issuedFirst.promise : issuedLatest.promise);
  readThreadImpl = (sessionId) => Promise.resolve({
    thread: { id: sessionId, sessionId, serf: { ref: "local:" + sessionId }, turns: [] },
  });
  const issuedOrderConversation = window.document.createElement("div");
  issuedOrderConversation.dataset.sessionId = "issued-order-session";
  conversation.replaceWith(issuedOrderConversation);
  window.SerfRenderer.init(issuedOrderConversation);
  renderUnresolvedTask("issued_order_task", 101);
  window.SerfRenderer.handleData("TASKS_CHANGED", { done: 0, total: 1 });
  const issuedOrderBadge = window.document.querySelector(".panel-toggle-badge");
  issuedFirst.resolve([{ id: 101, description: "Stale issued-first metadata", status: "done" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.has(101), false, "request N mutated cache after N+1 was issued");
  assert.strictEqual(issuedOrderConversation.querySelector(".plan-step").textContent, "#101", "request N mutated a plan label after N+1 was issued");
  assert.strictEqual(issuedOrderBadge.textContent, "0/1", "request N mutated the pushed task badge after N+1 was issued");
  issuedLatest.resolve([{ id: 101, description: "Latest issued metadata", status: "in_progress" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.get(101), "Latest issued metadata", "latest issued response did not apply");

  const rejectedOlder = deferred();
  const rejectedLatest = deferred();
  let rejectedOrderReads = 0;
  tasksImpl = () => (++rejectedOrderReads === 1 ? rejectedOlder.promise : rejectedLatest.promise);
  const rejectedOrderConversation = window.document.createElement("div");
  rejectedOrderConversation.dataset.sessionId = "rejected-order-session";
  issuedOrderConversation.replaceWith(rejectedOrderConversation);
  window.SerfRenderer.init(rejectedOrderConversation);
  renderUnresolvedTask("rejected_order_task", 102);
  window.SerfRenderer.handleData("TASKS_CHANGED", { done: 0, total: 1 });
  const rejectedOrderBadge = window.document.querySelector(".panel-toggle-badge");
  rejectedLatest.reject(new Error("newest task read failed"));
  await drainMicrotasks();
  rejectedOlder.resolve([{ id: 102, description: "Stale after latest rejection", status: "done" }]);
  await drainMicrotasks();
  assert.strictEqual(window.SerfRendererInternal.taskDescriptions.has(102), false, "request N mutated cache after N+1 rejected");
  assert.strictEqual(rejectedOrderConversation.querySelector(".plan-step").textContent, "#102", "request N mutated a plan label after N+1 rejected");
  assert.strictEqual(rejectedOrderBadge.textContent, "0/1", "request N mutated the pushed task badge after N+1 rejected");

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

  const tasksBeforeRead = deferred();
  const readAfterTasks = deferred();
  let tasksFirstReads = 0;
  tasksImpl = () => (++tasksFirstReads === 1 ? tasksBeforeRead.promise : new Promise(() => {}));
  readThreadImpl = () => readAfterTasks.promise;
  const tasksFirstConversation = window.document.createElement("div");
  tasksFirstConversation.dataset.sessionId = "tasks-first-session";
  conversationB.replaceWith(tasksFirstConversation);
  window.SerfRenderer.init(tasksFirstConversation);
  tasksBeforeRead.resolve([{ id: 301, description: "Cached before transcript", status: "done" }]);
  await drainMicrotasks();
  assert.strictEqual(tasksFirstConversation.querySelector(".task-card"), null, "tasks-first fixture created a plan before readThread resolved");
  window.SerfRendererInternal.rememberTask({ id: 301, description: "Stale panel description", status: "open" });
  readAfterTasks.resolve({
    thread: {
      id: "tasks-first-session",
      sessionId: "tasks-first-session",
      serf: { ref: "local:tasks-first-session" },
      turns: [{ items: [{ type: "userMessage", text: "tasks-first snapshot" }] }],
      testTask: { id: 301, status: "in_progress", notes: ["transcript note"] },
      testTrailingText: "after plan",
    },
  });
  await drainMicrotasks();
  const tasksFirstCard = tasksFirstConversation.querySelector(".task-card");
  const tasksFirstTrailing = Array.from(tasksFirstConversation.querySelectorAll(".user-message"))
    .find((el) => el.textContent.includes("after plan"));
  assert(tasksFirstCard, "readThread did not create the tasks-first transcript plan");
  const tasksFirstLabel = tasksFirstCard.querySelector(".plan-step");
  assert.strictEqual(tasksFirstLabel.textContent, "Cached before transcript", "cached task description did not hydrate the later transcript plan label");
  assert.strictEqual(tasksFirstLabel.dataset.taskId, "301", "later transcript row did not retain stable originating task identity");
  assert.strictEqual(tasksFirstLabel.dataset.taskLabelUnresolved, undefined, "cached description was incorrectly marked unresolved");
  assert.strictEqual(tasksFirstConversation.querySelector(".task-card"), tasksFirstCard, "tasks-first hydration replaced the transcript plan card node");
  assert.strictEqual(tasksFirstCard.nextElementSibling, tasksFirstTrailing, "tasks-first hydration changed transcript plan order");
  assert.strictEqual(tasksFirstCard.querySelector(".task-card-progress").textContent, "0 / 1", "cached metadata replaced transcript-derived progress");
  assert(tasksFirstCard.querySelector(".task-card-row").classList.contains("current"), "cached metadata replaced transcript-derived task status");
  assert.strictEqual(tasksFirstCard.querySelector(".task-card-note").textContent, "transcript note", "cached metadata replaced transcript-derived task content");

  const transcriptHashTasks = deferred();
  const transcriptHashRead = deferred();
  tasksImpl = () => transcriptHashTasks.promise;
  readThreadImpl = () => transcriptHashRead.promise;
  const transcriptHashConversation = window.document.createElement("div");
  transcriptHashConversation.dataset.sessionId = "transcript-hash-session";
  tasksFirstConversation.replaceWith(transcriptHashConversation);
  window.SerfRenderer.init(transcriptHashConversation);
  transcriptHashRead.resolve({
    thread: {
      id: "transcript-hash-session",
      sessionId: "transcript-hash-session",
      serf: { ref: "local:transcript-hash-session" },
      turns: [{ items: [{ type: "userMessage", text: "hash-label snapshot" }] }],
      testTask: { id: 201, description: "#202", status: "in_progress" },
    },
  });
  await drainMicrotasks();
  const transcriptHashLabel = transcriptHashConversation.querySelector(".plan-step");
  assert.strictEqual(transcriptHashLabel.textContent, "#202", "transcript hash-label fixture did not preserve task 201's description");
  window.SerfRenderer.applyTasks([{ id: 202, description: "Second task", status: "open" }]);
  assert.strictEqual(transcriptHashLabel.textContent, "#202", "transcript task 201's legitimate #202 description was reinterpreted as task 202's placeholder");

  tasksImpl = () => new Promise(() => {});
  const stableLabelConversation = window.document.createElement("div");
  stableLabelConversation.dataset.sessionId = "stable-label-session";
  conversationB.replaceWith(stableLabelConversation);
  window.SerfRenderer.init(stableLabelConversation);
  window.SerfRendererInternal.taskDescriptions.delete(401);
  window.SerfRendererInternal.taskDescriptions.delete(402);
  renderUnresolvedTask("stable_label_task", 401);
  const stableLabel = stableLabelConversation.querySelector(".plan-step");
  assert.strictEqual(stableLabel.textContent, "#401", "stable-label fixture did not render an unresolved task placeholder");
  assert.strictEqual(window.SerfRenderer.taskDescriptionsForRows && window.SerfRenderer.taskDescriptionsForRows.has(301), false, "renderer re-init leaked guarded task descriptions from the prior session");
  window.SerfRenderer.applyTasks([{ id: 401, description: "#402", status: "in_progress" }]);
  assert.strictEqual(stableLabel.textContent, "#402", "task 401's hash-prefixed description did not hydrate");
  window.SerfRenderer.applyTasks([{ id: 402, description: "Second task", status: "open" }]);
  assert.strictEqual(stableLabel.textContent, "#402", "task 401's legitimate #402 description was reinterpreted as task 402's placeholder");

  console.log("PASS: transcript paints before task descriptions and task labels hydrate in place");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
