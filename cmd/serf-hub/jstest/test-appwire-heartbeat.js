// Verifies the appwire app-level heartbeat: when a socket stays OPEN but stops
// answering, the heartbeat ping times out and force-closes it, firing the
// connection-lost lifecycle that drives reconnect. Without this, a silent TCP
// drop leaves the UI desynced ("quiet for N minutes") until a manual refresh.
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
    this.answerPings = true;
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
    setTimeout(() => {
      if (msg.method === "initialize") {
        this.initialized = true;
        this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: { features: {} } }) });
        return;
      }
      if (msg.method === "ping") {
        // A silently-dead socket accepts sends but never replies. Drop the
        // ping to model that.
        if (!this.answerPings) return;
        this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: {} }) });
        return;
      }
      this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: {} }) });
    }, 0);
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return;
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
  clearTimeout,
  setInterval,
  clearInterval,
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(SRC, context);

const wire = context.window.SerfAppwire;

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

(async () => {
  // Fast heartbeat so the test runs in milliseconds.
  wire.setHeartbeatTiming(10, 20);

  let connectionLost = false;
  wire.onConnectionLost(() => { connectionLost = true; });

  // Establish the connection (initialize) via any request.
  await wire.listModels();
  assert(FakeWebSocket.instances.length === 1, "one socket after initial connect");
  const sock = FakeWebSocket.instances[0];

  // A few heartbeats should pass while the socket answers pings.
  await delay(40);
  assert(!connectionLost, "healthy socket must not be reported as lost");
  assert(sock.readyState === FakeWebSocket.OPEN, "healthy socket stays open");

  // Now simulate a silent drop: socket stays OPEN but stops answering.
  sock.answerPings = false;
  await delay(80);

  assert(sock.readyState === FakeWebSocket.CLOSED, "unanswered heartbeat must force-close the dead socket");
  assert(connectionLost, "force-close must fire the connection-lost lifecycle");

  console.log("PASS: appwire heartbeat force-closes a silently-dropped socket");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
