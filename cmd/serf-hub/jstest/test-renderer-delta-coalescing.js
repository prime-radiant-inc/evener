// Assistant deltas must coalesce to at most one marked.parse per settle, not
// one parse per delta event.
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
let parses = 0;
window.marked = { parse: (t) => { parses++; return t; } };
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
  parses = 0;
  // Direct internal call: five deltas, one settle (mirrors what the batched
  // flush does per frame; handleData settles per call by design).
  R.appendAssistantDelta("a");
  R.appendAssistantDelta("b");
  R.appendAssistantDelta("c");
  R.appendAssistantDelta("d");
  R.appendAssistantDelta("e");
  pass(parses === 0, "no parse before settle (got " + parses + ")");
  R.settleFrame(false, conv.children.length);
  pass(parses === 1, "exactly one parse per settle for N deltas (got " + parses + ")");
  const el = conv.querySelector(".assistant-message");
  pass(el && el.textContent === "abcde", "coalesced content renders");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: assistant deltas coalesce to one parse per settle");
  process.exit(0);
})();
