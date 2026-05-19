// Regression: a routed Serf session id can differ from the canonical
// AppWire thread ref returned by thread/read. Live notifications must still
// render in the open session after a Hub-routed send.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const rendererSrc = fs.readFileSync(path.resolve(__dirname, "../assets/renderer.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-action-trigger="interrupt" disabled>interrupt</button>
    <button data-action-trigger="compact">compact</button>
    <button data-action-trigger="shutdown">shutdown</button>
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="sess-route"></header>
  <div id="conversation"
       data-session-id="sess-route"
       data-state="idle"></div>
  <form data-input-form data-session-id="sess-route">
    <textarea class="message-input"></textarea>
    <button class="send-btn" type="submit">Send</button>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.confirm = () => true;

let notificationHandler = null;
let startTurnTarget = "";

window.SerfAppwire = {
  tasks: () => Promise.resolve([]),
  onNotification: (handler) => {
    notificationHandler = handler;
    return () => {};
  },
  refForSession: (sessionId) => {
    if (!sessionId) return "";
    if (String(sessionId).includes(":")) return String(sessionId);
    return "local:" + sessionId;
  },
  readThread: () => Promise.resolve({
    thread: {
      id: "thread-live",
      sessionId: "sess-route",
      serf: { ref: "local:thread-live" },
      turns: [],
    },
  }),
  eventsFromThread: (thread) => [["SESSION_START", {
    session_id: thread.sessionId,
    status: "processing",
    capabilities: { queue: false },
    restored: true,
  }]],
  eventsFromNotification: (method, params) => {
    if (method === "thread/status/changed") return [["THREAD_STATUS_CHANGED", { status: params.status && params.status.type || "" }]];
    if (method === "turn/started") return [["TURN_STARTED", { turnId: params.turn && params.turn.id || "" }]];
    if (method === "turn/completed") return [["TURN_COMPLETED", { turnId: params.turn && params.turn.id || "" }]];
    if (method === "item/started") return [["ASSISTANT_TEXT_START", {}]];
    if (method === "item/agentMessage/delta") return [["ASSISTANT_TEXT_DELTA", { delta: params.delta || "" }]];
    if (method === "item/completed" && params.item && params.item.type === "user_message") {
      return [["USER_INPUT", { text: params.item.text || "", images: params.item.images || [] }]];
    }
    return [];
  },
  startTurn: (sessionIdOrRef) => {
    startTurnTarget = sessionIdOrRef;
    return Promise.resolve({ turn: { id: "turn_1", status: "running" } });
  },
};

window.eval(rendererSrc);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

async function run() {
  await new Promise((resolve) => setTimeout(resolve, 30));

  const ta = window.document.querySelector(".message-input");
  ta.value = "ping";
  const form = window.document.querySelector("[data-input-form]");
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 30));

  const failures = [];
  const pass = (condition, message) => { if (!condition) failures.push("FAIL: " + message); };
  pass(window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "interrupt should start disabled without an active turn");

  pass(startTurnTarget === "local:thread-live", "send should target canonical AppWire ref, got " + startTurnTarget);
  pass(typeof notificationHandler === "function", "renderer did not subscribe to AppWire notifications");

  const userMessages = () => Array.from(conv.querySelectorAll(".user-message-text")).map((el) => el.textContent);
  pass(userMessages().filter((text) => text === "ping").length === 1, "sent prompt should render before AppWire user-message notification");

  notificationHandler("item/completed", {
    threadId: "thread-live",
    ref: "local:thread-live",
    turnId: "turn_1",
    item: { type: "user_message", id: "item_user_1", turnId: "turn_1", text: "ping", status: "completed" },
  });
  pass(userMessages().filter((text) => text === "ping").length === 1, "AppWire user-message notification should not duplicate the sent prompt");

  notificationHandler("item/started", {
    threadId: "thread-live",
    ref: "local:thread-live",
    item: { type: "agent_message", id: "item_1", turnId: "turn_1" },
  });
  notificationHandler("item/agentMessage/delta", {
    threadId: "thread-live",
    ref: "local:thread-live",
    turnId: "turn_1",
    itemId: "item_1",
    delta: "hello from live turn",
  });
  await new Promise((resolve) => setTimeout(resolve, 10));

  const assistant = conv.querySelector(".assistant-message");
  pass(assistant && assistant.textContent.includes("hello from live turn"), "live AppWire update was not rendered");

  notificationHandler("thread/status/changed", {
    threadId: "thread-live",
    ref: "local:thread-live",
    status: { type: "processing" },
  });
  pass(conv.dataset.state === "processing", "processing status did not update conversation state");
  pass(window.document.querySelector(".send-btn").getAttribute("data-capability-queue") === "false",
    "processing status should not enable queue when source did not advertise queue");
  pass(!window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "interrupt should enable while processing after turn/start returns an id");

  notificationHandler("turn/started", {
    threadId: "thread-live",
    ref: "local:thread-live",
    turn: { id: "turn_1", status: "running" },
  });
  pass(!window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "interrupt should enable when a turn starts");

  notificationHandler("thread/status/changed", {
    threadId: "thread-live",
    ref: "local:thread-live",
    status: { type: "active" },
  });
  pass(conv.dataset.state === "active", "active status did not update conversation state");
  pass(!window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "active status should keep interrupt enabled while a turn is active");

  notificationHandler("turn/completed", {
    threadId: "thread-live",
    ref: "local:thread-live",
    turn: { id: "turn_1", status: "completed" },
  });
  pass(window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "interrupt should disable when the active turn completes");

  notificationHandler("thread/status/changed", {
    threadId: "thread-live",
    ref: "local:thread-live",
    status: { type: "idle" },
  });
  pass(conv.dataset.state === "idle", "idle status did not update conversation state");
  pass(window.document.querySelector('[data-action-trigger="interrupt"]').disabled, "interrupt should disable while idle");
  pass(!window.document.querySelector('[data-action-trigger="shutdown"]').disabled, "shutdown should stay enabled while idle");

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }

  console.log("PASS: appwire live session updates render");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
