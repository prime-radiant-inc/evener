// Verify click-outside dismissal of the tasks and details slide-over panels
// (and that the existing Esc behavior still closes them).
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div class="conversation" id="conversation"
       data-session-id="01TEST"
       data-replay-url=""
       data-events-url=""
       data-state="ended">conversation body</div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: t => t };
window.fetch = (url) => {
  if (url && url.endsWith("/tasks")) {
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  }
  if (url && url.endsWith("/details")) {
    return Promise.resolve({ ok: true, text: () => Promise.resolve("<div class='details-body'>details content</div>") });
  }
  return Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
};
window.EventSource = class { constructor(){this.listeners=new Map()} addEventListener(n,f){const l=this.listeners.get(n)||[];l.push(f);this.listeners.set(n,l)} set onerror(f){} close(){} };

window.eval(rendererSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// Helper: dispatch a real mousedown event on an element.
function mousedown(el) {
  const ev = new window.MouseEvent("mousedown", { bubbles: true, cancelable: true });
  el.dispatchEvent(ev);
}

// The body click delegate handles tasks/details toggle. Use .click() which
// dispatches a click event, but the body click handler reads e.target — same
// pattern test-sidebar.js uses and which has been proven to work.
const tasksTrigger = window.document.querySelector("[data-tasks-trigger]");
const detailsTrigger = window.document.querySelector("[data-details-trigger]");
const conversation = window.document.querySelector(".conversation");

// 1. Click tasks trigger → panel appears.
tasksTrigger.click();
let panel = window.document.getElementById("tasks-panel");
pass(panel !== null, "tasks panel should open after clicking trigger");

// 2. Click somewhere in .conversation (outside panel + outside trigger) →
//    panel removed.
mousedown(conversation);
panel = window.document.getElementById("tasks-panel");
pass(panel === null, "tasks panel should be removed by outside mousedown");

// 3. Click trigger again → reopens.
tasksTrigger.click();
panel = window.document.getElementById("tasks-panel");
pass(panel !== null, "tasks panel should reopen after clicking trigger again");

// 4. Click inside the panel (on a child) → still open.
//    Inject a child so we have something to click.
const inner = window.document.createElement("header");
inner.textContent = "panel header";
panel.appendChild(inner);
mousedown(inner);
panel = window.document.getElementById("tasks-panel");
pass(panel !== null, "tasks panel should remain open when clicking inside");

// 5a. With tasks open, click details trigger → tasks gone, details open.
detailsTrigger.click();
const tasksAfterSwap = window.document.getElementById("tasks-panel");
const detailsAfterSwap = window.document.getElementById("details-panel");
pass(tasksAfterSwap === null, "tasks panel should be replaced by details panel");
pass(detailsAfterSwap !== null, "details panel should open after clicking details trigger");

// 5b. Now click outside details → details gone. Verifies the orphaned tasks
//     listener (registered while tasks was open) doesn't get confused.
mousedown(conversation);
const detailsAfterOutside = window.document.getElementById("details-panel");
pass(detailsAfterOutside === null, "details panel should close on outside mousedown");

// 6. Open tasks again, press Esc → still closes (regression).
tasksTrigger.click();
panel = window.document.getElementById("tasks-panel");
pass(panel !== null, "tasks panel should reopen for esc test");
const esc = new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true });
window.document.dispatchEvent(esc);
panel = window.document.getElementById("tasks-panel");
pass(panel === null, "tasks panel should close on Esc");

// 7. Verify clicking the SAME trigger that opened a panel still toggles it
//    closed (don't break existing behavior). The body click delegate fires
//    on click events. Open the panel first, then click the trigger again.
tasksTrigger.click();
pass(window.document.getElementById("tasks-panel") !== null, "panel open before toggle");
tasksTrigger.click();
pass(window.document.getElementById("tasks-panel") === null, "clicking trigger again should close panel");

if (failures.length === 0) {
  console.log("PASS: click-outside dismissal works for tasks and details panels");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
