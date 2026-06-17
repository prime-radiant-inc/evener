// Live thinking (mockup #5): the thinking block is the quietest transcript
// entry. While streaming it lives in a reserved-height slot showing a one-line
// teleprompter tail (NOT a growing open body, which reflowed the prose below).
// It collapses to "Thought for Ns" with a duration tier + noun-phrase gist.
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

  // A reasoning stream opens one quiet thinking block in its reserved slot.
  R.handleData("REASONING_START", { itemId: "r1" });
  let think = conv.querySelector(".think");
  pass(!!think, "REASONING_START should create a .think block");
  // Reserved-slot collapse (mockup #5 alt A): the block streams in its
  // collapsed, fixed-height form so the prose below never reflows. It is NOT
  // open-and-growing while live.
  pass(think && !think.classList.contains("open"), "thinking block streams in its reserved slot (not open/growing)");
  pass(think && think.classList.contains("streaming"), "live thinking block carries .streaming");

  R.handleData("REASONING_DELTA", { delta: "let me ", itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "check the cache eviction logic", itemId: "r1" });
  const body = conv.querySelector(".think .think-body");
  pass(body && body.textContent === "let me check the cache eviction logic", "REASONING_DELTA should stream into .think-body");
  pass(conv.querySelectorAll(".think").length === 1, "deltas append to one block, not many");
  // The reserved-slot teleprompter tail tracks the trailing fragment.
  const pv = conv.querySelector(".think .pv");
  pass(pv && /cache eviction logic/.test(pv.textContent), "streaming preview shows the trailing fragment");

  // When the assistant starts answering, the thought collapses to a summary.
  R.handleData("ASSISTANT_TEXT_START", {});
  think = conv.querySelector(".think");
  pass(think && !think.classList.contains("open"), "thinking block stays collapsed when the assistant starts");
  pass(think && !think.classList.contains("streaming"), "collapsed block drops .streaming");
  pass(think && /Thought for/.test(think.textContent), "collapsed block reads 'Thought for …'");
  // Duration-weighted gist (mockup #5 alt D): a noun-phrase gist + a tier class.
  pass(think && /check the cache eviction logic/.test(think.textContent), "collapsed block keeps a gist of the thought");
  pass(think && /think-tier-/.test(think.className), "collapsed block carries a duration tier class");

  // An empty reasoning block (no deltas) leaves no residue.
  R.handleData("REASONING_START", { itemId: "r2" });
  R.handleData("TURN_COMPLETED", {});
  pass(conv.querySelectorAll(".think").length === 1, "empty thinking block is removed on turn complete");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: renderer live-thinking block streams reflow-free and collapses with a duration-ranked gist");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
