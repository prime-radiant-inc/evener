// Scroll behavior: the transcript sticks to the bottom only when the reader is
// already there. If they've scrolled up to read history, incoming frames must
// not yank the viewport back down.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
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
// jsdom has no layout, so fake the scroll metrics. scrollTop is writable.
Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1000 });
Object.defineProperty(conv, "clientHeight", { configurable: true, get: () => 400 });

window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // flush the cold-load buffer

  // Reader has scrolled up (far from bottom: 1000 - 100 - 400 = 500 > 50).
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "streaming while you read history" });
  pass(conv.scrollTop === 100, "frames must not yank the viewport when scrolled up (got " + conv.scrollTop + ")");

  // Reader is at the bottom (1000 - 600 - 400 = 0 < 50): new frames stick.
  conv.scrollTop = 600;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: " more" });
  pass(conv.scrollTop === 1000, "frames stick to the bottom when the reader is already there (got " + conv.scrollTop + ")");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: transcript sticks to bottom only when already at bottom");
  process.exit(0);
})();
