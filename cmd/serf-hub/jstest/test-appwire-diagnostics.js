// Node-only coverage for appwire diagnostic fields. Verifies that structured
// failed-turn metadata survives the browser protocol adapter.
const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/appwire.js", "utf8");

const context = {
  window: {
    addEventListener() {},
    location: { protocol: "http:", host: "127.0.0.1:9180", pathname: "/s/01TEST" },
  },
  document: { addEventListener() {}, querySelector() { return null; }, body: { dataset: {} } },
  WebSocket: function () {},
  fetch: async () => ({ ok: true, json: async () => ({}) }),
  console,
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(SRC, context);

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const threadEvents = context.window.SerfAppwire.eventsFromThread({
  id: "01TEST",
  turns: [{
    status: "failed",
    error: {
      message: "configuration error: unknown provider: openrouter",
      source: "serf",
      title: "Serf configuration error",
      hint: "Hub launched Serf with provider configuration this Serf runtime does not recognize.",
    },
  }],
});
const errorEvent = threadEvents.find((event) => event[0] === "ERROR");
assert(errorEvent, "thread replay should emit ERROR");
assert(errorEvent[1].source === "serf", "thread ERROR source missing");
assert(errorEvent[1].title === "Serf configuration error", "thread ERROR title missing");

const emptyQueueEvents = context.window.SerfAppwire.eventsFromThread({
  id: "01TEST",
  serf: { queue: { depth: 0, preview: [] } },
  turns: [],
});
const emptyQueue = emptyQueueEvents.find((event) => event[0] === "QUEUE_CHANGED");
assert(emptyQueue, "thread replay should emit empty QUEUE_CHANGED");
assert(emptyQueue[1].depth === 0, "empty queue depth should be 0");
assert(Array.isArray(emptyQueue[1].preview) && emptyQueue[1].preview.length === 0, "empty queue preview should be []");

const runningAssistantEvents = context.window.SerfAppwire.eventsFromThread({
  id: "01RUN",
  turns: [{
    id: "turn_running",
    status: "inProgress",
    items: [{
      type: "agentMessage",
      id: "item_agent",
      text: "partial answer",
    }],
  }],
});
const runningKinds = runningAssistantEvents.map((event) => event[0]);
assert(runningKinds.includes("ASSISTANT_TEXT_START"), "running assistant should emit start");
assert(runningAssistantEvents.some((event) => event[0] === "ASSISTANT_TEXT_DELTA" && event[1].delta === "partial answer"), "running assistant should replay text as delta");
assert(!runningKinds.includes("ASSISTANT_TEXT_END"), "running assistant should not emit end");

const splitToolEvents = context.window.SerfAppwire.eventsFromThread({
  id: "01TOOL",
  turns: [{
    id: "turn_done",
    status: "completed",
    items: [{
      type: "commandExecution",
      id: "item_tool_start",
      callId: "call_tool",
      toolName: "shell",
      argumentsJson: "{\"command\":\"printf ok\"}",
      status: "inProgress",
    }, {
      type: "commandExecution",
      id: "item_tool_result",
      callId: "call_tool",
      toolName: "shell",
      output: "ok",
      status: "completed",
    }],
  }],
});
assert(splitToolEvents.filter((event) => event[0] === "TOOL_CALL_START").length === 1, "split tool replay should emit one start");
assert(splitToolEvents.filter((event) => event[0] === "TOOL_CALL_END").length === 1, "split tool replay should emit one end");

const warningEvents = context.window.SerfAppwire.eventsFromNotification("warning", {
  message: "configuration error: unknown provider: openrouter",
  source: "serf",
  title: "Serf configuration error",
  hint: "Hub launched Serf with provider configuration this Serf runtime does not recognize.",
  cause: { kind: "provider", provider: "openai", model: "gpt-5", status: 503 },
});
assert(warningEvents.length === 1 && warningEvents[0][0] === "WARNING", "warning event missing");
assert(warningEvents[0][1].source === "serf", "warning source missing");
assert(warningEvents[0][1].hint.includes("Hub launched Serf"), "warning hint missing");
assert(warningEvents[0][1].cause && warningEvents[0][1].cause.kind === "provider", "warning cause missing");

console.log("PASS: appwire diagnostic fields");
