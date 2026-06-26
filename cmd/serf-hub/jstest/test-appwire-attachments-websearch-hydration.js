// Regression: non-image input attachments (audio, documents) and
// provider-native web_search content must survive thread hydration on reload.
// Audio/documents render as labeled file chips; web_search renders through the
// existing web_search tool card.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
const threadFixture = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../../../appwire/testdata/attachments-websearch-thread.json"), "utf8"));

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

  const chips = conv.querySelectorAll(".user-message-attachment");
  pass(chips.length === 2, "expected two attachment chips, got " + chips.length);
  if (chips.length === 2) {
    pass(/report\.pdf/.test(chips[0].textContent), "first chip should name the document");
    pass(/audio\/wav|audio/.test(chips[1].textContent), "second chip should label the audio");
  }
  // Attachments must not be rendered as broken images.
  pass(conv.querySelectorAll(".user-image-card img").length === 0, "attachments should not render as images");

  const search = conv.querySelector(".tool-call.web_search");
  pass(!!search, "web_search content should render as a web_search card");
  if (search) {
    pass(/go context cancellation/.test(search.textContent), "web_search card should show the query");
    pass(/go\.dev\/ctx/.test(search.textContent), "web_search card should show a result");
  }

  if (failures.length > 0) {
    console.log("Rendered conversation HTML:");
    console.log(conv.innerHTML);
    console.log("");
    for (const failure of failures) console.log(failure);
    process.exit(1);
  }

  console.log("PASS: attachments + web_search survive thread hydration");
  process.exit(0);
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
