// Sticky composer drafts (issue #21): unsent textarea content must persist
// per session in localStorage, survive session swaps / reloads, stay isolated
// between sessions, and clear on successful send / steer / drain. Empty
// content must never leave a stored draft behind.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const DRAFTS_SRC = fs.readFileSync(path.resolve(__dirname, "../assets/drafts.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const DRAFT_PREFIX = "serf-hub.draft.";

// makeDOM builds a fresh workspace (new "page load") for sessionId, seeded
// with the given localStorage entries — this models both a full reload and a
// session swap, since each swap re-renders the composer from scratch.
function makeDOM(sessionId, seed) {
  const sessionAttr = sessionId ? ` data-session-id="${sessionId}"` : "";
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="${sessionId || "01HOME"}"></header>
    <div id="conversation"
         data-session-id="${sessionId || "01HOME"}"
         data-active-turn-id="turn_1"
         data-state="active"></div>
    <form class="workspace-input" data-input-form${sessionAttr}>
      <textarea class="message-input" rows="1"></textarea>
      <button type="button" data-steer-trigger data-capability-steer="true">steer</button>
      <button type="submit" class="send-btn" data-capability-send="true" data-capability-queue="true">send</button>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const { window } = dom;
  for (const [k, v] of Object.entries(seed || {})) window.localStorage.setItem(k, v);
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, status: 202, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  window.eval(DRAFTS_SRC);
  require("./load-renderer").evalRenderer(window);
  window.SerfRenderer.init(window.document.getElementById("conversation"));
  return dom;
}

function snapshotStorage(window) {
  const out = {};
  for (let i = 0; i < window.localStorage.length; i++) {
    const k = window.localStorage.key(i);
    out[k] = window.localStorage.getItem(k);
  }
  return out;
}

function type(dom, text) {
  const ta = dom.window.document.querySelector(".message-input");
  ta.value = text;
  ta.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  return ta;
}

// 1. Typing persists the draft under the session's key.
(function testTypePersists() {
  const dom = makeDOM("01AAA");
  type(dom, "draft one");
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "draft one",
    "typing should persist draft for 01AAA, got " +
      JSON.stringify(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA"))
  );
})();

// 2. A fresh page load (new DOM seeded with the same storage) restores the draft.
(function testReloadRestores() {
  const first = makeDOM("01AAA");
  type(first, "draft one");
  const seed = snapshotStorage(first.window);

  const second = makeDOM("01AAA", seed);
  const ta = second.window.document.querySelector(".message-input");
  pass(ta.value === "draft one", "reload should restore draft, got " + JSON.stringify(ta.value));
})();

// 3. Session swap: each session keeps its own draft, no cross-contamination.
(function testPerSessionIsolation() {
  const a = makeDOM("01AAA");
  type(a, "draft for A");
  const seedAfterA = snapshotStorage(a.window);

  // Open session B with A's storage: B's composer must be empty.
  const b = makeDOM("01BBB", seedAfterA);
  const taB = b.window.document.querySelector(".message-input");
  pass(taB.value === "", "session B must not see session A's draft, got " + JSON.stringify(taB.value));

  type(b, "draft for B");
  const seedBoth = snapshotStorage(b.window);
  pass(seedBoth[DRAFT_PREFIX + "01AAA"] === "draft for A", "A's draft must survive B's typing");
  pass(seedBoth[DRAFT_PREFIX + "01BBB"] === "draft for B", "B's draft must be stored under B's key");

  // Swap back to A: A's draft is restored, B's untouched.
  const aAgain = makeDOM("01AAA", seedBoth);
  const taA = aAgain.window.document.querySelector(".message-input");
  pass(taA.value === "draft for A", "swapping back to A should restore A's draft, got " + JSON.stringify(taA.value));
})();

// 4. A composer form without a session id falls back to a stable shared key.
(function testFallbackKey() {
  const dom = makeDOM(null);
  type(dom, "home draft");
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "new") === "home draft",
    "session-less composer should use the fallback key, storage: " +
      JSON.stringify(snapshotStorage(dom.window))
  );
  const again = makeDOM(null, snapshotStorage(dom.window));
  pass(
    again.window.document.querySelector(".message-input").value === "home draft",
    "fallback-key draft should restore on reload"
  );
})();

// 5. Successful send clears the draft (and only the draft of that session).
async function testSendClears() {
  const dom = makeDOM("01AAA", { [DRAFT_PREFIX + "01BBB"]: "keep me" });
  type(dom, "ship it");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "ship it", "draft stored before send");

  const form = dom.window.document.querySelector("form[data-input-form]");
  form.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((r) => setTimeout(r, 20));

  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === null,
    "successful send should clear the draft, got " + JSON.stringify(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA"))
  );
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01BBB") === "keep me",
    "send must not touch other sessions' drafts"
  );

  // Reload after send: composer must come back empty.
  const after = makeDOM("01AAA", snapshotStorage(dom.window));
  pass(
    after.window.document.querySelector(".message-input").value === "",
    "composer must stay empty after send + reload"
  );
}

// 6. Successful steer (⇧↵ / steer button) clears the draft too.
async function testSteerClears() {
  const dom = makeDOM("01AAA");
  type(dom, "steer this");
  const steer = dom.window.document.querySelector("[data-steer-trigger]");
  steer.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await new Promise((r) => setTimeout(r, 20));
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === null,
    "successful steer should clear the draft, got " + JSON.stringify(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA"))
  );
  const ta = dom.window.document.querySelector(".message-input");
  pass(ta.value === "", "steer should clear the textarea");
}

// 7. Empty content never persists; clearing the textarea removes the draft.
(function testEmptyClearsStoredValue() {
  const dom = makeDOM("01AAA");
  const ta = type(dom, "temporary");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "temporary", "draft stored");

  ta.value = "";
  ta.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === null,
    "clearing the textarea should remove the stored draft"
  );

  // Whitespace-only content is not a meaningful draft either.
  type(dom, "   \n  ");
  pass(
    dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === null,
    "whitespace-only content should not persist a draft"
  );
})();

// 8. A composer element that survives a session swap must not leak the old
// session's text into the new session: re-binding after the form's
// data-session-id changed clears the stale textarea before restoring the new
// session's draft, and later typing writes only under the new key.
(function testSurvivingElementDoesNotLeak() {
  const dom = makeDOM("01AAA");
  const ta = type(dom, "draft-from-A");
  const form = dom.window.document.querySelector("form[data-input-form]");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "draft-from-A", "A draft stored");

  // Simulate a swap where the element survives (morph/reuse): the form's
  // session id flips to B while the textarea still shows A's text.
  form.dataset.sessionId = "01BBB";
  dom.window.SerfDrafts.bind(form);

  pass(ta.value === "", "stale text from A must not stay visible in B's composer, got " + JSON.stringify(ta.value));
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "draft-from-A", "A's stored draft must survive");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01BBB") === null, "nothing may be written under B's key on rebind");

  type(dom, "draft-from-B");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01BBB") === "draft-from-B", "B draft stored under B's key");
  pass(dom.window.localStorage.getItem(DRAFT_PREFIX + "01AAA") === "draft-from-A", "A draft untouched by B typing");

  // Same-session re-bind (mid-typing) still preserves the textarea.
  dom.window.SerfDrafts.bind(form);
  pass(ta.value === "draft-from-B", "same-session re-bind preserves in-progress text");
})();

// 9. Browser form-state restore (WebKit/Blink refill the textarea from the
// previous page on navigation): a FRESH composer element pre-filled with text
// that is verbatim another session's stored draft must be cleared, not kept.
// Unmatched pre-filled text is the user's own fresh typing and must survive.
(function testBrowserRestoredValueCleared() {
  // Standalone fresh element (no renderer init): the browser refilled the box
  // with A's draft text before drafts.js ever bound it.
  function freshForm(sessionId, prefill, seed) {
    const dom = new JSDOM(`<!DOCTYPE html><html><body>
      <form class="workspace-input" data-input-form data-session-id="${sessionId}">
        <textarea class="message-input" rows="1"></textarea>
      </form>
    </body></html>`, { runScripts: "outside-only", url: "http://localhost/" });
    for (const [k, v] of Object.entries(seed || {})) dom.window.localStorage.setItem(k, v);
    dom.window.document.querySelector(".message-input").value = prefill;
    dom.window.eval(DRAFTS_SRC);
    dom.window.SerfDrafts.bind(dom.window.document.querySelector("form[data-input-form]"));
    return dom.window.document.querySelector(".message-input").value;
  }
  const cleared = freshForm("01BBB", "draft-from-A", { [DRAFT_PREFIX + "01AAA"]: "draft-from-A" });
  pass(cleared === "", "browser-restored foreign draft must be cleared, got " + JSON.stringify(cleared));

  // Fresh typing the store doesn't know is never touched.
  const kept = freshForm("01CCC", "brand new thought", { [DRAFT_PREFIX + "01AAA"]: "draft-from-A" });
  pass(kept === "brand new thought", "unmatched pre-bind text is fresh typing and must survive");
})();

(async () => {
  await testSendClears();
  await testSteerClears();

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: composer drafts are sticky per session (localStorage)");
  process.exit(0);
})();
