// appwire client surface for lazy transcript loading: readThread forwards a
// turnLimit, listTurns shapes the thread/turns/list request and reply, and
// eventsFromTurns yields the per-turn events that eventsFromThread emits after
// its SESSION_START/QUEUE seed.
const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/appwire.js", "utf8");

function assert(cond, msg) {
  if (!cond) { console.error("FAIL: " + msg); process.exit(1); }
}

const sent = [];
class FakeWebSocket {
  constructor() {
    this.readyState = 1;
    this.listeners = new Map();
    setTimeout(() => this.dispatch("open", {}), 0);
  }
  addEventListener(name, h) { (this.listeners.get(name) || this.listeners.set(name, []).get(name)).push(h); }
  send(raw) {
    const msg = JSON.parse(raw);
    sent.push(msg);
    setTimeout(() => {
      if (msg.method === "initialize") {
        this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: { features: {} } }) });
      } else if (msg.method === "thread/turns/list") {
        this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: { data: [{ id: "turn_3" }], nextCursor: "2" } }) });
      } else {
        this.dispatch("message", { data: JSON.stringify({ id: msg.id, result: {} }) });
      }
    }, 0);
  }
  close() { this.readyState = 3; setTimeout(() => this.dispatch("close", {}), 0); }
  dispatch(name, ev) { for (const h of this.listeners.get(name) || []) h(ev); }
}

const context = {
  window: { addEventListener() {}, location: { protocol: "http:", host: "127.0.0.1:9180", pathname: "/" } },
  document: { addEventListener() {}, querySelector() { return null; }, body: { dataset: {} } },
  WebSocket: FakeWebSocket,
  fetch: async () => ({ ok: true, json: async () => ({}) }),
  console, setTimeout, clearTimeout,
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(SRC, context);
const wire = context.window.SerfAppwire;

(async () => {
  // readThread forwards a positive turnLimit; omits it when zero.
  await wire.readThread("s1", true, true, true, 40);
  const read = sent.find((m) => m.method === "thread/read");
  assert(read && read.params.turnLimit === 40, "readThread forwards turnLimit");

  // listTurns shapes the request and normalizes the reply.
  const page = await wire.listTurns("s1", "cursor-5", 30);
  const list = sent.find((m) => m.method === "thread/turns/list");
  assert(list && list.params.cursor === "cursor-5" && list.params.limit === 30, "listTurns sends cursor+limit");
  assert(Array.isArray(page.turns) && page.turns.length === 1 && page.turns[0].id === "turn_3", "listTurns returns turns");
  assert(page.nextCursor === "2", "listTurns returns nextCursor");

  // eventsFromTurns yields per-turn events with no SESSION_START; eventsFromThread
  // leads with SESSION_START then the same turn events.
  const thread = { turns: [{ id: "turn_1", status: "completed", items: [{ type: "userMessage", text: "hi", id: "i1" }] }] };
  const turnEvents = wire.eventsFromTurns(thread.turns);
  assert(turnEvents.length > 0 && turnEvents.every(([k]) => k !== "SESSION_START"), "eventsFromTurns has no SESSION_START");
  assert(turnEvents.some(([k]) => k === "USER_INPUT"), "eventsFromTurns includes the turn's user input");

  const threadEvents = wire.eventsFromThread(thread);
  assert(threadEvents[0][0] === "SESSION_START", "eventsFromThread still leads with SESSION_START");
  assert(threadEvents.some(([k]) => k === "USER_INPUT"), "eventsFromThread still includes turn events after refactor");

  console.log("PASS: appwire lazy-loading client surface");
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
