// Live thinking: REASONING_START opens a quiet collapsible "thinking" block
// that streams the reasoning summary; it collapses to "Thought for Ns" when
// the assistant starts answering or the turn completes.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;

(async () => {
  // Let the cold-load /tasks fetch resolve so events render instead of buffering.
  await new Promise((r) => setTimeout(r, 30));

  // A reasoning stream opens one quiet thinking block and streams the summary.
  R.handleData("REASONING_START", { itemId: "r1" });
  let think = conv.querySelector(".think");
  pass(!!think, "REASONING_START should create a .think block");
  pass(think && think.classList.contains("open"), "thinking block streams open while live");

  R.handleData("REASONING_DELTA", { delta: "let me ", itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "check the cache", itemId: "r1" });
  const body = conv.querySelector(".think .think-body");
  pass(body && body.textContent === "let me check the cache", "REASONING_DELTA should stream into .think-body");
  pass(conv.querySelectorAll(".think").length === 1, "deltas append to one block, not many");

  // When the assistant starts answering, the thought collapses to a summary.
  R.handleData("ASSISTANT_TEXT_START", {});
  think = conv.querySelector(".think");
  pass(think && !think.classList.contains("open"), "thinking block collapses when the assistant starts");
  pass(think && /Thought for/.test(think.textContent), "collapsed block reads 'Thought for …'");
  pass(think && /check the cache/.test(think.textContent), "collapsed block keeps a preview of the thought");

  // An empty reasoning block (no deltas) leaves no residue.
  R.handleData("REASONING_START", { itemId: "r2" });
  R.handleData("TURN_COMPLETED", {});
  pass(conv.querySelectorAll(".think").length === 1, "empty thinking block is removed on turn complete");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: renderer live-thinking block streams and collapses");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
