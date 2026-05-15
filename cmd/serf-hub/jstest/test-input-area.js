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
       data-active-turn-id="turn_steer"
       data-replay-url=""
       data-events-url=""
       data-state="processing"></div>
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

// Track fetch calls so we can verify reset-on-success behavior. Mock /tasks
// to return [] so the cold-load hydration resolves cleanly.
let lastFetch = null;
let fetchResponseOk = true;
let fetchCallCount = 0;
window.fetch = (url, opts) => {
  fetchCallCount++;
  if (typeof url === "string" && url.includes("/tasks")) {
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve([]),
      text: () => Promise.resolve(""),
    });
  }
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
  constructor(url) {
    this.url = url;
    this.listeners = new Map();
    this.closed = false;
    MockEventSource.instances.push(this);
  }
  addEventListener(name, fn) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(fn);
    this.listeners.set(name, listeners);
  }
  set onerror(_) {}
  close() { this.closed = true; }
  emit(name, data) {
    const event = { data: JSON.stringify(data || {}) };
    for (const fn of this.listeners.get(name) || []) fn(event);
  }
}
MockEventSource.instances = [];
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

// 5b. Replying to an ended session (replayUrl-only, replay finished and
//     closed the EventSource) must reconnect to /events on send-success
//     so the new turn renders without a page reload.
async function checkReconnectsLiveAfterSendOnEndedSession() {
  // Simulate the post-replay state: no eventSource open, no eventsUrl.
  window.SerfRenderer.eventSource = null;
  window.SerfRenderer.eventsUrl = "";
  const beforeCount = MockEventSource.instances.length;
  ta.value = "resume me";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  await new Promise(r => setTimeout(r, 10));
  pass(window.SerfRenderer.eventSource !== null, "expected EventSource re-opened after send-success on ended session");
  pass(
    window.SerfRenderer.eventsUrl === "/s/01TEST/events",
    "expected eventsUrl set to /s/01TEST/events, got " + JSON.stringify(window.SerfRenderer.eventsUrl)
  );

  const liveSource = MockEventSource.instances[MockEventSource.instances.length - 1];
  pass(MockEventSource.instances.length === beforeCount + 1, "expected exactly one live EventSource after resumed send");
  pass(liveSource && liveSource.url === "/s/01TEST/events", "expected live EventSource URL /s/01TEST/events, got " + (liveSource && liveSource.url));

  liveSource.emit("USER_INPUT", { text: "resume me", turn: 4 });
  liveSource.emit("ASSISTANT_TEXT_START", { turn: 4, message_id: "msg_resume" });
  liveSource.emit("ASSISTANT_TEXT_DELTA", { message_id: "msg_resume", delta: "live resumed answer" });
  liveSource.emit("ASSISTANT_TEXT_END", { message_id: "msg_resume" });
  await new Promise(r => setTimeout(r, 10));

  pass(conv.textContent.includes("resume me"), "expected resumed user message to render without refresh");
  pass(
    !!conv.querySelector('.user-message[data-entry-idx="4"]'),
    "expected resumed user message to use authoritative turn index 4"
  );
  pass(conv.textContent.includes("live resumed answer"), "expected resumed assistant answer to render without refresh");
}

// 5c. A failed send must NOT open a live stream — connecting to a daemon
//     that just refused our send would just queue stale events.
async function checkNoReconnectOnSendFailure() {
  window.SerfRenderer.eventSource = null;
  window.SerfRenderer.eventsUrl = "";
  ta.value = "broken";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = false;
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  await new Promise(r => setTimeout(r, 10));
  pass(window.SerfRenderer.eventSource === null, "expected no EventSource opened on send failure");
}

// 6. The steer button posts to /s/<id>/steer when the textarea has text,
//    or focuses the textarea when it is empty.
async function checkSteerWired() {
  const steer = form.querySelector("[data-steer-trigger]");
  pass(steer !== null, "steer button missing");

  // Empty textarea: click should not POST; should focus.
  ta.value = "";
  let posted = false;
  const origFetch = window.fetch;
  window.fetch = (url, init) => {
    if (typeof url === "string" && url.includes("/steer")) posted = true;
    return origFetch(url, init);
  };
  steer.click();
  await new Promise(r => setTimeout(r, 5));
  pass(!posted, "expected no /steer POST when textarea is empty");

  // With text: click should POST and clear the textarea.
  ta.value = "stop using mocks";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  steer.click();
  await new Promise(r => setTimeout(r, 10));
  pass(posted, "expected /steer POST when textarea has text");
  pass(ta.value === "", "expected textarea cleared after successful steer, got " + JSON.stringify(ta.value));

  // If the active turn ends while steer is in flight, the button must stay
  // disabled after the request settles.
  window.SerfRenderer.setActiveTurnId("turn_steer");
  ta.value = "race the turn end";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  let settleSteer = null;
  window.fetch = (url, init) => {
    if (typeof url === "string" && url.includes("/steer")) {
      return new Promise(resolve => {
        settleSteer = () => resolve({
          ok: true,
          status: 202,
          json: () => Promise.resolve([]),
          text: () => Promise.resolve(""),
        });
      });
    }
    return origFetch(url, init);
  };
  steer.click();
  await new Promise(r => setTimeout(r, 5));
  window.SerfRenderer.setActiveTurnId("");
  settleSteer();
  await new Promise(r => setTimeout(r, 10));
  pass(steer.disabled, "expected steer to remain disabled after active turn cleared during request");

  window.fetch = origFetch;
}

// 7. "/" at the start of an empty textarea routes to the command palette
//    via window.SerfSearch.openWith("/"). Anywhere else "/" is literal.
async function checkSlashOpensPalette() {
  let openedWith = null;
  window.SerfSearch = { openWith: (q) => { openedWith = q; } };

  // Empty textarea + "/" → palette opens.
  ta.value = "";
  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }));
  pass(openedWith === "/", 'expected SerfSearch.openWith("/") for / on empty textarea, got ' + JSON.stringify(openedWith));

  // Non-empty textarea + "/" → palette does NOT open.
  openedWith = null;
  ta.value = "already typing";
  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }));
  pass(openedWith === null, 'expected no openWith for / mid-text, got ' + JSON.stringify(openedWith));

  // Cmd-/ or Alt-/ should be ignored too (modifier-bearing keystrokes are literal).
  openedWith = null;
  ta.value = "";
  ta.dispatchEvent(new window.KeyboardEvent("keydown", { key: "/", metaKey: true, bubbles: true, cancelable: true }));
  pass(openedWith === null, "expected no openWith for Cmd-/");

  // Restore for safety; later tests don't depend on this.
  delete window.SerfSearch;
}

// --- Attachment tests ---

// 1×1 transparent PNG (base64) — a valid image file body for tests.
const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==";
const PNG_BYTES = Buffer.from(PNG_BASE64, "base64");

function makePngFile(name) {
  return new window.File([new Uint8Array(PNG_BYTES)], name, { type: "image/png" });
}

// JSDOM's FileReader fires onload async; wait for the renderer's enqueue
// promise to settle by yielding a few microtasks/timers.
async function waitForReads(n = 8) {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 5));
  }
}

const filePicker = window.document.querySelector("[data-file-picker]");
const dropZone = window.document.querySelector("[data-drop-zone]");
const attContainer = window.document.querySelector("[data-attachments]");

// Reset queue before attachment tests run (after the earlier checkReset
// scenarios have left it empty already, but be explicit).
window.SerfRenderer.pendingAttachments = [];
window.SerfRenderer.attachmentErrors = [];
window.SerfRenderer.renderAttachments();

// Helper: simulate a file picker change with a list of files. JSDOM's
// HTMLInputElement.files is read-only via assignment in some setups, so
// stub the property explicitly.
function dispatchPickerChange(files) {
  Object.defineProperty(filePicker, "files", {
    configurable: true,
    get() { return files; },
  });
  filePicker.dispatchEvent(new window.Event("change", { bubbles: true }));
}

async function testFilePickerAddsChip() {
  const before = window.SerfRenderer.pendingAttachments.length;
  dispatchPickerChange([makePngFile("hello.png")]);
  await waitForReads();
  pass(window.SerfRenderer.pendingAttachments.length === before + 1,
    "expected 1 queued attachment, got " + window.SerfRenderer.pendingAttachments.length);
  const chips = attContainer.querySelectorAll(".attachment-chip");
  pass(chips.length === 1, "expected 1 chip, got " + chips.length);
  pass(chips[0].querySelector(".att-name").textContent.includes("hello.png"),
    "chip name missing 'hello.png': " + chips[0].textContent);
}

async function testDropAddsChips() {
  // Reset queue so we measure only this test's adds.
  window.SerfRenderer.pendingAttachments = [];
  window.SerfRenderer.renderAttachments();
  const dropEvent = new window.Event("drop", { bubbles: true, cancelable: true });
  // Stub dataTransfer with two PNG files.
  Object.defineProperty(dropEvent, "dataTransfer", {
    value: { files: [makePngFile("a.png"), makePngFile("b.png")] },
  });
  dropZone.dispatchEvent(dropEvent);
  await waitForReads();
  const chips = attContainer.querySelectorAll(".attachment-chip");
  pass(chips.length === 2, "expected 2 chips after drop, got " + chips.length);
  pass(window.SerfRenderer.pendingAttachments.length === 2,
    "expected queue length 2, got " + window.SerfRenderer.pendingAttachments.length);
}

async function testRemoveChip() {
  // Should still have 2 from the drop test; click × on the first.
  const removeBtns = attContainer.querySelectorAll(".att-remove");
  pass(removeBtns.length === 2, "expected 2 remove buttons, got " + removeBtns.length);
  removeBtns[0].click();
  pass(window.SerfRenderer.pendingAttachments.length === 1,
    "expected queue length 1 after remove, got " + window.SerfRenderer.pendingAttachments.length);
  const chips = attContainer.querySelectorAll(".attachment-chip");
  pass(chips.length === 1, "expected 1 chip after remove, got " + chips.length);
}

async function testSubmitWithTextAndImage() {
  // Reset queue, queue one PNG, set text, submit.
  window.SerfRenderer.pendingAttachments = [];
  window.SerfRenderer.renderAttachments();
  dispatchPickerChange([makePngFile("snap.png")]);
  await waitForReads();
  pass(window.SerfRenderer.pendingAttachments.length === 1, "expected 1 queued before submit");

  ta.value = "look at this";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await waitForReads();

  pass(lastFetch !== null, "expected fetch on submit");
  const body = lastFetch && lastFetch.opts && JSON.parse(lastFetch.opts.body);
  pass(body && body.text === "look at this", "expected text in body, got " + JSON.stringify(body));
  pass(body && Array.isArray(body.images) && body.images.length === 1,
    "expected 1 image in body, got " + JSON.stringify(body && body.images));
  pass(body.images[0].media_type === "image/png",
    "expected image/png media_type, got " + body.images[0].media_type);
  pass(body.images[0].data === PNG_BASE64,
    "expected base64 to match expected, got " + body.images[0].data);
  pass(body.images[0].name === "snap.png",
    "expected name 'snap.png', got " + body.images[0].name);
}

async function testSubmitClearsQueue() {
  // Continuation of testSubmitWithTextAndImage's success path.
  pass(window.SerfRenderer.pendingAttachments.length === 0,
    "expected queue cleared after success, got " + window.SerfRenderer.pendingAttachments.length);
  const chips = attContainer.querySelectorAll(".attachment-chip");
  pass(chips.length === 0, "expected no chips after success, got " + chips.length);
}

async function testSubmitEmptyDoesNothing() {
  ta.value = "";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  window.SerfRenderer.pendingAttachments = [];
  window.SerfRenderer.renderAttachments();
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await waitForReads();
  pass(lastFetch === null, "expected no fetch on empty submit, got " + JSON.stringify(lastFetch));
}

async function testUnavailableSendDoesNotSubmit() {
  const send = form.querySelector(".send-btn");
  send.setAttribute("data-capability-send", "false");
  send.disabled = true;
  ta.value = "blocked";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await waitForReads();
  pass(lastFetch === null, "expected no fetch when send capability is unavailable");
  pass(send.disabled === true, "expected send button to remain disabled");
  send.setAttribute("data-capability-send", "true");
  send.disabled = false;
}

async function testRejectsOversize() {
  window.SerfRenderer.pendingAttachments = [];
  window.SerfRenderer.attachmentErrors = [];
  window.SerfRenderer.renderAttachments();
  // Make a "file" whose .size reports 9MB without holding 9MB of bytes.
  const tiny = new window.File([new Uint8Array(8)], "huge.png", { type: "image/png" });
  Object.defineProperty(tiny, "size", { value: 9 * 1024 * 1024 });
  dispatchPickerChange([tiny]);
  await waitForReads();
  pass(window.SerfRenderer.pendingAttachments.length === 0,
    "expected oversize rejected from queue");
  const errs = attContainer.querySelectorAll(".attachment-chip.error");
  pass(errs.length === 1, "expected 1 error chip for oversize, got " + errs.length);
}

async function testRejectsNonImage() {
  window.SerfRenderer.pendingAttachments = [];
  window.SerfRenderer.attachmentErrors = [];
  window.SerfRenderer.renderAttachments();
  const txt = new window.File([new Uint8Array([1, 2, 3])], "notes.txt", { type: "text/plain" });
  dispatchPickerChange([txt]);
  await waitForReads();
  pass(window.SerfRenderer.pendingAttachments.length === 0,
    "expected non-image rejected from queue");
  const errs = attContainer.querySelectorAll(".attachment-chip.error");
  pass(errs.length === 1, "expected 1 error chip for non-image, got " + errs.length);
  pass(errs[0].textContent.includes("notes.txt"),
    "expected error chip to mention filename, got " + errs[0].textContent);
}

(async () => {
  await checkReset();
  await checkFailureKeepsValue();
  await checkReconnectsLiveAfterSendOnEndedSession();
  await checkNoReconnectOnSendFailure();
  await checkSteerWired();
  await checkSlashOpensPalette();

  await testFilePickerAddsChip();
  await testDropAddsChips();
  await testRemoveChip();
  await testSubmitWithTextAndImage();
  await testSubmitClearsQueue();
  await testSubmitEmptyDoesNothing();
  await testUnavailableSendDoesNotSubmit();
  await testRejectsOversize();
  await testRejectsNonImage();

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
