// A communicate tool call landing while its agentMessage is still streaming
// (raw tail mode) must be deduped against the SOURCE buffer, not rendered text.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => "<p>" + t + "</p>" };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  const text = "x".repeat(4100) + " **bold tail**";
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: text }); // switches to raw-tail mode
  pass(R.lastElementIsAssistantText(text) === true,
    "dedup matches the streaming message by source, raw tail included");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: communicate dedup compares against the source buffer while streaming");
  process.exit(0);
})();
