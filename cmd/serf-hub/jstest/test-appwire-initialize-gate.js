const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/appwire.js", "utf8");

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

let resolveInitializeSent;
const initializeSent = new Promise((resolve) => { resolveInitializeSent = resolve; });

class FakeWebSocket {
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    this.initialized = false;
    this.listeners = new Map();
    this.sent = [];
    FakeWebSocket.instance = this;
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
    const msg = JSON.parse(raw);
    this.sent.push(msg);
    if (msg.method === "initialize") {
      this.initializeRequest = msg;
      resolveInitializeSent();
      return;
    }
    setTimeout(() => {
      if (!this.initialized) {
        this.dispatch("message", {
          data: JSON.stringify({ id: msg.id, error: { code: -32600, message: "initialize required" } }),
        });
        return;
      }
      this.dispatch("message", {
        data: JSON.stringify({
          id: msg.id,
          result: { data: [{ provider: "openai", model: "gpt-5.2" }] },
        }),
      });
    }, 0);
  }

  completeInitialize() {
    this.initialized = true;
    this.dispatch("message", {
      data: JSON.stringify({ id: this.initializeRequest.id, result: { serverInfo: { name: "fake" } } }),
    });
  }

  dispatch(name, event) {
    for (const handler of this.listeners.get(name) || []) handler(event);
  }
}
FakeWebSocket.CONNECTING = 0;
FakeWebSocket.OPEN = 1;

const context = {
  window: {
    addEventListener() {},
    location: { protocol: "http:", host: "100.114.54.38:9180", pathname: "/" },
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
  const firstRequest = context.window.SerfAppwire.listModels();
  await initializeSent;

  const secondRequest = context.window.SerfAppwire.listModels();
  await Promise.resolve();
  const socket = FakeWebSocket.instance;
  assert(socket.sent.map((msg) => msg.method).join(",") === "initialize",
    "non-initialize RPC was sent before the initialize response");

  socket.completeInitialize();
  const [modelsA, modelsB] = await Promise.all([firstRequest, secondRequest]);
  assert(modelsA.length === 1 && modelsB.length === 1, "both gated requests should complete after initialize");
  assert(socket.sent.filter((msg) => msg.method === "model/list").length === 2,
    "both requests should be sent after initialize completes");

  console.log("PASS: appwire gates all RPCs on initialization, not transport OPEN");
})().catch((err) => {
  console.error(err.message || err);
  process.exit(1);
});
