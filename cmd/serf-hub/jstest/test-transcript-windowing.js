// Windowing: stylesheet carries content-visibility on conversation children;
// prepend schedules a next-frame scroll correction.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
pass(/\.conversation\s*>\s*\*\s*\{[^}]*content-visibility:\s*auto/.test(css),
  "conversation children get content-visibility: auto");
pass(/contain-intrinsic-size:\s*auto\s+\d+px/.test(css),
  "entries carry a remembered-size estimate");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  let scheduled = 0;
  const realSchedule = R.scheduleFrame.bind(R);
  R.scheduleFrame = (cb) => { scheduled++; realSchedule(cb); };
  window.SerfAppwire = window.SerfAppwire || {};
  window.SerfAppwire.eventsFromTurns = () => [];
  R.prependOlderTurns([{ id: "old1" }]);
  pass(scheduled === 1, "prepend schedules a next-frame settle correction");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: windowing CSS present; prepend settles in two phases");
  process.exit(0);
})();
