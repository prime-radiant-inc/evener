// appwire forkThread defer-input mapping (issue #42): the browser client must
// send deferInput (and no editedInput) on thread/fork when the renderer asks
// for a deferred fork, and must surface the response's originalInput as
// original_input so the renderer can stage it in the fork's composer.
const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync(__dirname + "/../assets/appwire.js", "utf8");
const sent = [];

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
    sent.push(msg);
    setTimeout(() => {
      if (msg.method === "thread/fork") {
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            result: {
              thread: { id: "01CHILD", sessionId: "01CHILD", serf: { ref: "local:01CHILD" } },
              originalInput: "original prompt text",
            },
          }),
        });
        return;
      }
      this.dispatch("message", {
        data: JSON.stringify({ id: msg.id, result: { serverInfo: { name: "fake" } } }),
      });
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
  const out = await context.window.SerfAppwire.forkThread("01HOST", { turn: 3, defer_input: true });

  const fork = sent.find((m) => m.method === "thread/fork");
  assert(fork, "thread/fork request should be sent");
  assert(fork.params.sourceTurnId === "3", "sourceTurnId should be the turn, got " + JSON.stringify(fork.params));
  assert(fork.params.deferInput === true, "deferInput should map from defer_input, got " + JSON.stringify(fork.params));
  assert(!fork.params.editedInput, "editedInput must be empty in the defer flow, got " + JSON.stringify(fork.params));
  assert(out.ref === "local:01CHILD", "ref should pass through, got " + JSON.stringify(out));
  assert(out.session_id === "01CHILD", "session_id should pass through, got " + JSON.stringify(out));
  assert(out.original_input === "original prompt text", "originalInput should map to original_input, got " + JSON.stringify(out));

  console.log("PASS: appwire forkThread maps deferInput and originalInput");
  process.exit(0);
})().catch((err) => {
  console.error("FAIL: " + (err && err.message));
  process.exit(1);
});
