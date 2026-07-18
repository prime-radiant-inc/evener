// A transcript window can contain only tool and reasoning records even though
// earlier user/assistant dialogue exists. Cold hydration must page backward
// until it has primary dialogue, while ordinary windows keep the single-read
// fast path.
const { JSDOM } = require("jsdom");

const toolEvents = (id) => [["TOOL_CALL_START", {
  call_id: id, tool_name: "shell", arguments_json: JSON.stringify({ command: "true" }),
}], ["TOOL_CALL_END", {
  call_id: id, tool_name: "shell", output: "done",
}]];

function boot(initialEvents, pages) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01TEST"><span class="status-dot" data-state="idle"></span></header>
    <div class="conversation" id="conversation" data-session-id="01TEST" data-state="idle"></div>
  </body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/01TEST",
  });
  const w = dom.window;
  w.marked = { parse: (text) => text };
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

  const listCalls = [];
  let pageIndex = 0;
  let readLimit = null;
  w.SerfAppwire = {
    tasks: () => Promise.resolve([]),
    refForSession: (sessionId) => "local:" + sessionId,
    activeTurnIDFromThread: () => "",
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    readThread: (_sessionId, _includeTurns, _subscribe, _replaceSubscription, turnLimit) => {
      readLimit = turnLimit;
      return Promise.resolve({
        thread: {
          id: "01TEST", sessionId: "01TEST", status: { type: "completed" },
          serf: { ref: "local:01TEST" }, turns: [{ id: "initial", status: "completed", testEvents: initialEvents }],
        },
        olderCursor: pages.length ? "cursor-initial" : "",
      });
    },
    listTurns: (_sessionId, cursor, limit) => {
      listCalls.push({ cursor, limit });
      return Promise.resolve(pages[pageIndex++]);
    },
    eventsFromThread: (thread) => thread.turns.flatMap((turn) => turn.testEvents || []),
    eventsFromTurns: (turns) => turns.flatMap((turn) => turn.testEvents || []),
  };

  require("./load-renderer").evalRenderer(w);
  const conversation = w.document.getElementById("conversation");
  w.SerfRenderer.init(conversation);
  return { w, conversation, listCalls, readLimit: () => readLimit };
}

async function drainMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

(async () => {
  const sparse = boot(toolEvents("latest"), [
    {
      turns: [{ id: "older-tools", status: "completed", testEvents: toolEvents("older") }],
      nextCursor: "cursor-more",
    },
    {
      turns: [{ id: "user-turn", status: "completed", testEvents: [["USER_INPUT", { text: "ORIGINAL USER MESSAGE" }]] }],
      nextCursor: "cursor-before-dialogue",
    },
  ]);
  await drainMicrotasks();

  if (sparse.readLimit() !== 40) throw new Error("cold read must retain the bounded 40-turn window");
  const cursors = sparse.listCalls.map((call) => call.cursor);
  if (JSON.stringify(cursors) !== JSON.stringify(["cursor-initial", "cursor-more"])) {
    throw new Error("cold hydration must page until primary dialogue, got cursors " + JSON.stringify(cursors));
  }
  if (!sparse.conversation.textContent.includes("ORIGINAL USER MESSAGE")) {
    throw new Error("primary dialogue from an older page was not rendered");
  }
  if (sparse.w.SerfRenderer.olderTurnsCursor !== "cursor-before-dialogue") {
    throw new Error("hydration must retain the cursor for history older than the first dialogue");
  }

  const ordinary = boot([["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: "VISIBLE REPLY" }]], [
    { turns: [], nextCursor: "" },
  ]);
  await drainMicrotasks();
  if (ordinary.listCalls.length !== 0) {
    throw new Error("a normal initial window with dialogue must not fetch older pages");
  }
  if (!ordinary.conversation.textContent.includes("VISIBLE REPLY")) {
    throw new Error("normal initial dialogue did not render");
  }

  console.log("PASS: cold hydration pages only until primary dialogue is visible");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
