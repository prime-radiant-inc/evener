// Past 4KB the message stops re-parsing: the parsed head freezes, deltas stream
// as plain text in .streaming-tail, and finalization parses everything once.
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
window.marked = { parse: (t) => { parses++; return "<p>" + t + "</p>"; } };
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
  const big = "x".repeat(4100);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: big });
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "**tail**" });
  const el = conv.querySelector(".assistant-message");
  pass(!!el, "message element exists");
  pass(el.dataset.turnId === "t1", "data-turn-id preserved in tail mode");
  const tail = el.querySelector(".streaming-tail");
  pass(!!tail, "streaming-tail node exists past 4KB");
  pass(tail && tail.textContent === "**tail**", "tail holds raw un-parsed markdown");
  pass(el.classList.contains("streaming"), "message carries .streaming for the caret");
  const before = parses;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "more" });
  pass(parses === before, "no parse per delta in tail mode");
  R.handleData("ASSISTANT_TEXT_END", { text: big + "**tail**more" });
  pass(!el.querySelector(".streaming-tail"), "finalization replaces the tail with parsed output");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: long messages stream frozen-head/raw-tail and finalize once");
  process.exit(0);
})();
