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

// After rendering an empty task list, the status text must read "no tasks yet".
window.SerfRendererInternal.updateTasksBadge(0, 0, "");
pass(textEl.textContent === "no tasks yet",
  "empty task list badge should read 'no tasks yet', not the terminal 'no tasks' (got: " + textEl.textContent + ")");

if (failures.length === 0) {
  console.log("PASS: task badge reads 'no tasks yet' for an empty task list");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
