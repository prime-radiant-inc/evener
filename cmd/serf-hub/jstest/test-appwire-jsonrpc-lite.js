const fs = require("fs");
const vm = require("vm");

const SRC = fs.readFileSync("../assets/appwire.js", "utf8");
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
      if (msg.method === "turn/interrupt") {
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            error: {
              code: -32004,
              message: "interrupt is not available",
              data: { serfErrorInfo: "actionUnavailable" },
            },
          }),
        });
        return;
      }
      const result = msg.method === "thread/start"
        ? { thread: { id: "th_codex", sessionId: "th_codex", serf: { ref: "codex:th_codex" } } }
        : msg.method === "thread/list"
          ? { data: [{ id: "th_codex", sessionId: "th_codex", preview: "Codex task", status: { type: "idle" }, serf: { ref: "codex:th_codex" } }] }
          : msg.method === "model/list"
            ? { data: [] }
            : { serverInfo: { name: "fake" } };
      this.dispatch("message", {
        data: JSON.stringify({
          id: msg.id,
          result,
        }),
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
  await context.window.SerfAppwire.listModels();
  const localStart = await context.window.SerfAppwire.startThread({
    harness: "serf",
    prompt: "hello serf",
    model: "openai/gpt-5.2",
    working_dir: "/work/project",
  });
  assert(localStart.ref === "local:01LOCAL", "local appwire start should preserve canonical ref");
  assert(localStart.session_id === "01LOCAL", "local appwire start should read snake_case session_id");
  const codexStart = await context.window.SerfAppwire.startThread({
    harness: "codex",
    prompt: "hello codex",
    model: "openai/gpt-5.1-codex",
    working_dir: "/work/project",
    reasoning_effort: "high",
  });
  assert(codexStart.ref === "codex:th_codex", "codex appwire start should preserve canonical ref");
  assert(sent.length >= 2, "initialize and model/list should be sent");
  for (const msg of sent) {
    assert(!Object.prototype.hasOwnProperty.call(msg, "jsonrpc"), "browser appwire request included jsonrpc");
  }
  const start = sent.find((msg) => msg.method === "thread/start" && msg.params.harness === "codex");
  assert(start && start.params.harness === "codex", "browser appwire start did not pass harness");
  assert(!start.params.sourceId, "browser appwire start overloaded sourceId instead of harness");
  assert(start.params.modelProvider === "", "browser appwire start should leave model interpretation to the harness");
  assert(start.params.model === "openai/gpt-5.1-codex", "browser appwire start should pass the raw model to the harness");
  assert(start.params.reasoningEffort === "high", "browser appwire start should pass reasoning effort");
  assert(start.params.prompt === "hello codex", "browser appwire start should pass prompt");
  const results = await context.window.SerfAppwire.search("codex");
  assert(results.live.length === 1, "browser appwire search did not return live Codex result");
  assert(results.live[0].id === "codex:th_codex", "browser appwire search should navigate by canonical ref");
  const replayEvents = context.window.SerfAppwire.eventsFromThread({
    id: "th_codex",
    sessionId: "th_codex",
    serf: { ref: "codex:th_codex" },
    turns: [],
  });
  assert(replayEvents[0][1].session_id === "th_codex", "browser appwire replay should keep session_id as the bare thread id");
  assert(replayEvents[0][1].ref === "codex:th_codex", "browser appwire replay should preserve canonical ref separately");
  const replayedAssistantEvents = context.window.SerfAppwire.eventsFromThread({
    id: "th_codex",
    sessionId: "th_codex",
    serf: { ref: "codex:th_codex" },
    turns: [{ items: [{ type: "agent_message", text: "CODEX_REPLAY_OK" }] }],
  });
  const replayAssistantStart = replayedAssistantEvents.findIndex(([kind]) => kind === "ASSISTANT_TEXT_START");
  const replayAssistantEnd = replayedAssistantEvents.findIndex(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_REPLAY_OK");
  assert(replayAssistantStart >= 0 && replayAssistantStart < replayAssistantEnd, "browser appwire replay should start completed assistant text before ending it");
  const completedTurnEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_codex",
      status: "completed",
      items: [
        { type: "user_message", text: "hello codex", turnId: "turn_codex" },
        { type: "agent_message", text: "CODEX_LIVE_OK" },
      ],
    },
  });
  assert(completedTurnEvents.some(([kind, data]) => kind === "USER_INPUT" && data.text === "hello codex"), "completed Codex turn should replay live user input");
  assert(completedTurnEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_LIVE_OK"), "completed Codex turn should render live assistant text");
  let structuredError;
  try {
    await context.window.SerfAppwire.action("01NOINTERRUPT", "interrupt");
  } catch (err) {
    structuredError = err;
  }
  assert(structuredError, "browser appwire should reject failed RPC requests");
  assert(structuredError.message === "interrupt is not available", "browser appwire should preserve error message");
  assert(structuredError.code === -32004, "browser appwire should preserve error code");
  assert(structuredError.data && structuredError.data.serfErrorInfo === "actionUnavailable", "browser appwire should preserve structured error data");
  assert(structuredError.serfErrorInfo === "actionUnavailable", "browser appwire should expose serfErrorInfo directly");
  console.log("PASS: appwire browser client uses JSON-RPC-lite envelopes");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
