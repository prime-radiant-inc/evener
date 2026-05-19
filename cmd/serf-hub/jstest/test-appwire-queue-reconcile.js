const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const pendingSrc = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");
const rendererSrc = fs.readFileSync(path.resolve(__dirname, "../assets/renderer.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="processing"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="queue-preview" data-queue-preview hidden>
      <span data-queue-depth>0</span>
      <ul data-queue-list></ul>
    </div>
    <textarea class="message-input"></textarea>
    <button type="button" data-steer-trigger></button>
    <button type="submit" class="send-btn" data-capability-send="false" data-capability-queue="true"></button>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

let notify = null;
let pending = null;
window.SerfAppwire = {
  refForSession: (id) => "local:" + id,
  setPendingRegistry: (reg) => { pending = reg; },
  onNotification: (cb) => { notify = cb; return () => {}; },
  onConnectionLost: () => () => {},
  readThread: () => Promise.resolve({
    thread: {
      id: "01TEST",
      sessionId: "01TEST",
      serf: { ref: "local:01TEST" },
      status: { type: "processing" },
      queue: { depth: 0, preview: [] },
      turns: [],
    },
  }),
  eventsFromThread: () => [],
  eventsFromNotification: () => [],
  activeTurnIDFromThread: () => "",
  tasks: () => Promise.resolve([]),
};

window.eval(pendingSrc);
window.eval(rendererSrc);

(async () => {
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  await new Promise((resolve) => setTimeout(resolve, 10));

  assert.ok(pending, "renderer did not install pending registry");
  assert.ok(notify, "renderer did not subscribe to AppWire notifications");

  const queueList = window.document.querySelector("[data-queue-list]");
  pending.register({ method: "turn/queue", text: "queued as string" });
  const pendingQueueList = window.document.querySelector("[data-queue-pending-list]");
  assert.equal(pendingQueueList.querySelectorAll(".optimistic-pending").length, 1, "expected pending queue chip");

  notify("thread/queueChanged", {
    threadId: "01TEST",
    queue: { depth: 1, preview: ["queued as string"] },
  });

  assert.equal(pendingQueueList.querySelectorAll(".optimistic-pending").length, 0,
    "string queue previews should reconcile pending queue chips");

  pending.register({ method: "turn/start", text: "", items: [{ type: "image", name: "shot.png" }] });
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 1, "expected image-only pending user chip");
  notify("item/completed", {
    threadId: "01TEST",
    item: { type: "user_message", text: "", images: [{ type: "image", name: "shot.png" }] },
  });
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0,
    "image-only authoritative user items should reconcile pending turn/start chips");

  console.log("PASS test-appwire-queue-reconcile.js");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
