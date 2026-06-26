// Regression: reasoning (thinking) blocks vanish on page reload because
// eventsFromItem in appwire.js did not handle "reasoning" type items.
// When a thread is hydrated from the server, reasoning items were silently
// dropped, so the rendered conversation lost all previous thinking blocks.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const threadFixture = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../../../appwire/testdata/reasoning-thread.json"), "utf8"));

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
window.eval(appwireSrc);
window.SerfAppwire.tasks = () => Promise.resolve([]);
require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

async function run() {
  await new Promise((resolve) => setTimeout(resolve, 30));

  const events = window.SerfAppwire.eventsFromThread(threadFixture);

  for (const [kind, data] of events) {
    window.SerfRenderer.handleData(kind, data);
  }

  const failures = [];
  const pass = (condition, message) => { if (!condition) failures.push("FAIL: " + message); };

  const thinks = conv.querySelectorAll(".think");
  pass(thinks.length === 1, "expected one thinking block, got " + thinks.length);
  if (thinks[0]) {
    pass(thinks[0].textContent.includes("Let me break this down"), "thinking block should contain reasoning text");
    pass(!thinks[0].classList.contains("streaming"), "hydrated thinking block should not be streaming");
    pass(!thinks[0].classList.contains("open"), "hydrated thinking block should be collapsed");
    pass(/Thought for/.test(thinks[0].textContent), "collapsed block should read 'Thought for …'");
  }

  const assistantMessages = conv.querySelectorAll(".assistant-message");
  pass(assistantMessages.length === 1, "expected one assistant message, got " + assistantMessages.length);

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }

  console.log("PASS: reasoning blocks survive page reload via thread hydration");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
