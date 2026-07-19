// test-queue-promote: verifies each queue-preview row offers a per-message
// promote-to-steering action (issue #22). Clicking it POSTs the row index to
// /s/<id>/promote-queued (REST fallback; the appwire path is covered by
// Go relay tests), the daemon's thread/queueChanged re-renders the preview
// without the promoted row, and a failed promote surfaces an error banner
// without touching local queue state.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-active-turn-id="turn_live"
       data-state="active"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="composer-attachments" data-composer-attachments data-attachments></div>
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
      <div class="controls-right">
        <button type="button" class="btn btn-ghost" data-steer-trigger>steer</button>
        <button type="submit" class="send-btn btn btn-primary"
                data-capability-send="false"
                data-capability-queue="true">send</button>
      </div>
    </div>
    <div class="input-status" id="input-status"></div>
    <input type="file" data-file-picker hidden>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };

let fetchResponseOk = true;
const fetchLog = [];
window.fetch = (url, opts) => {
  if (typeof url === "string" && url.includes("/tasks")) {
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve([]),
      text: () => Promise.resolve(""),
    });
  }
  fetchLog.push({ url, opts });
  return Promise.resolve({
    ok: fetchResponseOk,
    status: fetchResponseOk ? 204 : 409,
    json: () => Promise.resolve({}),
    text: () => Promise.resolve(fetchResponseOk ? "" : "no active turn to steer"),
  });
};

Object.defineProperty(window.HTMLTextAreaElement.prototype, "scrollHeight", {
  configurable: true,
  get() { return 36; },
});
Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });

require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const list = window.document.querySelector("[data-queue-list]");
const wait = (ms) => new Promise(r => setTimeout(r, ms));

function primeQueue(preview) {
  window.SerfRenderer.handle("QUEUE_CHANGED", {
    data: JSON.stringify({ depth: preview.length, preview }),
  });
}

async function testRowsHavePromoteButtons() {
  primeQueue(["investigate the failing test", "and then verify the regression"]);
  pass(list.children.length === 2, "expected 2 rows, got " + list.children.length);
  for (let i = 0; i < 2; i++) {
    const btn = list.children[i].querySelector("[data-promote-index]");
    pass(btn !== null, "row " + i + " should have a promote button");
    pass(btn && btn.getAttribute("data-promote-index") === String(i),
      "row " + i + " promote button should carry index " + i + ", got " + (btn && btn.getAttribute("data-promote-index")));
  }
}

async function testPromotePostsIndex() {
  fetchLog.length = 0;
  fetchResponseOk = true;
  const btn = list.children[1].querySelector("[data-promote-index]");
  btn.click();
  await wait(20);

  const calls = fetchLog.map(c => c.url);
  pass(calls.length === 1 && calls[0].includes("/s/01TEST/promote-queued"),
    "expected one /promote-queued call, got " + JSON.stringify(calls));
  const body = JSON.parse((fetchLog[0] && fetchLog[0].opts && fetchLog[0].opts.body) || "{}");
  pass(body.index === 1, "expected body.index=1, got " + JSON.stringify(body));

  // No local mirror: the row stays until the daemon's authoritative
  // thread/queueChanged lands with the promoted entry removed.
  primeQueue(["investigate the failing test"]);
  pass(list.children.length === 1, "expected 1 row after daemon queueChanged, got " + list.children.length);
  pass(list.children[0].textContent.includes("investigate the failing test"),
    "expected remaining row to be the first message, got " + list.children[0].textContent);
  pass(list.children[0].querySelector("[data-promote-index]").getAttribute("data-promote-index") === "0",
    "re-rendered row should re-key its promote index to 0");
}

async function testPromoteFailureSurfacesBanner() {
  primeQueue(["still queued"]);
  fetchLog.length = 0;
  fetchResponseOk = false;
  const bannersBefore = window.document.querySelectorAll(".banner").length;
  const btn = list.children[0].querySelector("[data-promote-index]");
  btn.click();
  await wait(20);

  pass(fetchLog.length === 1, "expected one promote attempt, got " + fetchLog.length);
  pass(window.SerfRenderer.queueState.depth === 1,
    "queue state must be unchanged on promote failure, got depth " + window.SerfRenderer.queueState.depth);
  pass(list.children.length === 1, "row must stay queued on promote failure");
  const bannersAfter = window.document.querySelectorAll(".banner");
  pass(bannersAfter.length > bannersBefore,
    "expected a new error banner after promote failure; before=" + bannersBefore + " after=" + bannersAfter.length);
  const newBanners = Array.from(bannersAfter).slice(bannersBefore);
  pass(newBanners.some(b => /promote failed/i.test(b.textContent)),
    "expected new banner mentioning 'promote failed'; new banners: " + newBanners.map(b => b.textContent).join(", "));
  fetchResponseOk = true;
}

(async () => {
  await testRowsHavePromoteButtons();
  await testPromotePostsIndex();
  await testPromoteFailureSurfacesBanner();

  if (failures.length === 0) {
    console.log("PASS: queue-preview per-message promote to steering");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
