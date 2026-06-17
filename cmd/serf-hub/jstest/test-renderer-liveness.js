// Honest liveness: while a session is actively working, a gap with no frames
// surfaces "still working · no updates for Ns" and stops the reassuring status
// dot pulse — so a hung agent never looks identical to a busy one. Frames
// (incl. the model's reasoning) reset the clock.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
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
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;
const dot = window.document.querySelector(".status-dot");

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // let the cold-load fetch flush

  // Incoming frames stamp lastFrameAt.
  R.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  pass(typeof R.lastFrameAt === "number" && R.lastFrameAt > 0, "frames stamp lastFrameAt");

  // Working + a long silent gap → honest notice, and the dot stops pulsing.
  R.state = "active";
  conv.dataset.state = "active";
  R.lastFrameAt = Date.now() - 30000; // 30s of silence
  R.refreshLiveness();
  const live = conv.parentNode.querySelector(".liveness");
  pass(live && !live.hidden, "a stale working session shows a liveness notice");
  pass(live && /no updates for/.test(live.textContent), "the notice states the gap honestly");
  pass(conv.getAttribute("data-stale") === "true", "stale flag set on the conversation");
  pass(dot && !dot.hasAttribute("data-pulse"), "reassuring dot pulse stops while stale");

  // A fresh frame clears the notice and resumes the normal pulse.
  R.lastFrameAt = Date.now();
  R.refreshLiveness();
  pass(live.hidden, "a fresh frame hides the liveness notice");
  pass(!conv.hasAttribute("data-stale"), "stale flag cleared when fresh");
  pass(dot && dot.hasAttribute("data-pulse"), "dot pulse resumes when frames flow again");

  // Idle (not working) never reads as stalled, even after a long gap.
  R.state = "idle";
  R.lastFrameAt = Date.now() - 60000;
  R.refreshLiveness();
  pass(live.hidden, "an idle session is never 'no updates' — it isn't working");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: renderer liveness is honest about silent gaps");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
