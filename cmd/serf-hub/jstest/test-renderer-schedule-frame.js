// scheduleFrame must work where window.requestAnimationFrame is undefined
// (plain jsdom, and as a proxy for exotic embedded webviews).
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only" });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
R.init(window.document.getElementById("conversation"));

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  pass(typeof R.scheduleFrame === "function", "scheduleFrame exists");
  pass(typeof window.requestAnimationFrame === "undefined", "test env really has no rAF");
  let ran = 0;
  R.scheduleFrame(() => { ran++; });
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "scheduleFrame falls back to a timer when rAF is missing");
  // cancelFrame suppresses a pending callback.
  R.scheduleFrame(() => { ran++; });
  R.cancelFrame();
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "cancelFrame drops the pending callback");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scheduleFrame works without requestAnimationFrame");
  process.exit(0);
})();
