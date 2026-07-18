// A communicate tool call landing while its agentMessage is still streaming
// (raw tail mode) must be deduped against the SOURCE buffer, not rendered text.
//
// Case 2 is the regression the renderer fix exists for: a delta arriving
// AFTER the 4096-char crossing lives in the raw `.streaming-tail` as
// unparsed markdown ("**bold tail**"), so the old rendered-DOM comparison
// (last.textContent vs rendered candidate) can never match — the tail keeps
// the asterisks while the rendered candidate drops them.
const { JSDOM } = require("jsdom");

function makeRenderer() {
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
  // Mimic real markdown closely enough for this test: **bold** becomes
  // <strong>bold</strong>, so rendered textContent drops the asterisks while
  // a raw streaming tail keeps them.
  window.marked = { parse: (t) => "<p>" + String(t).replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") + "</p>" };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { R: window.SerfRenderer, conv };
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  // Case 1: a single delta that crosses the 4KB threshold. The crossing delta
  // itself is rendered into the frozen head, so the tail is empty.
  {
    const { R } = makeRenderer();
    await new Promise((r) => setTimeout(r, 30));
    R.handleData("TURN_STARTED", { turnId: "t1" });
    R.handleData("ASSISTANT_TEXT_START", {});
    const text = "x".repeat(4100) + " **bold tail**";
    R.handleData("ASSISTANT_TEXT_DELTA", { delta: text }); // switches to raw-tail mode
    pass(R.lastElementIsAssistantText(text) === true,
      "case 1: dedup matches the streaming message by source, raw tail included");
  }

  // Case 2: a post-crossing delta lands in the raw .streaming-tail as
  // unparsed markdown. The old rendered-DOM comparison returns false here
  // (tail keeps "**", rendered candidate drops them); source-buffer dedup
  // must still match.
  {
    const { R, conv } = makeRenderer();
    await new Promise((r) => setTimeout(r, 30));
    R.handleData("TURN_STARTED", { turnId: "t1" });
    R.handleData("ASSISTANT_TEXT_START", {});
    R.handleData("ASSISTANT_TEXT_DELTA", { delta: "x".repeat(4100) }); // crosses -> frozen head + tail span
    R.handleData("ASSISTANT_TEXT_DELTA", { delta: " **bold tail**" }); // streams raw into the tail
    const tail = conv.querySelector(".streaming-tail");
    pass(!!tail, "case 2: a raw .streaming-tail exists after the post-crossing delta");
    pass(tail && tail.textContent.includes("**bold tail**"),
      "case 2: the tail holds unparsed markdown (asterisks intact)");
    pass(R.lastElementIsAssistantText("x".repeat(4100) + " **bold tail**") === true,
      "case 2: dedup matches by source buffer even with a raw markdown tail");
  }

  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: communicate dedup compares against the source buffer while streaming");
  process.exit(0);
})();
