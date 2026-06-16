// Verify that every task in the sidebar panel becomes an expandable
// <details> element with full metadata in <dl>.
const fs = require("fs");
const { JSDOM } = require("jsdom");


const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="ended"></div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: t => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

require("./load-renderer").evalRenderer(window);

// Build the panel directly using the internal renderer.
const tasks = [
  { id: 1, type: "research", description: "Audit existing code", prompt: "Read the relevant files and summarize how the current implementation works.", status: "done", depends_on: [], notes: ["Found three relevant files", "Pattern is consistent with other modules"] },
  { id: 2, type: "implement", description: "Add the new endpoint", prompt: "Wire up POST /widgets per spec.", status: "in_progress", depends_on: [1], reasoning_effort: "high" },
  { id: 3, type: "verify", description: "Write integration tests", prompt: "Cover golden path and error cases.", status: "open", depends_on: [2] },
];

// Trigger panel render via the click handler. Since fetch is mocked to return [],
// open the panel and inject tasks via the internal renderTasksInto.
const panel = window.document.createElement("aside");
panel.id = "tasks-panel";
window.document.body.appendChild(panel);

// Reach into the IIFE: renderTasksInto isn't exposed. Use toggleTasksPanel
// click flow + manual injection. Easier: simulate the fetch resolving.
// Actually, since renderTasksInto is closure-private, we re-implement the
// invocation by clicking the trigger and replacing the fetch with one
// that returns our test tasks.
const calls = [];
window.fetch = (url) => {
  calls.push(url);
  if (url.endsWith("/tasks")) {
    return Promise.resolve({ ok: true, json: () => Promise.resolve(tasks) });
  }
  return Promise.resolve({ ok: false });
};
panel.remove();

// Click the tasks trigger.
window.document.querySelector("[data-tasks-trigger]").click();

// Wait for fetch microtask.
setTimeout(() => {
  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  const p = window.document.getElementById("tasks-panel");
  pass(p !== null, "tasks panel should exist");
  if (!p) { console.log(failures.join("\n")); process.exit(1); }

  // Every task row should be a <details> (expandable).
  const rows = p.querySelectorAll(".tasks-list .task-row");
  pass(rows.length === 3, "expected 3 task rows, got " + rows.length);

  const detailsEls = p.querySelectorAll(".task-row-details");
  pass(detailsEls.length === 3, "every row should be expandable, got " + detailsEls.length);

  // Each row's expanded content should include prompt and type.
  const firstDl = detailsEls[0].querySelector(".task-detail");
  pass(firstDl !== null, "first row should have a task-detail dl");
  if (firstDl) {
    pass(firstDl.textContent.includes("Read the relevant files"), "first row prompt should be in detail (was: " + firstDl.textContent.slice(0, 100) + ")");
    // And the description should be in the summary, not duplicated in detail.
    const firstSummary = detailsEls[0].querySelector("summary");
    pass(firstSummary && firstSummary.textContent.includes("Audit existing code"), "first row description should be in summary");
    pass(firstDl.textContent.includes("research"), "first row type should appear");
    pass(firstDl.textContent.includes("Found three"), "first row notes should appear");
  }

  // Second row should show depends_on
  const secondDl = detailsEls[1].querySelector(".task-detail");
  pass(secondDl && secondDl.textContent.includes("#1"), "second row depends_on should mention #1");
  pass(secondDl && secondDl.textContent.includes("high"), "second row reasoning_effort should appear");

  // Badge should reflect 1/3 done.
  const badge = window.document.querySelector(".panel-toggle-badge");
  pass(badge !== null, "badge should exist when tasks present");
  pass(badge && badge.textContent === "1/3", "badge should be 1/3, got: " + (badge ? badge.textContent : "null"));

  if (failures.length === 0) {
    console.log("PASS: sidebar panel fully expandable");
    console.log("Sample row:");
    console.log(detailsEls[0].outerHTML.slice(0, 500));
    process.exit(0);
  } else {
    console.log("PANEL HTML:");
    console.log(p.innerHTML);
    console.log("\nFailures:");
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
}, 50);
