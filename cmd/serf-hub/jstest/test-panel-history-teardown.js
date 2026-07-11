// All details/tasks panel dismissal paths must converge on one teardown —
// including history navigation (iOS swipe-back fires popstate, and htmx
// restores the prior page via htmx:historyRestore). Before this contract,
// a swipe-back with the details sheet open left the panel/scrim/focus-trap
// state behind, and (worse) the htmx body-snapshot restore re-ran every
// asset <script>, double-binding the delegated handlers so each tap toggled
// twice — the "every button is dead" bug.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div class="conversation" id="conversation"
       data-session-id="01TEST"
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

// Count activate/deactivate pairs: history teardown must release the trap.
let trapActive = 0;
window.SerfFocusTrap = {
  activate: function () { trapActive++; return { handle: true }; },
  deactivate: function () { trapActive--; },
};

require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const detailsTrigger = window.document.querySelector("[data-details-trigger]");
const tasksTrigger = window.document.querySelector("[data-tasks-trigger]");

// ── popstate (swipe-back / browser back) tears the details panel down ──
detailsTrigger.click();
pass(window.document.getElementById("details-panel") !== null, "details panel opens");
pass(window.document.getElementById("panel-scrim") !== null, "details scrim present");
window.dispatchEvent(new window.Event("popstate"));
pass(window.document.getElementById("details-panel") === null, "popstate must remove the details panel");
pass(window.document.getElementById("panel-scrim") === null, "popstate must remove the scrim");
pass(trapActive === 0, "popstate teardown must deactivate the focus trap (active=" + trapActive + ")");
pass(detailsTrigger.hasAttribute("data-active") === false, "popstate must reset the details trigger active state");

// The page must be fully interactive afterwards: one click reopens the panel.
detailsTrigger.click();
pass(window.document.getElementById("details-panel") !== null, "details panel reopens with a single tap after popstate teardown");
window.dispatchEvent(new window.Event("popstate"));
pass(window.document.getElementById("details-panel") === null, "second popstate teardown also works");

// ── htmx:historyRestore tears the tasks panel down too ──
tasksTrigger.click();
pass(window.document.getElementById("tasks-panel") !== null, "tasks panel opens");
const restore = new window.Event("htmx:historyRestore", { bubbles: true });
window.document.body.dispatchEvent(restore);
pass(window.document.getElementById("tasks-panel") === null, "htmx:historyRestore must remove the tasks panel");
pass(window.document.getElementById("panel-scrim") === null, "htmx:historyRestore must remove the scrim");
pass(trapActive === 0, "htmx:historyRestore teardown must deactivate the focus trap (active=" + trapActive + ")");
pass(tasksTrigger.hasAttribute("data-active") === false, "htmx:historyRestore must reset the tasks trigger active state");

if (failures.length === 0) {
  console.log("PASS: panel teardown converges across popstate and htmx history restore");
  process.exit(0);
}
for (const f of failures) console.log(f);
process.exit(1);
