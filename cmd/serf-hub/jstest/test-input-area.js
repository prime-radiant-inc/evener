// Test harness: load renderer.js into a JSDOM window and exercise the
// auto-grow + reset-on-send behavior on the workspace input textarea.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = path.resolve(__dirname, "../assets/renderer.js");
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");
const COMPOSER_PATH = path.resolve(__dirname, "../assets/composer-attachments.js");
const composerSrc = fs.readFileSync(COMPOSER_PATH, "utf8");

// Build a tiny app shell that the renderer expects, including the new
// bottom-strip structure (input-card, input-controls, input-status). The
// attachment containers mirror templates/partials/workspace.html: the new
// composer-attachments chip container lives next to the (now unused) legacy
// data-attachments div, and a sibling [data-attachment-error] absorbs
// non-image rejection banners.
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-active-turn-id="turn_steer"
       data-state="active"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="input-attachments" data-attachments></div>
    <div class="composer-attachments" data-composer-attachments></div>
    <div class="composer-attachment-error" data-attachment-error hidden></div>
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

// composer-attachments.js round-trips through <canvas>.toBlob / <img>.onload
// (kata r6a1) to re-encode arbitrary image formats to PNG. JSDOM has neither;
// stub them to hand back our deterministic PNG fixture used below in the
// attachment tests.
const PASTE_PNG_BYTES = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==",
  "base64",
);
window.HTMLCanvasElement.prototype.toBlob = function (cb, mimeType) {
  cb(new window.Blob([new Uint8Array(PASTE_PNG_BYTES)], { type: mimeType || "image/png" }));
};
window.HTMLCanvasElement.prototype.getContext = function () { return { drawImage() {} }; };
class FakeImage {
  constructor() { this._src = ""; this.width = 8; this.height = 4; this.onload = null; this.onerror = null; }
  set src(v) { this._src = v; Promise.resolve().then(() => { if (this.onload) this.onload(); }); }
  get src() { return this._src; }
}
window.Image = FakeImage;
window.URL.createObjectURL = () => "blob:fake";
window.URL.revokeObjectURL = () => {};

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

// composer-attachments.js must register SerfComposerAttachments before the
// renderer's bindInputForm wires it in.
window.eval(composerSrc);
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

async function checkProcessingSendCapabilityKeepsSendMode() {
  await waitForReads();
  const send = form.querySelector(".send-btn");
  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "active",
    capabilities: { send: true, queue: false },
  });
  pass(send.getAttribute("data-capability-send") === "true", "active send capability should stay true");
  pass(send.getAttribute("data-capability-queue") === "false", "active queue capability should stay false");
  pass(send.disabled === false, "send-capable active composer should stay enabled");

  ta.value = "active follow-up";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 10));
  pass(lastFetch !== null, "expected fetch for send-capable active submit");
  pass(lastFetch && lastFetch.url.includes("/s/01TEST/send"), "send-capable active submit should use /send, got " + (lastFetch && lastFetch.url));

  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "idle",
    capabilities: { send: true, queue: false },
  });
  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  pass(send.getAttribute("data-capability-send") === "false", "active status should not preserve stale idle send capability");
  pass(send.getAttribute("data-capability-queue") === "false", "active status should not invent queue support when source queue=false is cached");
  pass(send.disabled === true, "active status with cached queue=false should disable composer until fresh caps arrive");

  window.SerfAppwire = {
    readThread: () => Promise.resolve({
      thread: {
        status: { type: "active" },
        serf: { capabilities: { send: false, queue: true } },
      },
    }),
  };
  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "idle",
    capabilities: { send: true, queue: false },
  });
  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  pass(send.getAttribute("data-capability-send") === "false", "active status should disable stale send baseline before refresh resolves");
  pass(send.getAttribute("data-capability-queue") === "false", "active status should keep cached queue=false before refresh resolves");
  await new Promise(r => setTimeout(r, 10));
  pass(send.getAttribute("data-capability-send") === "false", "fresh active send capability should replace idle send capability");
  pass(send.getAttribute("data-capability-queue") === "true", "fresh active queue capability should replace idle queue capability");

  let resolveRead;
  window.SerfAppwire = {
    readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
  };
  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "idle",
    capabilities: { send: true, queue: false },
  });
  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "idle" });
  resolveRead({
    thread: {
      status: { type: "active" },
      serf: { capabilities: { send: false, queue: true } },
    },
  });
  await new Promise(r => setTimeout(r, 10));
  pass(send.getAttribute("data-capability-send") === "true", "stale active refresh should not overwrite newer idle send state");
  pass(send.getAttribute("data-capability-queue") === "false", "stale active refresh should not overwrite newer idle queue state");

  window.SerfAppwire = {
    readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
  };
  window.SerfRenderer.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "idle",
    capabilities: { send: true, queue: false },
  });
  resolveRead({
    thread: {
      status: { type: "active" },
      serf: { capabilities: { send: false, queue: true } },
    },
  });
  await new Promise(r => setTimeout(r, 10));
  pass(send.getAttribute("data-capability-send") === "true", "same-session hydration should invalidate stale refresh send state");
  pass(send.getAttribute("data-capability-queue") === "false", "same-session hydration should invalidate stale refresh queue state");
}

// 5b. Replying to an ended session with no open stream must reconnect
//     AppWire on send-success so the new turn renders without a page reload.
async function checkReconnectsLiveAfterSendOnEndedSession() {
  let notificationHandler = null;
  let subscriptions = 0;
  window.SerfAppwire = {
    refForSession: (sessionId) => "local:" + sessionId,
    onNotification: (handler) => {
      notificationHandler = handler;
      subscriptions++;
      return () => {};
    },
    onConnectionLost: () => () => {},
    readThread: () => Promise.resolve({
      thread: {
        id: "01TEST",
        sessionId: "01TEST",
        serf: { ref: "local:01TEST" },
        turns: [],
      },
    }),
    eventsFromThread: () => [],
    eventsFromNotification: (method, params) => {
      if (method === "item/completed" && params.item && params.item.type === "userMessage") {
        return [["USER_INPUT", { text: params.item.text || "", turn: 4 }]];
      }
      if (method === "item/started" && params.item && params.item.type === "agentMessage") {
        return [["ASSISTANT_TEXT_START", { message_id: params.item.id }]];
      }
      if (method === "item/agentMessage/delta") {
        return [["ASSISTANT_TEXT_DELTA", { message_id: params.itemId, delta: params.delta || "" }]];
      }
      if (method === "item/completed" && params.item && params.item.type === "agentMessage") {
        return [["ASSISTANT_TEXT_END", { message_id: params.item.id }]];
      }
      return [];
    },
    startTurn: () => Promise.resolve({ turn: { id: "turn_resume" } }),
  };

  // Simulate the post-hydration state: no stream handle open.
  window.SerfRenderer.liveStream = null;
  ta.value = "resume me";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  await new Promise(r => setTimeout(r, 20));
  pass(window.SerfRenderer.liveStream !== null, "expected AppWire stream opened after send-success on ended session");
  pass(subscriptions === 1, "expected exactly one AppWire subscription after resumed send, got " + subscriptions);
  pass(typeof notificationHandler === "function", "expected AppWire notification handler after resumed send");

  notificationHandler("item/completed", {
    threadId: "01TEST",
    ref: "local:01TEST",
    item: { type: "userMessage", id: "item_user_resume", text: "resume me" },
  });
  notificationHandler("item/started", {
    threadId: "01TEST",
    ref: "local:01TEST",
    item: { type: "agentMessage", id: "msg_resume" },
  });
  notificationHandler("item/agentMessage/delta", {
    threadId: "01TEST",
    ref: "local:01TEST",
    itemId: "msg_resume",
    delta: "live resumed answer",
  });
  notificationHandler("item/completed", {
    threadId: "01TEST",
    ref: "local:01TEST",
    item: { type: "agentMessage", id: "msg_resume" },
  });
  await new Promise(r => setTimeout(r, 10));

  pass(conv.textContent.includes("resume me"), "expected resumed user message to render without refresh");
  pass(
    !!conv.querySelector('.user-message[data-entry-idx="4"]'),
    "expected resumed user message to use authoritative turn index 4"
  );
  pass(conv.textContent.includes("live resumed answer"), "expected resumed assistant answer to render without refresh");
}

// 5c. A failed send must NOT open a live stream.
async function checkNoReconnectOnSendFailure() {
  window.SerfAppwire = {
    refForSession: (sessionId) => "local:" + sessionId,
    startTurn: () => Promise.reject(new Error("boom")),
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    readThread: () => Promise.resolve({ thread: { id: "01TEST", sessionId: "01TEST", turns: [] } }),
    eventsFromThread: () => [],
    eventsFromNotification: () => [],
  };
  window.SerfRenderer.liveStream = null;
  ta.value = "broken";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = false;
  const submitEvent = new window.Event("submit", { bubbles: true, cancelable: true });
  form.dispatchEvent(submitEvent);
  await new Promise(r => setTimeout(r, 10));
  pass(window.SerfRenderer.liveStream === null, "expected no AppWire stream opened on send failure");
  window.SerfAppwire = null;
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
  window.SerfRenderer.updateThreadState("active");
  window.SerfRenderer.setActiveTurnId("turn_steer");
  ta.value = "stop using mocks";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  steer.click();
  await new Promise(r => setTimeout(r, 10));
  pass(posted, "expected /steer POST when textarea has text");
  pass(ta.value === "", "expected textarea cleared after successful steer, got " + JSON.stringify(ta.value));

  // If the active turn ends while steer is in flight, the button must stay
  // disabled after the request settles.
  window.SerfRenderer.updateThreadState("active");
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
  pass(typeof settleSteer === "function", "expected /steer POST while active turn exists");
  window.SerfRenderer.setActiveTurnId("");
  if (typeof settleSteer === "function") settleSteer();
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
//
// After kata v80q the legacy addFiles / pendingAttachments pipeline was
// retired in favor of SerfComposerAttachments (kata r6a1/65mm). These
// tests drive the new pipeline: items live on
// SerfRenderer.composerPasteState.items, chips render as [data-attachment]
// inside [data-composer-attachments], and rejection banners surface on
// [data-attachment-error] (no per-chip error class anymore).

// A 1×1 transparent PNG — used both for the canvas re-encode fixture
// (above) and as the canonical "image body" for picker/drop tests.
const PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg==";
const PNG_BYTES = Buffer.from(PNG_BASE64, "base64");

function makePngFile(name) {
  return new window.File([new Uint8Array(PNG_BYTES)], name, { type: "image/png" });
}

// JSDOM's canvas / FileReader fire async; wait for the helper's enqueue
// promise to settle by yielding a few microtasks/timers.
async function waitForReads(n = 20) {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 5));
  }
}

const filePicker = window.document.querySelector("[data-file-picker]");
const dropZone = window.document.querySelector("[data-drop-zone]");
const attContainer = window.document.querySelector("[data-composer-attachments]");
const errBanner = window.document.querySelector("[data-attachment-error]");

function pendingItems() {
  return (window.SerfRenderer.composerPasteState && window.SerfRenderer.composerPasteState.items) || [];
}

function resetComposerState() {
  window.SerfRenderer.handleData("SESSION_START", {
    session_id: "01TEST",
    status: "idle",
    capabilities: { send: true, queue: false },
  });
  if (window.SerfRenderer.composerPasteState) {
    window.SerfRenderer.composerPasteState.items = [];
  } else {
    window.SerfRenderer.composerPasteState = { items: [] };
  }
  // Re-render chips so the new (empty) state is reflected in the DOM.
  if (window.SerfComposerAttachments) {
    window.SerfComposerAttachments.renderAttachmentChips(attContainer, window.SerfRenderer.composerPasteState);
  }
  if (errBanner) { errBanner.textContent = ""; errBanner.hidden = true; }
}

function makeAttachment(name) {
  return { type: "image", mediaType: "image/png", data: new Uint8Array(PNG_BYTES).buffer, name };
}

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
  resetComposerState();
  dispatchPickerChange([makePngFile("hello.png")]);
  await waitForReads();
  pass(pendingItems().length === 1,
    "expected 1 queued attachment, got " + pendingItems().length);
  const chips = attContainer.querySelectorAll("[data-attachment]");
  pass(chips.length === 1, "expected 1 chip, got " + chips.length);
  pass(chips[0].textContent.includes("hello.png"),
    "chip name missing 'hello.png': " + chips[0].textContent);
}

async function testDropAddsChips() {
  resetComposerState();
  const dropEvent = new window.Event("drop", { bubbles: true, cancelable: true });
  // Stub dataTransfer with two PNG files.
  Object.defineProperty(dropEvent, "dataTransfer", {
    value: { files: [makePngFile("a.png"), makePngFile("b.png")] },
  });
  dropZone.dispatchEvent(dropEvent);
  await waitForReads();
  const chips = attContainer.querySelectorAll("[data-attachment]");
  pass(chips.length === 2, "expected 2 chips after drop, got " + chips.length);
  pass(pendingItems().length === 2,
    "expected queue length 2, got " + pendingItems().length);
}

async function testRemoveChip() {
  // Should still have 2 from the drop test; click × on the first.
  const removeBtns = attContainer.querySelectorAll("[data-attachment-remove]");
  pass(removeBtns.length === 2, "expected 2 remove buttons, got " + removeBtns.length);
  removeBtns[0].click();
  pass(pendingItems().length === 1,
    "expected queue length 1 after remove, got " + pendingItems().length);
  const chips = attContainer.querySelectorAll("[data-attachment]");
  pass(chips.length === 1, "expected 1 chip after remove, got " + chips.length);
}

async function testSubmitWithTextAndImage() {
  resetComposerState();
  dispatchPickerChange([makePngFile("snap.png")]);
  await waitForReads();
  pass(pendingItems().length === 1, "expected 1 queued before submit");

  ta.value = "look at this";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  fetchResponseOk = true;
  lastFetch = null;
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await waitForReads();

  pass(lastFetch !== null, "expected fetch on submit");
  const body = lastFetch && lastFetch.opts && JSON.parse(lastFetch.opts.body);
  pass(body && body.text === "look at this", "expected text in body, got " + JSON.stringify(body));
  pass(body && Array.isArray(body.items) && body.items.length === 1,
    "expected 1 item in body, got " + JSON.stringify(body && body.items));
  if (body && body.items && body.items[0]) {
    pass(body.items[0].type === "image",
      "expected type=image, got " + body.items[0].type);
    pass(body.items[0].mediaType === "image/png",
      "expected mediaType=image/png, got " + body.items[0].mediaType);
    pass(body.items[0].data === PNG_BASE64,
      "expected base64-encoded canvas PNG, got " + body.items[0].data);
    pass(body.items[0].name === "snap.png",
      "expected name 'snap.png', got " + body.items[0].name);
  }
}

async function testSubmitClearsQueue() {
  // Continuation of testSubmitWithTextAndImage's success path.
  pass(pendingItems().length === 0,
    "expected queue cleared after success, got " + pendingItems().length);
  const chips = attContainer.querySelectorAll("[data-attachment]");
  pass(chips.length === 0, "expected no chips after success, got " + chips.length);
}

async function testSubmitPreservesAttachmentsAddedWhileInFlight() {
  resetComposerState();
  const first = makeAttachment("first.png");
  const second = makeAttachment("second.png");
  window.SerfRenderer.composerPasteState.items = [first];
  window.SerfRenderer.renderComposerChips();

  const originalFetch = window.fetch;
  let resolveSend;
  window.fetch = (url, opts) => {
    if (typeof url === "string" && url.includes("/send")) {
      lastFetch = { url, opts };
      return new Promise((resolve) => {
        resolveSend = () => resolve({
          ok: true,
          status: 202,
          json: () => Promise.resolve([]),
          text: () => Promise.resolve(""),
        });
      });
    }
    return originalFetch(url, opts);
  };

  ta.value = "in flight";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 5));

  window.SerfRenderer.composerPasteState.items.push(second);
  window.SerfRenderer.renderComposerChips();
  ta.value = "new draft\n\n[image 2]";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  pass(pendingItems().length === 2,
    "expected second attachment staged while send is in flight, got " + pendingItems().length);

  resolveSend();
  await waitForReads();
  window.fetch = originalFetch;

  const body = lastFetch && lastFetch.opts && JSON.parse(lastFetch.opts.body);
  pass(body && Array.isArray(body.items) && body.items.length === 1 && body.items[0].name === "first.png",
    "in-flight send should use submitted snapshot only, got " + JSON.stringify(body && body.items));
  pass(pendingItems().length === 1 && pendingItems()[0] === second,
    "successful send should preserve newly staged attachment, got " + pendingItems().map(i => i.name).join(","));
  pass(ta.value === "new draft\n\n[image 2]",
    "successful send should preserve newer composer draft, got " + JSON.stringify(ta.value));
  const chips = attContainer.querySelectorAll("[data-attachment]");
  pass(chips.length === 1 && chips[0].textContent.includes("second.png"),
    "expected chip for second.png after in-flight send, got " + attContainer.textContent);
	}

async function testSubmitBlocksWhileAttachmentIsDecoding() {
	resetComposerState();
	dispatchPickerChange([makePngFile("slow.png")]);
	pass(pendingItems().length === 1 && pendingItems()[0].pending === true,
	  "expected synchronous pending placeholder before decode");
	ta.value = "too soon";
	ta.dispatchEvent(new window.Event("input", { bubbles: true }));
	lastFetch = null;
	form.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
	await new Promise((resolve) => setTimeout(resolve, 5));
	pass(lastFetch === null, "expected no fetch while attachment is still processing");
	const banners = Array.from(window.document.querySelectorAll(".banner, .diagnostic, [data-banner]")).map((el) => el.textContent).join("\n");
	pass(/still processing/.test(banners), "expected still-processing banner, got " + JSON.stringify(banners));
	await waitForReads();
	pass(pendingItems().length === 1 && pendingItems()[0].pending === false,
	  "expected decode to complete after blocked submit");
}

async function testSubmitEmptyDoesNothing() {
  ta.value = "";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  resetComposerState();
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

async function testRejectsNonImage() {
  resetComposerState();
  const txt = new window.File([new Uint8Array([1, 2, 3])], "notes.txt", { type: "text/plain" });
  dispatchPickerChange([txt]);
  await waitForReads();
  pass(pendingItems().length === 0,
    "expected non-image rejected from queue");
  // The new pipeline surfaces non-image rejections on the shared
  // [data-attachment-error] banner (not per-chip). The banner becomes
  // visible (hidden=false) with text mentioning the offending filename.
  pass(errBanner && errBanner.hidden === false,
    "expected attachment-error banner visible after non-image picker change");
  pass(errBanner && errBanner.textContent.includes("notes.txt"),
    "expected banner text to mention 'notes.txt', got " + (errBanner && errBanner.textContent));
}

(async () => {
  await checkReset();
  await checkFailureKeepsValue();
  await checkProcessingSendCapabilityKeepsSendMode();
  await checkReconnectsLiveAfterSendOnEndedSession();
  await checkNoReconnectOnSendFailure();
  await checkSteerWired();
  await checkSlashOpensPalette();

  await testFilePickerAddsChip();
  await testDropAddsChips();
  await testRemoveChip();
	  await testSubmitWithTextAndImage();
	  await testSubmitClearsQueue();
	  await testSubmitPreservesAttachmentsAddedWhileInFlight();
	  await testSubmitBlocksWhileAttachmentIsDecoding();
	  await testSubmitEmptyDoesNothing();
  await testUnavailableSendDoesNotSubmit();
  await testRejectsNonImage();

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
