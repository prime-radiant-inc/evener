// Pass 7 a11y: collapse/expand disclosure buttons must expose aria-expanded so
// assistive tech announces the collapsed/expanded state. The thinking block is
// the simplest disclosure to drive; bindDisclosureToggle wires all of them
// (thinking, tool clusters, coalesced system runs) the same way.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve(""),
});
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

async function run() {
  await new Promise(r => setTimeout(r, 30));
  // Drive a reasoning stream so a thinking disclosure renders. The block is
  // a clickable disclosure from creation (collapsible even while streaming).
  window.SerfRenderer.handleData("REASONING_START", { itemId: "r1" });
  window.SerfRenderer.handleData("REASONING_DELTA", { delta: "weighing the options carefully", itemId: "r1" });
  await new Promise(r => setTimeout(r, 10));

  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  const think = conv.querySelector("button.think");
  pass(!!think, "expected a thinking disclosure button");

  // Streaming open by default → aria-expanded must be present and "true".
  pass(think && think.getAttribute("aria-expanded") === "true",
    "open thinking disclosure must expose aria-expanded=true, got " + (think && think.getAttribute("aria-expanded")));

  // Clicking collapses it → class drops open AND aria-expanded flips to false.
  if (think) {
    think.dispatchEvent(new window.Event("click"));
    pass(!think.classList.contains("open"), "click should remove .open");
    pass(think.getAttribute("aria-expanded") === "false",
      "collapsed thinking disclosure must expose aria-expanded=false, got " + think.getAttribute("aria-expanded"));
    // Clicking again expands it.
    think.dispatchEvent(new window.Event("click"));
    pass(think.getAttribute("aria-expanded") === "true",
      "re-expanded thinking disclosure must expose aria-expanded=true");
  }

  if (failures.length === 0) {
    console.log("PASS: all assertions");
    process.exit(0);
  }
  for (const f of failures) console.log(f);
  process.exit(1);
}

run();
