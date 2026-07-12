// Verify the task-status badge's zero-tasks resting copy reads "no tasks yet"
// (not the terminal-sounding bare "no tasks"), matching the tasks panel's own
// empty-state title ("No tasks yet").
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <button data-tasks-trigger><span class="status-key">tasks</span><span class="status-value" data-task-status-text>—</span></button>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const textEl = window.document.querySelector("[data-task-status-text]");

const trigger = window.document.querySelector("[data-tasks-trigger]");

// After rendering an empty task list, the status text must read "no tasks yet".
window.SerfRendererInternal.updateTasksBadge(0, 0, "");
pass(textEl.textContent === "no tasks yet",
  "empty task list badge should read 'no tasks yet', not the terminal 'no tasks' (got: " + textEl.textContent + ")");
pass(!trigger.hasAttribute("aria-label"),
  "empty task list must not leave a stale aria-label on the trigger");

window.SerfRendererInternal.updateTasksBadge(1, 3, "Implement compact rail");
const badge = window.document.querySelector(".panel-toggle-badge");
pass(badge && badge.textContent === "1/3", "task badge is the sole numeric 1/3 progress display");
pass(textEl.textContent === "Implement compact rail",
  "task-status text keeps only the current task summary, got " + JSON.stringify(textEl.textContent));
pass(!/\b1\/3\b/.test(textEl.textContent), "task-status text must not duplicate the badge count");
// The phone strip hides the "tasks" label word and leads with a decorative
// glyph, so the button must carry a full spoken label: progress + current task.
pass(trigger.getAttribute("aria-label") === "tasks 1/3 — Implement compact rail",
  "trigger aria-label must speak progress + current task, got " + JSON.stringify(trigger.getAttribute("aria-label")));

window.SerfRendererInternal.updateTasksBadge(3, 3, "all tasks complete");
pass(trigger.getAttribute("aria-label") === "tasks 3/3 — all tasks complete",
  "completed list aria-label must still speak progress, got " + JSON.stringify(trigger.getAttribute("aria-label")));

window.SerfRendererInternal.updateTasksBadge(0, 0, "");
pass(!trigger.hasAttribute("aria-label"),
  "clearing the task list must remove the aria-label again");

if (failures.length === 0) {
  console.log("PASS: task badge reads 'no tasks yet' for an empty task list");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
