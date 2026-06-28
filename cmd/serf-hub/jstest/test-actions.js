// Verify the workspace title-bar action buttons (interrupt, compact,
// shutdown) post to /s/<id>/<action> via fetch and that disabled buttons
// don't fire.
const fs = require("fs");
const { JSDOM } = require("jsdom");


const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-action-trigger="interrupt">interrupt</button>
    <button data-action-trigger="compact">compact</button>
    <button data-action-trigger="shutdown" class="header-action-danger">shutdown</button>
    <button data-action-trigger="interrupt" id="disabled-btn" disabled>interrupt-d</button>
  </div>
  <header class="workspace-header" data-session-id="01ACT001"></header>
  <div class="conversation" id="conversation"
       data-session-id="01ACT001"
       data-active-turn-id="turn_1"
       data-state="active">conversation body</div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: t => t };

// Capture fetch calls.
const calls = [];
window.fetch = (url, opts) => {
  calls.push({ url, opts });
  return Promise.resolve({ ok: true, text: () => Promise.resolve("") });
};

require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const interruptBtn = window.document.querySelector('[data-action-trigger="interrupt"]:not([disabled])');
const compactBtn = window.document.querySelector('[data-action-trigger="compact"]');
const shutdownBtn = window.document.querySelector('[data-action-trigger="shutdown"]');
const disabledBtn = window.document.getElementById("disabled-btn");

// 1. interrupt button → fetch posts to /s/01ACT001/interrupt, no confirm.
let confirmCalled = false;
window.confirm = () => { confirmCalled = true; return true; };
calls.length = 0;
interruptBtn.click();
pass(calls.length === 1, "interrupt should make exactly one fetch call");
pass(calls[0] && calls[0].url === "/s/01ACT001/interrupt", "interrupt URL should be /s/01ACT001/interrupt, got " + (calls[0] && calls[0].url));
pass(calls[0] && calls[0].opts && calls[0].opts.method === "POST", "interrupt should use POST");
pass(calls[0] && /"turn_id":"turn_1"/.test(calls[0].opts.body || ""), "interrupt should send active turn id");
pass(!confirmCalled, "interrupt should NOT call window.confirm");

// 2. compact button → fetch posts to /s/01ACT001/compact, no confirm.
confirmCalled = false;
calls.length = 0;
compactBtn.click();
pass(calls.length === 1, "compact should make exactly one fetch call");
pass(calls[0] && calls[0].url === "/s/01ACT001/compact", "compact URL should be /s/01ACT001/compact");
pass(!confirmCalled, "compact should NOT call window.confirm");

// 3. shutdown → fetch posts immediately; stopping a daemon is resumable.
confirmCalled = false;
window.confirm = () => { confirmCalled = true; return true; };
calls.length = 0;
shutdownBtn.click();
pass(!confirmCalled, "shutdown should NOT call window.confirm");
pass(calls.length === 1, "shutdown should make a fetch call");
pass(calls[0] && calls[0].url === "/s/01ACT001/shutdown", "shutdown URL should be /s/01ACT001/shutdown");

// 4. shutdown still posts even when window.confirm would decline.
confirmCalled = false;
window.confirm = () => { confirmCalled = true; return false; };
calls.length = 0;
shutdownBtn.click();
pass(!confirmCalled, "shutdown should not ask for confirmation");
pass(calls.length === 1, "shutdown should POST without confirmation");

// 5. disabled interrupt button → no fetch.
confirmCalled = false;
window.confirm = () => true;
calls.length = 0;
disabledBtn.click();
pass(calls.length === 0, "disabled action button must NOT fire fetch");

// ── SerfAppwire primary path ─────────────────────────────────────────────────
// Install the AppWire stub. From here on, triggerSessionAction takes the
// SerfAppwire branch and must NOT fall through to fetch().
const appwireCalls = [];
window.SerfAppwire = {
  action: (sid, name, turnId) => {
    appwireCalls.push({ sid, name, turnId });
    return Promise.resolve();
  },
};

// 6. interrupt via SerfAppwire — correct session id, action name, and turn id.
appwireCalls.length = 0;
calls.length = 0;
interruptBtn.click();
pass(appwireCalls.length === 1, "interrupt should call SerfAppwire.action once (not fetch)");
pass(calls.length === 0, "interrupt must NOT fall through to fetch when SerfAppwire is present");
pass(appwireCalls[0] && appwireCalls[0].sid === "01ACT001", "interrupt: SerfAppwire.action must receive the session id");
pass(appwireCalls[0] && appwireCalls[0].name === "interrupt", "interrupt: SerfAppwire.action must receive action name 'interrupt'");
pass(appwireCalls[0] && appwireCalls[0].turnId === "turn_1", "interrupt: SerfAppwire.action must receive active turn id");

// 7. compact via SerfAppwire.
appwireCalls.length = 0;
calls.length = 0;
compactBtn.click();
pass(appwireCalls.length === 1, "compact should call SerfAppwire.action once");
pass(calls.length === 0, "compact must NOT fall through to fetch when SerfAppwire is present");
pass(appwireCalls[0] && appwireCalls[0].name === "compact", "compact: SerfAppwire.action must receive action name 'compact'");

// 8. shutdown via SerfAppwire.
appwireCalls.length = 0;
calls.length = 0;
shutdownBtn.click();
pass(appwireCalls.length === 1, "shutdown should call SerfAppwire.action once");
pass(calls.length === 0, "shutdown must NOT fall through to fetch when SerfAppwire is present");
pass(appwireCalls[0] && appwireCalls[0].name === "shutdown", "shutdown: SerfAppwire.action must receive action name 'shutdown'");

// 9. disabled interrupt button must not fire SerfAppwire.action.
appwireCalls.length = 0;
disabledBtn.click();
pass(appwireCalls.length === 0, "disabled action button must NOT fire SerfAppwire.action");

if (failures.length === 0) {
  console.log("PASS: workspace action buttons (interrupt/compact/shutdown) wired correctly");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
