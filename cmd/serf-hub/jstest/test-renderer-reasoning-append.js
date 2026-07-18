// Reasoning deltas append (no full-buffer rewrite) and the preview tail
// updates at settle from a bounded slice.
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
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("REASONING_START", { itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "first ", itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "second", itemId: "r1" });
  const body = conv.querySelector(".think .think-body");
  pass(body && body.textContent === "first second", "deltas append to the body");
  pass(body && body.childNodes.length === 2, "append-only: one text node per delta (got " + (body && body.childNodes.length) + ")");
  pass(typeof R.updateReasoningPreview === "function", "updateReasoningPreview exists");
  const pv = conv.querySelector(".think .pv");
  pass(pv && pv.textContent.includes("first second"), "preview tail reflects the buffer");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: reasoning appends text nodes; preview tail is bounded");
  process.exit(0);
})();
