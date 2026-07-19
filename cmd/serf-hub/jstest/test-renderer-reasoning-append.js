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
  // ── The preview shows the NEWEST text, not a stale prefix (F5) ─────────
  // clip(prefix, 200) of the last-400 slice froze the teleprompter on chars
  // -400..-201 forever. With a >400-char buffer the preview must END with
  // the live edge of the thought.
  R.handleData("REASONING_DELTA", { delta: "A".repeat(200), itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "B".repeat(200), itemId: "r1" });
  const pvText = pv.textContent;
  pass(pvText.length <= 200, "preview stays bounded to 200 chars (got " + pvText.length + ")");
  pass(/B+$/.test(pvText) && !pvText.includes("A"), "preview ends with the newest content (B), not the stale prefix (A)");
  pass(pvText === "B".repeat(200), "preview is exactly the newest 200 normalized chars");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: reasoning appends text nodes; preview tail is bounded");
  process.exit(0);
})();
