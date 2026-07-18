// TURN_COMPLETED finalizes from textBuf (interrupt: no ASSISTANT_TEXT_END ever
// arrives). A subsequent ASSISTANT_TEXT_END for that message (codex
// turn/completed-with-items path) REPLACES the block in place — no duplicate.
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
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "partial answer" });
  // Interrupt shape: TURN_COMPLETED with NO ASSISTANT_TEXT_END.
  R.handleData("TURN_COMPLETED", { turnId: "t1", turn: { id: "t1", durationMs: 1200 } });
  let msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === 1, "TURN_COMPLETED finalizes the partial message (got " + msgs.length + ")");
  pass(msgs[0].textContent.includes("partial answer"), "partial content rendered at finalize");
  pass(msgs[0].querySelector(".turn-meta"), "turn-meta badge appended after finalize");
  // Codex shape: the synthesized END arrives AFTER TURN_COMPLETED.
  R.handleData("ASSISTANT_TEXT_END", { text: "partial answer, completed" });
  msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === 1, "late END replaces in place — no duplicate (got " + msgs.length + ")");
  pass(msgs[0].textContent.includes("completed"), "late END content applied");
  pass(msgs[0].querySelector(".turn-meta"), "turn-meta survives the in-place replace");
  pass(msgs[0].dataset.turnId === "t1", "turn id preserved through replace");
  // Mismatched completion: TURN_COMPLETED for a DIFFERENT turn must not
  // finalize the currently-streaming message — later deltas would be dropped.
  R.handleData("TURN_STARTED", { turnId: "t2" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "streaming" });
  R.handleData("TURN_COMPLETED", { turnId: "OTHER", turn: { id: "OTHER", durationMs: 500 } });
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: " continues" });
  msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === 2, "mismatched completion must not finalize — later delta still lands in the same active message (got " + msgs.length + ")");
  const live = msgs[msgs.length - 1];
  pass(live.dataset.turnId === "t2", "live message still belongs to the active turn");
  pass(live.textContent.includes("streaming continues"), "delta after mismatched completion renders into the active message");
  pass(!live.querySelector(".turn-meta"), "mismatched completion appends no .turn-meta to the live message");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: finalization is idempotent across TURN_COMPLETED and a late END");
  process.exit(0);
})();
