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
    this.readyState = FakeWebSocket.OPEN;
    this.initialized = false;
    this.listeners = new Map();
    FakeWebSocket.instances.push(this);
    setTimeout(() => this.dispatch("open", {}), 0);
  }

  addEventListener(name, handler) {
    const handlers = this.listeners.get(name) || [];
    handlers.push(handler);
    this.listeners.set(name, handlers);
  }

  send(raw) {
    const msg = JSON.parse(raw);
    this.sent = this.sent || [];
    this.sent.push(msg);
    setTimeout(() => {
      if (msg.method === "initialize") {
        if (FakeWebSocket.instances.length === 1) {
          this.dispatch("message", {
            data: JSON.stringify({
              id: msg.id,
              error: { code: -32603, message: "initialize failed" },
            }),
          });
          return;
        }
        this.initialized = true;
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            result: { serverInfo: { name: "fake" } },
          }),
        });
        return;
      }
      if (!this.initialized) {
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            error: { code: -32600, message: "initialize required" },
          }),
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
        return;
      }
      this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: {} }) });
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
FakeWebSocket.OPEN = 1;
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
  let firstError;
  try {
    await context.window.SerfAppwire.listModels();
  } catch (err) {
    firstError = err;
  }
  assert(firstError && firstError.message === "initialize failed", "first call should fail during initialize");

  const models = await context.window.SerfAppwire.listModels();
  assert(models.length === 1 && models[0].model === "gpt-5.2", "second call should reconnect and succeed");
  assert(FakeWebSocket.instances.length === 2, "second call should use a fresh WebSocket after initialize failure");

  console.log("PASS: appwire reconnects after initialize failure");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
