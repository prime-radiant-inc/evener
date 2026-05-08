// Test harness: load renderer.js into a JSDOM window and exercise the
// auto-grow + reset-on-send behavior on the workspace input textarea.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = path.resolve(__dirname, "../assets/renderer.js");
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

// Build a tiny app shell that the renderer expects, including the new
// bottom-strip structure (input-card, input-controls, input-status).
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
       data-state="ended"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="input-attachments" data-attachments></div>
    <div class="input-card" data-drop-zone>
      <textarea class="message-input" rows="1"></textarea>
    </div>
    <div class="input-controls">
      <button type="button" class="input-btn" data-attach-trigger>＋</button>
      <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>steer</button>
      <button type="submit" class="send-btn input-btn input-btn-primary">send</button>
    </div>
    <div class="input-status" id="input-status"></div>
    <input type="file" data-file-picker hidden>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };

// Track fetch calls so we can verify reset-on-success behavior.
let lastFetch = null;
let fetchResponseOk = true;
window.fetch = (url, opts) => {
  lastFetch = { url, opts };
  return Promise.resolve({
    ok: fetchResponseOk,
    status: fetchResponseOk ? 202 : 500,
    json: () => Promise.resolve([]),
    text: () => Promise.resolve(fetchResponseOk ? "" : "boom"),
  });
};

// Stub EventSource so init() doesn't try to open one (no events url anyway).
class MockEventSource {
  constructor() { this.listeners = new Map(); }
  addEventListener() {}
  set onerror(_) {}
  close() {}
}
window.EventSource = MockEventSource;

// JSDOM doesn't lay out text, so scrollHeight is always 0. Override it on
// HTMLTextAreaElement so the auto-grow math has something to work with —
// scrollHeight grows by ~20px per 80 chars of content.
Object.defineProperty(window.HTMLTextAreaElement.prototype, "scrollHeight", {
  configurable: true,
  get() {
    const len = (this.value || "").length;
    // Pretend each ~50 chars adds a line of ~20px, on top of a 36px base.
    return 36 + Math.floor(len / 50) * 20;
  },
});

// Pin window.innerHeight to something predictable for the clamp test.
Object.defineProperty(window, "innerHeight", {
  configurable: true,
  value: 800,
});

window.eval(rendererSrc);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const ta = window.document.querySelector(".message-input");
const form = window.document.querySelector("form[data-input-form]");

// Helper to read the inline height back as a number.
const heightPx = () => parseFloat(ta.style.height) || 0;

// 1. Initial grow() runs in bindInputForm; height is set from scrollHeight.
//    With empty value, scrollHeight is 36, so style.height === "36px".
pass(heightPx() === 36, "expected initial height 36px, got " + ta.style.height);

// 2. Typing a short message triggers grow on input → height tracks scrollHeight.
ta.value = "x".repeat(120); // → scrollHeight = 36 + 2*20 = 76
ta.dispatchEvent(new window.Event("input", { bubbles: true }));
pass(heightPx() === 76, "expected height 76px after short input, got " + ta.style.height);

// 3. A very long message clamps at 50% of viewport (innerHeight=800 → 400).
ta.value = "x".repeat(10000); // scrollHeight would be enormous
ta.dispatchEvent(new window.Event("input", { bubbles: true }));
pass(heightPx() === 400, "expected clamp at 400px (0.5 * 800), got " + ta.style.height);

// 4. Successful submit clears the value AND resets height back to baseline.
async function checkReset() {
  ta.value = "ship it";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  pass(heightPx() > 0, "expected non-zero height before submit");

  fetchResponseOk = true;
  // requestSubmit isn't implemented in JSDOM the same way; dispatch submit instead.
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  // Allow the fetch promise + finally to settle.
  await new Promise(r => setTimeout(r, 10));

  pass(lastFetch !== null, "expected fetch to be called on submit");
  pass(lastFetch && lastFetch.url.includes("/s/01TEST/send"), "fetch url wrong: " + (lastFetch && lastFetch.url));
  pass(ta.value === "", "expected textarea cleared after success, got " + JSON.stringify(ta.value));
  // After a reset the textarea is empty, so grow() sets height back to 36px.
  pass(heightPx() === 36, "expected height reset to 36px after success, got " + ta.style.height);
}

// 5. A failed submit must NOT clear the value or reset height.
async function checkFailureKeepsValue() {
  ta.value = "won't go";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = false;
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  await new Promise(r => setTimeout(r, 10));
  pass(ta.value === "won't go", "expected textarea preserved on failure, got " + JSON.stringify(ta.value));
}

// 6. The steer button is wired to a no-op console.warn (Step 4 territory).
function checkSteerNoop() {
  const steer = form.querySelector("[data-steer-trigger]");
  pass(steer !== null, "steer button missing");
  let warned = false;
  const origWarn = console.warn;
  console.warn = (msg) => { if (typeof msg === "string" && msg.includes("steer")) warned = true; };
  steer.click();
  console.warn = origWarn;
  pass(warned, "expected console.warn from steer click");
}

(async () => {
  await checkReset();
  await checkFailureKeepsValue();
  checkSteerNoop();

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
