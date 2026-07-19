// During hydration replay, per-event stick/scroll work is suppressed; one
// scroll settle runs when hydration completes.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((r, j) => { resolve = r; reject = j; });
  return { promise, resolve, reject };
};
const drainMicrotasks = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  pass(R.suppressScrollSettle === false || R.suppressScrollSettle === undefined,
    "not suppressing outside hydration");
  let measures = 0;
  const realIsNearBottom = R.isNearBottom.bind(R);
  R.isNearBottom = () => { measures++; return realIsNearBottom(); };
  R.suppressScrollSettle = true;
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "hi" });
  pass(measures === 0, "no stick measurement while suppressed (got " + measures + ")");
  R.suppressScrollSettle = false;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "!" });
  pass(measures === 1, "measurement resumes after suppression");
  // The new-content pill must not tick during a suppressed (hydration) settle
  // either — the final scrollToBottom after replay clears it anyway.
  let noted = 0;
  const realNote = R.noteNewContent.bind(R);
  R.noteNewContent = (n) => { noted++; return realNote(n); };
  R.suppressScrollSettle = true;
  R.handleData("USER_INPUT", { text: "replayed history" });
  R.suppressScrollSettle = false;
  pass(noted === 0, "noteNewContent skipped while suppressScrollSettle (got " + noted + ")");
  R.handleData("USER_INPUT", { text: "live input" });
  pass(noted === 1, "noteNewContent resumes after suppression (got " + noted + ")");
  R.noteNewContent = realNote;

  // ── Hydration drive (appwire stubs mirror test-renderer-hydration-order.js) ──
  // A multi-event readThread replay plus one buffered live notification must
  // run with suppressScrollSettle set, measure nothing per event, and scroll
  // exactly once when hydration completes.
  let notify = () => {};
  const readThreadCall = deferred();
  window.SerfAppwire = {
    tasks: () => new Promise(() => {}),
    refForSession: (sessionId) => "local:" + sessionId,
    activeTurnIDFromThread: () => "turn_active",
    onNotification: (handler) => {
      notify = handler;
      return () => {};
    },
    onConnectionLost: () => () => {},
    readThread: () => readThreadCall.promise,
    eventsFromThread: (thread) => thread.testEvents,
    eventsFromNotification: (method, params) => {
      if (method !== "item/completed") return [];
      return [["USER_INPUT", { text: params.item.text }]];
    },
  };
  const hydConv = window.document.createElement("div");
  hydConv.dataset.sessionId = "hyd-session";
  hydConv.dataset.state = "active";
  conv.replaceWith(hydConv);
  R.init(hydConv);

  const realHandleData = R.handleData.bind(R);
  const flagsSeen = [];
  R.handleData = (kind, data) => {
    flagsSeen.push([kind, R.suppressScrollSettle]);
    return realHandleData(kind, data);
  };
  let scrolls = 0;
  const realScrollToBottom = R.scrollToBottom.bind(R);
  R.scrollToBottom = () => { scrolls++; return realScrollToBottom(); };
  measures = 0;

  // A live notification arriving before hydration completes is buffered and
  // replayed inside the same suppressed window as the thread events.
  notify("item/completed", {
    ref: "local:hyd-session",
    threadId: "hyd-session",
    turnId: "turn_live",
    item: { id: "user_live", type: "userMessage", text: "buffered live" },
  });
  readThreadCall.resolve({
    thread: {
      id: "hyd-session",
      sessionId: "hyd-session",
      serf: { ref: "local:hyd-session" },
      turns: [{
        id: "turn_1",
        status: "completed",
        items: [{ id: "u1", type: "userMessage", text: "snap" }],
      }],
      testEvents: [
        ["USER_INPUT", { text: "snap" }],
        ["ASSISTANT_TEXT_START", {}],
        ["ASSISTANT_TEXT_DELTA", { delta: "a" }],
        ["ASSISTANT_TEXT_DELTA", { delta: "b" }],
        ["ASSISTANT_TEXT_END", {}],
      ],
    },
  });
  await drainMicrotasks();

  pass(flagsSeen.length === 6,
    "hydration replayed 5 thread events + 1 buffered notification (got " + flagsSeen.length + ")");
  pass(flagsSeen.every(([, flag]) => flag === true),
    "every replayed event ran under suppressScrollSettle: " + JSON.stringify(flagsSeen));
  pass(measures === 0,
    "no stick measurement during hydration replay (got " + measures + ")");
  pass(scrolls === 1,
    "exactly one scroll settle after hydration (got " + scrolls + ")");
  pass(R.suppressScrollSettle === false,
    "suppression cleared after hydration");
  pass(hydConv.textContent.includes("snap") && hydConv.textContent.includes("buffered live"),
    "hydrated and buffered transcript content rendered");

  // ── Failed hydration must clear the flag (no wedged scrolling) ──
  const failRead = deferred();
  window.SerfAppwire.readThread = () => failRead.promise;
  window.SerfAppwire.eventsFromThread = () => [
    ["USER_INPUT", { text: "fail snapshot" }],
    ["BOOM", {}],
  ];
  R.handleData = (kind, data) => {
    if (kind === "BOOM") throw new Error("mid-replay failure");
    return realHandleData(kind, data);
  };
  const failConv = window.document.createElement("div");
  failConv.dataset.sessionId = "fail-session";
  failConv.dataset.state = "active";
  hydConv.replaceWith(failConv);
  R.init(failConv);
  failRead.resolve({
    thread: {
      id: "fail-session",
      sessionId: "fail-session",
      serf: { ref: "local:fail-session" },
      turns: [],
    },
  });
  await drainMicrotasks();
  pass(R.suppressScrollSettle === false,
    "failed hydration cleared suppression (no wedged scrolling)");
  // Neutralize the scheduled reconnect so it can't replay the failing fixture.
  window.SerfAppwire.readThread = () => new Promise(() => {});

  // ── resetTranscriptReplay must clear the flag too ──
  R.handleData = realHandleData;
  R.suppressScrollSettle = true;
  R.resetTranscriptReplay();
  pass(R.suppressScrollSettle === false,
    "resetTranscriptReplay cleared suppression");

  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: hydration replay suppresses per-event scroll settle and settles once");
  process.exit(0);
})().catch((err) => {
  console.error(err.stack || err);
  process.exit(1);
});
