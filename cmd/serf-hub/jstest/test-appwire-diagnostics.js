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
