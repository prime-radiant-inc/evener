// listModelsWithDiagnostics must forward the RPC response's recent field
// (appwire.js's real code path — no mocking of listModelsWithDiagnostics
// itself, unlike test-spawn-model-picker-recent.js which stubs it directly).
// Uses the same FakeWebSocket harness pattern as test-appwire-jsonrpc-lite.js
// to drive a real request()/response round trip through model/list.
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
    this.listeners = new Map();
    setTimeout(() => this.dispatch("open", {}), 0);
  }

  addEventListener(name, handler) {
    const handlers = this.listeners.get(name) || [];
    handlers.push(handler);
    this.listeners.set(name, handlers);
  }

  send(raw) {
    const msg = JSON.parse(raw);
    setTimeout(() => {
      const result = msg.method === "model/list"
        ? {
          data: [{ provider: "anthropic", model: "claude-opus-4-6" }],
          diagnostics: [{ provider: "openai", error: "missing API key" }],
          recent: [{ provider: "anthropic", model: "claude-opus-4-6" }],
        }
        : { serverInfo: { name: "fake" } };
      this.dispatch("message", { data: JSON.stringify({ id: msg.id, result }) });
    }, 0);
  }

  dispatch(name, event) {
    for (const handler of this.listeners.get(name) || []) handler(event);
  }
}
FakeWebSocket.OPEN = 1;

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
  const result = await context.window.SerfAppwire.listModelsWithDiagnostics({});
  assert(Array.isArray(result.models) && result.models.length === 1, "models should be forwarded");
  assert(Array.isArray(result.diagnostics) && result.diagnostics.length === 1, "diagnostics should be forwarded");
  assert(Array.isArray(result.recent) && result.recent.length === 1 && result.recent[0].model === "claude-opus-4-6",
    "listModelsWithDiagnostics should forward the RPC response's recent field, got " + JSON.stringify(result.recent));
  console.log("PASS: appwire listModelsWithDiagnostics forwards recent");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
