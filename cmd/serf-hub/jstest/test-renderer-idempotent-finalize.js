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
  // ── Late END after an empty finalize must not target an older turn's block ──
  // Finish t2 so its block is the finalized tail.
  R.handleData("TURN_COMPLETED", { turnId: "t2", turn: { id: "t2", durationMs: 700 } });
  const t2Block = conv.querySelector('.assistant-message[data-turn-id="t2"]');
  pass(t2Block && t2Block.textContent.includes("streaming continues"), "t2 finalized as the tail block");
  // Turn t3: tool work renders at the tail, then the turn finalizes empty
  // (no assistant text) — lastFinalizedAssistantEl still points at t2's block.
  R.handleData("TURN_STARTED", { turnId: "t3" });
  // (card-mode tool: its row lands directly at the conversation tail)
  R.handleData("TOOL_CALL_START", { call_id: "c1", tool_name: "shell", arguments_json: JSON.stringify({ command: "make test" }) });
  R.handleData("TURN_COMPLETED", { turnId: "t3", turn: { id: "t3", durationMs: 100 } });
  const tail = conv.lastElementChild;
  pass(tail && tail.classList.contains("tool-call"), "tool row is the transcript tail after the empty finalize");
  // A late END with no active message must NOT replace t2's block: the block
  // being replaced is no longer the transcript tail.
  R.handleData("ASSISTANT_TEXT_END", { text: "late orphan text" });
  pass(t2Block.textContent.includes("streaming continues"), "late END did not clobber the older turn's block");
  msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs[msgs.length - 1].textContent.includes("late orphan text"), "late END lands as its own block at the tail");
  pass(msgs[msgs.length - 1] !== t2Block, "the late block is a new element, not the t2 block");
  // Empty finalize of a started-then-empty message also drops the replace pointer.
  R.handleData("TURN_STARTED", { turnId: "t4" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("TURN_COMPLETED", { turnId: "t4", turn: { id: "t4", durationMs: 50 } });
  pass(R.lastFinalizedAssistantEl === null, "empty finalize clears the replace pointer");
  // ── Late END after SESSION_END's banner must still REPLACE (F3) ────────
  // SESSION_END finalizes the stream and then appends an end banner, so the
  // finalized block is no longer conversation.lastElementChild. A replace
  // guard keyed on lastElementChild rejects the late END and appends a
  // duplicate answer.
  R.handleData("TURN_STARTED", { turnId: "t5" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "banner case answer" });
  R.handleData("TURN_COMPLETED", { turnId: "t5", turn: { id: "t5", durationMs: 900 } });
  R.handleData("SESSION_END", { reason: "interrupted" });
  const banner = conv.querySelector(".banner.note");
  pass(!!banner && /interrupted/.test(banner.textContent), "SESSION_END appended the end banner");
  pass(conv.lastElementChild === banner, "banner follows the finalized assistant block");
  const t5Block = conv.querySelector('.assistant-message[data-turn-id="t5"]');
  const countBefore = conv.querySelectorAll(".assistant-message").length;
  R.handleData("ASSISTANT_TEXT_END", { text: "banner case answer, final" });
  msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === countBefore, "late END after the banner replaces in place — no duplicate (got " + msgs.length + ")");
  pass(msgs[msgs.length - 1] === t5Block, "the SAME t5 block was replaced, not a new one appended");
  pass(t5Block.textContent.includes("final"), "late END content applied over the finalized block");
  pass(conv.querySelector(".banner.note") === banner, "end banner still present after the replace");
  pass(conv.lastElementChild === banner, "banner still follows the replaced block");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: finalization is idempotent across TURN_COMPLETED and a late END");
  process.exit(0);
})();
