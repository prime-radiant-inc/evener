// Regression: live AppWire tool output deltas are keyed by itemId, while
// tool starts and completions can carry the model callId. The browser must
// attach those deltas to the active tool card without waiting for refresh.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const rendererSrc = fs.readFileSync(path.resolve(__dirname, "../assets/renderer.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-replay-url=""
       data-events-url=""
       data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
    <button class="send-btn" type="submit">Send</button>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
window.confirm = () => true;

class FakeWebSocket {
  constructor() {
    throw new Error("test should not open a websocket");
  }
}
window.WebSocket = FakeWebSocket;

window.eval(appwireSrc);
window.SerfAppwire.tasks = () => Promise.resolve([]);
window.eval(rendererSrc);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

function deliver(method, params) {
  for (const [kind, data] of window.SerfAppwire.eventsFromNotification(method, params)) {
    window.SerfRenderer.handleData(kind, data);
  }
}

async function run() {
  await new Promise((resolve) => setTimeout(resolve, 30));

  deliver("item/started", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: { type: "agent_message", id: "item_empty_1", turnId: "turn_1", status: "running" },
  });
  deliver("item/completed", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: { type: "agent_message", id: "item_empty_1", turnId: "turn_1", status: "completed" },
  });

  deliver("item/started", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: {
      type: "tool_call",
      id: "item_tool_start_1",
      callId: "call_1",
      turnId: "turn_1",
      toolName: "shell",
      argumentsJson: JSON.stringify({ command: "printf 'one\\ntwo\\n'" }),
      status: "running",
    },
  });
  deliver("item/toolOutput/delta", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    itemId: "item_tool_delta_1",
    callId: "call_1",
    delta: "one\n",
  });
  deliver("item/toolOutput/delta", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    itemId: "item_tool_delta_1",
    callId: "call_1",
    delta: "two\n",
  });

  let shellOutput = conv.querySelector(".shell-output");
  const failures = [];
  const pass = (condition, message) => { if (!condition) failures.push("FAIL: " + message); };

  pass(!Array.from(conv.querySelectorAll(".assistant-message")).some((el) => !el.textContent.trim()), "empty live assistant placeholder should be removed");
  pass(shellOutput && shellOutput.textContent.includes("one\ntwo\n"), "live tool output delta did not render before completion");

  const liveStateBeforeToolTurnCompletion = window.SerfAppwire.liveItemStateSize();
  deliver("turn/completed", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turn: {
      id: "turn_1",
      status: "completed",
      items: [{
        type: "tool_call",
        id: "item_tool_result_1",
        callId: "call_1",
        turnId: "turn_1",
        toolName: "shell",
        output: "one\ntwo\n",
        status: "completed",
      }],
    },
  });
  pass(window.SerfAppwire.liveItemStateSize() === liveStateBeforeToolTurnCompletion - 1, "completed turn should evict live tool state keyed by callId");
  deliver("item/started", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: { type: "agent_message", id: "item_msg_1", turnId: "turn_1", status: "running" },
  });
  deliver("item/agentMessage/delta", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    itemId: "item_msg_1",
    delta: "stream still attached",
  });
  deliver("item/started", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: {
      type: "tool_call",
      id: "item_tool_2",
      callId: "call_2",
      turnId: "turn_1",
      toolName: "communicate",
      status: "running",
    },
  });
  deliver("item/completed", {
    threadId: "01TEST",
    ref: "local:01TEST",
    turnId: "turn_1",
    item: {
      type: "tool_call",
      id: "item_tool_2",
      callId: "call_2",
      turnId: "turn_1",
      toolName: "communicate",
      argumentsJson: JSON.stringify({ message: "completed communicate text" }),
      status: "completed",
    },
  });

  shellOutput = conv.querySelector(".shell-output");
  const shellCard = conv.querySelector(".tool-call.shell");
  const assistantMessages = Array.from(conv.querySelectorAll(".assistant-message")).map((el) => el.textContent);
  pass(conv.querySelectorAll(".tool-call.shell").length === 1, "tool completion should not create a duplicate tool card");
  pass(shellCard && shellOutput && shellCard.contains(shellOutput), "tool output should be contained by its tool call card");
  pass(shellOutput && shellOutput.textContent === "one\ntwo\n", "tool output should not replay after streamed deltas; got " + JSON.stringify(shellOutput && shellOutput.textContent));
  pass(assistantMessages.some((text) => text.includes("stream still attached")), "assistant delta after tool completion did not render");
  pass(assistantMessages.some((text) => text.includes("completed communicate text")), "completed communicate arguments did not render");

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }

  console.log("PASS: appwire live tool output stream stays attached");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
