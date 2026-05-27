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
      if (msg.method === "turn/steer" && msg.params.input && msg.params.input[0] && msg.params.input[0].text === "FAIL_REGISTRY_SWITCH") {
        this.dispatch("message", {
          data: JSON.stringify({
            id: msg.id,
            error: {
              code: -32000,
              message: "turn steer failed",
            },
          }),
        });
        return;
      }
      const result = msg.method === "thread/start"
        ? msg.params.harness === "serf"
          ? { thread: { id: "01LOCAL", session_id: "01LOCAL", serf: { ref: "local:01LOCAL" } } }
          : { thread: { id: "th_codex", sessionId: "th_codex", serf: { ref: "codex:th_codex" } } }
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
  await context.window.SerfAppwire.listModels();
  const localStart = await context.window.SerfAppwire.startThread({
    harness: "serf",
    prompt: "hello serf",
    model: "openai/gpt-5.2",
    working_dir: "/work/project",
    non_interactive: true,
    launch_overrides: { nonInteractive: true },
  });
  assert(localStart.ref === "local:01LOCAL", "local appwire start should preserve canonical ref");
  assert(localStart.session_id === "01LOCAL", "local appwire start should read snake_case session_id");
  const codexStart = await context.window.SerfAppwire.startThread({
    harness: "codex",
    prompt: "hello codex",
    model: "openai/gpt-5.1-codex",
    working_dir: "/work/project",
    reasoning_effort: "high",
    launch_overrides: { maxRounds: 4, appReplaySize: 128 },
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
  assert(start.params.launchOverrides && start.params.launchOverrides.maxRounds === 4, "browser appwire start should pass launch overrides");
  assert(start.params.launchOverrides.appReplaySize === 128, "browser appwire start should preserve launch override fields");
  assert(start.params.input && start.params.input[0] && start.params.input[0].text === "hello codex", "browser appwire start should pass input");
  const localStartRequest = sent.find((msg) => msg.method === "thread/start" && msg.params.harness === "serf");
  assert(localStartRequest && localStartRequest.params.nonInteractive === true, "browser appwire start should pass explicit non-interactive mode");
  assert(localStartRequest.params.launchOverrides && localStartRequest.params.launchOverrides.nonInteractive === true,
    "browser appwire start should preserve non-interactive launch override");
  const results = await context.window.SerfAppwire.search("codex");
  assert(results.live.length === 1, "browser appwire search did not return live Codex result");
  assert(results.live[0].id === "codex:th_codex", "browser appwire search should navigate by canonical ref");
  const replayEvents = context.window.SerfAppwire.eventsFromThread({
    id: "th_codex",
    sessionId: "th_codex",
    serf: { ref: "codex:th_codex" },
    turns: [],
  });
  assert(replayEvents[0][1].session_id === "codex:th_codex", "browser appwire replay should keep source-qualified session identity");
  assert(replayEvents[0][1].ref === "codex:th_codex", "browser appwire replay should preserve canonical ref separately");
  await context.window.SerfAppwire.readThread("codex:th_codex", true, true, true);
  const read = sent.find((msg) => msg.method === "thread/read");
  assert(read && read.params.ref === "codex:th_codex", "browser appwire read should pass canonical ref");
  assert(read.params.subscribe === true, "browser appwire hydration read should request live subscription");
  assert(read.params.replaceSubscription === true, "browser appwire hydration read should replace the active live subscription");
  const replayedAssistantEvents = context.window.SerfAppwire.eventsFromThread({
    id: "th_codex",
    sessionId: "th_codex",
    serf: { ref: "codex:th_codex" },
    turns: [{ items: [{ type: "agentMessage", text: "CODEX_REPLAY_OK" }] }],
  });
  const replayAssistantStart = replayedAssistantEvents.findIndex(([kind]) => kind === "ASSISTANT_TEXT_START");
  const replayAssistantEnd = replayedAssistantEvents.findIndex(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_REPLAY_OK");
  assert(replayAssistantStart >= 0 && replayAssistantStart < replayAssistantEnd, "browser appwire replay should start completed assistant text before ending it");
  const replayedSystemEvents = context.window.SerfAppwire.eventsFromThread({
    id: "th_serf",
    sessionId: "th_serf",
    serf: { ref: "local:th_serf" },
    turns: [{ items: [{ type: "systemMessage", description: "System prompt", text: "You are Serf." }] }],
  });
  const systemEvent = replayedSystemEvents.find(([kind]) => kind === "SYSTEM_MESSAGE");
  assert(systemEvent && systemEvent[1].title === "System prompt" && systemEvent[1].text === "You are Serf.", "browser appwire replay should preserve system transcript blocks");
  const completedTurnEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_codex",
      status: "completed",
      items: [
        { type: "userMessage", text: "hello codex", turnId: "turn_codex" },
        { type: "agentMessage", text: "CODEX_LIVE_OK" },
      ],
    },
  });
  assert(completedTurnEvents.some(([kind, data]) => kind === "USER_INPUT" && data.text === "hello codex"), "completed Codex turn should replay live user input");
  assert(completedTurnEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_LIVE_OK"), "completed Codex turn should render live assistant text");
  const appTurnUserEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_2",
      status: "completed",
      items: [{ type: "userMessage", text: "numeric app turn", turnId: "turn_2" }],
    },
  });
  const appTurnUser = appTurnUserEvents.find(([kind]) => kind === "USER_INPUT");
  assert(appTurnUser && !Object.prototype.hasOwnProperty.call(appTurnUser[1], "turn"), "appwire turn id must not be treated as transcript entry index");
  const transcriptIndexedUserEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_2",
      status: "completed",
      items: [{ type: "userMessage", text: "indexed user", turnId: "turn_2", transcriptEntryIndex: 3 }],
    },
  });
  const transcriptIndexedUser = transcriptIndexedUserEvents.find(([kind]) => kind === "USER_INPUT");
  assert(transcriptIndexedUser && transcriptIndexedUser[1].turn === 3, "Serf transcript entry index should drive fork turn metadata");
  let structuredError;
  try {
    await context.window.SerfAppwire.action("01NOINTERRUPT", "interrupt", "turn_web");
  } catch (err) {
    structuredError = err;
  }
  const interrupt = sent.filter((msg) => msg.method === "turn/interrupt").pop();
  assert(interrupt && interrupt.params.expectedTurnId === "turn_web", "browser appwire interrupt should include active turn id");
  assert(structuredError, "browser appwire should reject failed RPC requests");
  assert(structuredError.message === "interrupt is not available", "browser appwire should preserve error message");
  assert(structuredError.code === -32004, "browser appwire should preserve error code");
  assert(structuredError.data && structuredError.data.serfErrorInfo === "actionUnavailable", "browser appwire should preserve structured error data");
  assert(structuredError.serfErrorInfo === "actionUnavailable", "browser appwire should expose serfErrorInfo directly");
  const registryOneFailures = [];
  const registryTwoFailures = [];
  context.window.SerfAppwire.setPendingRegistry({
    register(intent) { return { id: "one", intent }; },
    fail(handle, msg) { registryOneFailures.push({ handle, msg }); },
  });
  const failedTurn = context.window.SerfAppwire.steer("01LOCAL", "turn_1", "FAIL_REGISTRY_SWITCH");
  context.window.SerfAppwire.setPendingRegistry({
    register(intent) { return { id: "two", intent }; },
    fail(handle, msg) { registryTwoFailures.push({ handle, msg }); },
  });
  try {
    await failedTurn;
  } catch (_err) {
    // Expected failure from the fake server above.
  }
  context.window.SerfAppwire.setPendingRegistry(null);
  assert(registryOneFailures.length === 1, "failed optimistic call should fail the registry that registered the chip");
  assert(registryOneFailures[0].handle.id === "one", "failed optimistic call should preserve the original handle");
  assert(registryTwoFailures.length === 0, "failed optimistic call should not fail the current global registry after it changes");

  let startPendingRegistrations = 0;
  context.window.SerfAppwire.setPendingRegistry({
    register() { startPendingRegistrations++; return { id: "start" }; },
    fail() {},
  });
  await context.window.SerfAppwire.startTurn("01LOCAL", "single local echo", []);
  context.window.SerfAppwire.setPendingRegistry(null);
  assert(startPendingRegistrations === 0, "turn/start should not register a pending chip when renderer already appends a local echo");

  context.window.SerfAppwire.eventsFromNotification("item/started", {
    threadId: "th_codex",
    turnId: "turn_dedupe",
    item: { id: "msg_dedupe", type: "agentMessage" },
  });
  const startedAgentSnapshotEvents = context.window.SerfAppwire.eventsFromNotification("item/started", {
    threadId: "th_codex",
    turnId: "turn_started_snapshot",
    item: { id: "msg_started_snapshot", type: "agentMessage", text: "started prefix" },
  });
  assert(startedAgentSnapshotEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_DELTA" && data.delta === "started prefix"),
    "item/started should render agentMessage snapshot text");
  const startedToolSnapshotEvents = context.window.SerfAppwire.eventsFromNotification("item/started", {
    threadId: "th_tool_started_snapshot",
    turnId: "turn_tool_started_snapshot",
    item: {
      id: "tool_started_snapshot",
      callId: "call_started_snapshot",
      type: "commandExecution",
      toolName: "shell",
      output: "started output",
    },
  });
  assert(startedToolSnapshotEvents.some(([kind, data]) => kind === "TOOL_CALL_OUTPUT_DELTA" && data.delta === "started output"),
    "item/started should render commandExecution snapshot output");
  context.window.SerfAppwire.eventsFromNotification("item/agentMessage/delta", {
    threadId: "th_codex",
    turnId: "turn_dedupe",
    itemId: "msg_dedupe",
    delta: "CODEX_DEDUPE_",
  });
  context.window.SerfAppwire.eventsFromNotification("item/completed", {
    threadId: "th_codex",
    turnId: "turn_dedupe",
    item: { id: "msg_dedupe", turnId: "turn_dedupe", type: "agentMessage", text: "CODEX_DEDUPE_OK" },
  });
  assert(typeof context.window.SerfAppwire.liveItemStateSize === "function", "browser appwire should expose live item state size for regression coverage");
  const liveStateBeforeCompletedTurn = context.window.SerfAppwire.liveItemStateSize();
  const dedupedCompletedTurnEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_dedupe",
      status: "completed",
      items: [{ id: "msg_dedupe", turnId: "turn_dedupe", type: "agentMessage", text: "CODEX_DEDUPE_OK" }],
    },
  });
  assert(!dedupedCompletedTurnEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_DEDUPE_OK"), "completed turn should not replay already completed assistant item");
  assert(context.window.SerfAppwire.liveItemStateSize() === liveStateBeforeCompletedTurn - 1, "completed turn should evict reconciled live item state");
  const userStartedEvents = context.window.SerfAppwire.eventsFromNotification("item/started", {
    threadId: "th_codex",
    turnId: "turn_user_dedupe",
    item: { id: "user_dedupe", type: "userMessage", text: "USER_DEDUPE_OK" },
  });
  assert(userStartedEvents.some(([kind, data]) => kind === "USER_INPUT" && data.text === "USER_DEDUPE_OK"),
    "item/started should render user input");
  const userCompletedEvents = context.window.SerfAppwire.eventsFromNotification("item/completed", {
    threadId: "th_codex",
    turnId: "turn_user_dedupe",
    item: { id: "user_dedupe", turnId: "turn_user_dedupe", type: "userMessage", text: "USER_DEDUPE_OK" },
  });
  assert(!userCompletedEvents.some(([kind]) => kind === "USER_INPUT"),
    "item/completed should not duplicate user input already rendered from item/started");
  context.window.SerfAppwire.eventsFromNotification("item/started", {
    threadId: "th_failed",
    turnId: "turn_failed",
    item: { id: "msg_failed", type: "agentMessage" },
  });
  context.window.SerfAppwire.eventsFromNotification("item/agentMessage/delta", {
    threadId: "th_failed",
    turnId: "turn_failed",
    itemId: "msg_failed",
    delta: "partial failure",
  });
  const liveStateBeforeFailedTurn = context.window.SerfAppwire.liveItemStateSize();
  const failedTurnEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_failed",
    turn: {
      id: "turn_failed",
      status: "failed",
      error: {
        message: "turn failed for test",
        source: "serf",
        title: "Serf configuration error",
        hint: "Check launch config.",
        cause: { kind: "provider", provider: "openai", model: "gpt-5", status: 503 },
      },
      items: [{ id: "msg_failed", turnId: "turn_failed", type: "agentMessage", text: "final failure" }],
    },
  });
  const failedTurnAssistant = failedTurnEvents.find(([kind]) => kind === "ASSISTANT_TEXT_END");
  assert(failedTurnAssistant && failedTurnAssistant[1].text === "final failure",
    "failed turn should replay final transcript items before ERROR");
  const failedTurnError = failedTurnEvents.find(([kind]) => kind === "ERROR");
  assert(failedTurnError, "failed turn should render an error event");
  assert(failedTurnError[1].source === "serf", "failed turn error should preserve source");
  assert(failedTurnError[1].title === "Serf configuration error", "failed turn error should preserve title");
  assert(failedTurnError[1].hint === "Check launch config.", "failed turn error should preserve hint");
  assert(failedTurnError[1].cause && failedTurnError[1].cause.kind === "provider", "failed turn error should preserve cause");
  assert(context.window.SerfAppwire.liveItemStateSize() === liveStateBeforeFailedTurn - 1, "failed turn should evict live item state");
  const reusedItemNextTurnEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_codex",
    turn: {
      id: "turn_reused",
      status: "completed",
      items: [{ id: "msg_dedupe", turnId: "turn_reused", type: "agentMessage", text: "CODEX_REUSED_TURN_OK" }],
    },
  });
  assert(reusedItemNextTurnEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_REUSED_TURN_OK"), "completed turn should render same item id reused in a later turn");
  const reusedItemOtherThreadEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_other",
    turn: {
      id: "turn_other",
      status: "completed",
      items: [{ id: "msg_dedupe", type: "agentMessage", text: "CODEX_OTHER_OK" }],
    },
  });
  assert(reusedItemOtherThreadEvents.some(([kind, data]) => kind === "ASSISTANT_TEXT_END" && data.text === "CODEX_OTHER_OK"), "completed turn should render same item id in a different thread");
  context.window.SerfAppwire.eventsFromNotification("item/toolOutput/delta", {
    threadId: "th_tool_delta",
    itemId: "tool_delta_only",
    callId: "call_delta_only",
    delta: "partial output",
  });
  const deltaOnlyToolFinalEvents = context.window.SerfAppwire.eventsFromNotification("turn/completed", {
    threadId: "th_tool_delta",
    turn: {
      id: "turn_tool_delta",
      status: "completed",
      items: [{
        id: "tool_delta_only",
        callId: "call_delta_only",
        type: "commandExecution",
        toolName: "shell",
        output: "partial output",
        status: "completed",
      }],
    },
  });
  const deltaOnlyStart = deltaOnlyToolFinalEvents.findIndex(([kind]) => kind === "TOOL_CALL_START");
  const deltaOnlyEnd = deltaOnlyToolFinalEvents.findIndex(([kind]) => kind === "TOOL_CALL_END");
  assert(deltaOnlyStart >= 0 && deltaOnlyStart < deltaOnlyEnd, "delta-only tool final snapshot should synthesize start before end");
  assert(typeof context.window.SerfAppwire.onConnectionLost === "function", "browser appwire should expose connection-loss callbacks");
  let lostCount = 0;
  context.window.SerfAppwire.onConnectionLost(() => { lostCount++; });
  FakeWebSocket.instances[0].dispatch("close", {});
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert(lostCount === 1, "browser appwire should notify connection-loss callbacks on websocket close");
  console.log("PASS: appwire browser client uses JSON-RPC-lite envelopes");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
