// test-queue-and-drain: verifies the composer routes Enter to turn/queue
// while the session is processing (kata 111a), and that the steer button
// (and ⇧↵ shortcut) drains the queue as a single STEERING injection
// (kata 0bq1). Exercises the renderer's wire-sourced queueState (kata
// r80p) and the queue-preview rendering driven by thread/queueChanged
// notifications.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = path.resolve(__dirname, "../assets/renderer.js");
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

// Composer template mirrors templates/partials/workspace.html: queue is
// advertised as available (data-capability-queue=true) while send is OFF
// (data-capability-send=false) — the processing-turn state shape.
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-active-turn-id="turn_live"
       data-state="processing"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="input-attachments" data-attachments></div>
    <div class="queue-preview" data-queue-preview hidden>
      <div class="queue-preview-header">
        <span class="queue-preview-label">queued <span data-queue-depth>0</span></span>
      </div>
      <ul class="queue-preview-list" data-queue-list></ul>
    </div>
    <div class="input-card" data-drop-zone>
      <textarea class="message-input" rows="1"></textarea>
    </div>
    <div class="input-controls">
      <button type="button" class="input-btn" data-attach-trigger>＋</button>
      <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>send as steer</button>
      <button type="submit" class="send-btn input-btn input-btn-primary"
              data-capability-send="false"
              data-capability-queue="true"
              disabled>send</button>
    </div>
    <div class="input-status" id="input-status"></div>
    <input type="file" data-file-picker hidden>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };

let lastFetch = null;
let fetchResponseOk = true;
const fetchLog = [];
window.fetch = (url, opts) => {
  // /tasks responses must be JSON arrays so the cold-load hydration resolves.
  if (typeof url === "string" && url.includes("/tasks")) {
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve([]),
      text: () => Promise.resolve(""),
    });
  }
  lastFetch = { url, opts };
  fetchLog.push({ url, opts });
  return Promise.resolve({
    ok: fetchResponseOk,
    status: fetchResponseOk ? 204 : 500,
    json: () => Promise.resolve({}),
    text: () => Promise.resolve(fetchResponseOk ? "" : "boom"),
  });
};

Object.defineProperty(window.HTMLTextAreaElement.prototype, "scrollHeight", {
  configurable: true,
  get() { return 36 + Math.floor((this.value || "").length / 50) * 20; },
});
Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });

window.eval(rendererSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const ta = window.document.querySelector(".message-input");
const form = window.document.querySelector("form[data-input-form]");
const steerBtn = window.document.querySelector("[data-steer-trigger]");
const preview = window.document.querySelector("[data-queue-preview]");
const depthEl = preview.querySelector("[data-queue-depth]");
const list = preview.querySelector("[data-queue-list]");

const wait = (ms) => new Promise(r => setTimeout(r, ms));

async function testQueuePreviewStartsHidden() {
  pass(preview.hidden === true, "expected queue-preview hidden on init");
  pass(depthEl.textContent === "0", "expected depth=0 on init, got " + depthEl.textContent);
  pass(list.children.length === 0, "expected empty list on init");
}

async function testEnterQueuesWhenProcessing() {
  // Send capability is off + queue is on → submit posts to /queue.
  ta.value = "investigate the failing test";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  lastFetch = null;
  fetchResponseOk = true;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await wait(15);

  pass(lastFetch !== null, "expected fetch on queue submit");
  pass(lastFetch && lastFetch.url.includes("/s/01TEST/queue"),
    "fetch url should hit /queue, got " + (lastFetch && lastFetch.url));
  const body = lastFetch && JSON.parse(lastFetch.opts.body);
  pass(body && body.text === "investigate the failing test",
    "expected queue body.text, got " + JSON.stringify(body));
  pass(ta.value === "", "expected textarea cleared after queue, got " + JSON.stringify(ta.value));

  // The renderer no longer mirrors locally (kata r80p). Wire the
  // authoritative state in by simulating the daemon's thread/queueChanged
  // notification so the preview re-renders.
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: 1, preview: ["investigate the failing test"] }),
  });
  pass(window.SerfRenderer.queueState.depth === 1,
    "expected queueState.depth=1, got " + window.SerfRenderer.queueState.depth);
  pass(preview.hidden === false, "expected preview visible after first queue");
  pass(depthEl.textContent === "1", "expected depth=1, got " + depthEl.textContent);
  pass(list.children.length === 1, "expected 1 li, got " + list.children.length);
  pass(list.children[0].textContent.includes("investigate the failing test"),
    "expected queued text in preview, got " + list.children[0].textContent);
}

async function testQueueTwiceShowsBothEntries() {
  ta.value = "and then verify the regression";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await wait(15);

  // Drive a second QUEUE_CHANGED with both entries.
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: 2, preview: ["investigate the failing test", "and then verify the regression"] }),
  });
  pass(window.SerfRenderer.queueState.depth === 2,
    "expected queueState.depth=2 after second queue, got " + window.SerfRenderer.queueState.depth);
  pass(depthEl.textContent === "2", "expected depth=2, got " + depthEl.textContent);
  pass(list.children.length === 2, "expected 2 li, got " + list.children.length);
}

async function testQueueChangedShrinksOnHeadPop() {
  // Simulate the daemon popping the queue head into a fresh user turn —
  // it emits thread/queueChanged with depth=1 and the remaining entry.
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: 1, preview: ["and then verify the regression"] }),
  });
  pass(window.SerfRenderer.queueState.depth === 1,
    "expected queueState.depth=1 after head pop, got " + window.SerfRenderer.queueState.depth);
  pass(list.children.length === 1, "expected 1 li after pop, got " + list.children.length);
  pass(list.children[0].textContent.includes("and then verify the regression"),
    "expected remaining entry to be the second message");
}

async function testDrainAsSteerClearsQueueAndPosts() {
  // Type something while queue still has one entry → drain should carry the
  // textarea text on /drain-as-steer.
  ta.value = "and one more thought before drain";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchLog.length = 0;
  fetchResponseOk = true;
  steerBtn.click();
  await wait(20);

  const calls = fetchLog.map(c => c.url);
  pass(calls.length === 1 && calls[0].includes("/drain-as-steer"),
    "expected one /drain-as-steer call from steer button, got " + JSON.stringify(calls));
  const body = JSON.parse((fetchLog[0] && fetchLog[0].opts && fetchLog[0].opts.body) || "{}");
  pass(body.text === "and one more thought before drain",
    "expected drain body to carry textarea text, got " + JSON.stringify(body));
  // After drain the daemon emits thread/queueChanged with depth=0. The
  // renderer no longer mirrors locally (kata r80p), so we deliver the
  // wire event here to verify the preview hides.
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: 0, preview: [] }),
  });
  pass(window.SerfRenderer.queueState.depth === 0,
    "expected queueState.depth=0 after drain, got " + window.SerfRenderer.queueState.depth);
  pass(preview.hidden === true, "expected preview hidden after drain");
  pass(ta.value === "", "expected textarea cleared after drain, got " + JSON.stringify(ta.value));
}

async function testEmptyQueueButTextActsAsClassicSteer() {
  // Queue is empty; textarea has text → button posts to /steer (NOT /drain).
  ta.value = "behave like the old steer";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchLog.length = 0;
  steerBtn.click();
  await wait(15);

  const calls = fetchLog.map(c => c.url);
  pass(calls.some(u => u.includes("/steer") && !u.includes("drain")),
    "expected /steer call when queue empty, got " + JSON.stringify(calls));
  pass(!calls.some(u => u.includes("drain-as-steer")),
    "expected NO /drain-as-steer call when queue empty, got " + JSON.stringify(calls));
  pass(ta.value === "", "expected textarea cleared after classic steer");
}

async function testEmptyQueueAndEmptyTextIsNoop() {
  ta.value = "";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchLog.length = 0;
  steerBtn.click();
  await wait(10);
  pass(fetchLog.length === 0,
    "expected no fetch when steer pressed with empty queue + empty textarea, got " + JSON.stringify(fetchLog.map(c => c.url)));
}

async function testShiftEnterTriggersDrain() {
  // Re-prime: one queued entry via the daemon's authoritative wire event.
  ta.value = "queued via Enter";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchLog.length = 0;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await wait(10);
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: 1, preview: ["queued via Enter"] }),
  });
  pass(window.SerfRenderer.queueState.depth >= 1, "primed queue empty");

  fetchLog.length = 0;
  ta.dispatchEvent(new window.KeyboardEvent("keydown", {
    key: "Enter", shiftKey: true, bubbles: true, cancelable: true,
  }));
  await wait(15);
  const calls = fetchLog.map(c => c.url);
  pass(calls.some(u => u.includes("/drain-as-steer")),
    "expected ⇧↵ to drain, calls=" + JSON.stringify(calls));
}

async function testQueueFailureSurfaceErrorBanner() {
  // Wipe the queue first so we start clean.
  window.SerfRenderer.queueState = { depth: 0, preview: [] };
  window.SerfRenderer.renderQueuePreview();
  ta.value = "will fail";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = false;
  fetchLog.length = 0;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await wait(15);
  // Failure path: queueState should remain depth=0 (daemon never emitted
  // a thread/queueChanged because the POST itself failed).
  pass(window.SerfRenderer.queueState.depth === 0,
    "expected queue state unchanged on queue failure, got " + window.SerfRenderer.queueState.depth);
  const banners = window.document.querySelectorAll(".banner");
  const last = banners[banners.length - 1];
  pass(last && /queue failed/i.test(last.textContent),
    "expected an error banner mentioning queue failed; last banner: " + (last && last.textContent));
  fetchResponseOk = true;
}

(async () => {
  await testQueuePreviewStartsHidden();
  await testEnterQueuesWhenProcessing();
  await testQueueTwiceShowsBothEntries();
  await testQueueChangedShrinksOnHeadPop();
  await testDrainAsSteerClearsQueueAndPosts();
  await testEmptyQueueButTextActsAsClassicSteer();
  await testEmptyQueueAndEmptyTextIsNoop();
  await testShiftEnterTriggersDrain();
  await testQueueFailureSurfaceErrorBanner();

  if (failures.length === 0) {
    console.log("PASS: queue + drain-as-steer composer behaviors");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
