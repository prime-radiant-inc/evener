// Regression: Serf transcripts store tool calls and tool results as adjacent
// entries. AppWire replay must render them as one tool card, not a start row
// followed by a separate result row with no target.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const rendererSrc = fs.readFileSync(path.resolve(__dirname, "../assets/renderer.js"), "utf8");
const threadFixture = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../../../internal/appwire/testdata/tool-groups-thread.json"), "utf8"));

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
window.eval(rendererSrc);

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
  const shellTools = conv.querySelectorAll(".tool-call.shell");
  const readTools = conv.querySelectorAll(".tool-call.read_file");
  const shellOutput = conv.querySelector(".shell-output");
  const assistantMessages = Array.from(conv.querySelectorAll(".assistant-message")).map((el) => el.textContent);
  pass(shellTools.length === 1, "expected one shell card, got " + shellTools.length);
  pass(readTools.length === 1, "expected one failed read_file card, got " + readTools.length);
  if (shellTools[0]) {
    pass(shellTools[0].textContent.includes("printf 'alpha\nbeta\n'"), "shell target was not preserved");
    pass(shellOutput && shellTools[0].contains(shellOutput), "shell output did not attach to shell card");
    pass(shellOutput && shellOutput.textContent.includes("alpha\nbeta\n"), "split shell result did not attach to original card");
  }
  if (readTools[0]) {
    pass(readTools[0].textContent.includes("/tmp/does-not-exist"), "failed tool target was not preserved");
    pass(readTools[0].querySelector(".result-bad"), "failed tool did not render as an error result");
  }
  pass(assistantMessages.some((text) => text.includes("I need one more check.")), "interleaved assistant text was lost");

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }

  console.log("PASS: appwire replay pairs split tool calls and results");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
