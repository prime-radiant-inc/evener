const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/appwire.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

class FakeWebSocket {
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    this.initialized = false;
    this.listeners = new Map();
    this.sent = [];
    FakeWebSocket.instances.push(this);
    setTimeout(() => {
      this.readyState = FakeWebSocket.OPEN;
      this.dispatch("open", {});
    }, 0);
  }

  addEventListener(name, handler) {
    const handlers = this.listeners.get(name) || [];
    handlers.push(handler);
    this.listeners.set(name, handlers);
  }

  send(raw) {
    if (this.readyState !== FakeWebSocket.OPEN) {
      throw new Error("WebSocket is already in CLOSING or CLOSED state.");
    }
    const msg = JSON.parse(raw);
    this.sent.push(msg);
    setTimeout(() => {
      if (msg.method === "initialize") {
        this.initialized = true;
        this.dispatch("message", {
          data: JSON.stringify({ id: msg.id, result: { serverInfo: { name: "fake" } } }),
        });
        return;
      }
      if (msg.method === "model/list") {
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            result: { data: [{ provider: "openai", model: "gpt-5.2" }] },
          }),
        });
      }
    }, 0);
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
    setTimeout(() => this.dispatch("close", {}), 0);
  }

  dispatch(name, event) {
    for (const handler of this.listeners.get(name) || []) handler(event);
  }
}
FakeWebSocket.CONNECTING = 0;
FakeWebSocket.OPEN = 1;
FakeWebSocket.CLOSING = 2;
FakeWebSocket.CLOSED = 3;
FakeWebSocket.instances = [];

const context = {
  window: {
    addEventListener() {},
    location: { protocol: "http:", host: "127.0.0.1:9180", pathname: "/" },
  },
  document: { addEventListener() {}, querySelector() { return null; }, body: { dataset: {} } },
  WebSocket: FakeWebSocket,
  fetch: async () => ({ ok: true, json: async () => ({}) }),
  console,
  setTimeout,
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(SRC, context);

(async () => {
  const firstModels = await context.window.SerfAppwire.listModels();
  assert(firstModels.length === 1, "initial request should succeed");

  const first = FakeWebSocket.instances[0];
  first.readyState = FakeWebSocket.CLOSING;

  const firstRetry = context.window.SerfAppwire.listModels();
  first.dispatch("error", {});
  const concurrentRetry = context.window.SerfAppwire.listModels();

  const [modelsA, modelsB] = await Promise.all([firstRetry, concurrentRetry]);
  assert(modelsA.length === 1 && modelsB.length === 1, "requests should recover on the replacement socket");
  assert(FakeWebSocket.instances.length === 2, "concurrent retries should share one replacement socket");

  const replacement = FakeWebSocket.instances[1];
  const replacementMethods = replacement.sent.map((msg) => msg.method);
  assert(replacementMethods[0] === "initialize", "replacement socket should initialize before requests");
  assert(replacementMethods.filter((method) => method === "model/list").length === 2,
    "both retries should be sent on the initialized replacement socket");

  console.log("PASS: appwire replaces closing sockets and ignores stale socket errors");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
