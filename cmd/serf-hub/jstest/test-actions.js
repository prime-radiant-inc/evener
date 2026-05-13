// Verify the workspace title-bar action buttons (interrupt, compact,
// shutdown) post to /s/<id>/<action> via fetch and that disabled buttons
// don't fire.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

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
       data-replay-url=""
       data-events-url=""
       data-state="processing">conversation body</div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: t => t };

// Capture fetch calls.
const calls = [];
window.fetch = (url, opts) => {
  calls.push({ url, opts });
  return Promise.resolve({ ok: true, text: () => Promise.resolve("") });
};
window.EventSource = class { constructor(){this.listeners=new Map()} addEventListener(n,f){const l=this.listeners.get(n)||[];l.push(f);this.listeners.set(n,l)} set onerror(f){} close(){} };

window.eval(rendererSrc);

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

if (failures.length === 0) {
  console.log("PASS: workspace action buttons (interrupt/compact/shutdown) wired correctly");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
