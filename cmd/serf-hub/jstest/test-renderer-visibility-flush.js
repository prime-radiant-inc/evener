// A visibilitychange in either direction drains a pending frame queue — a
// scheduled rAF never fires once the tab hides.
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
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
// appwire.js is not part of the renderer bundle; stub the one surface the
// live delivery path touches (the test overrides eventsFromNotification).
window.SerfAppwire = { eventsFromNotification: () => [] };
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.appwireHydrated = true;
  window.SerfAppwire.eventsFromNotification = (m, p) => m === "test/delta" ? [["ASSISTANT_TEXT_DELTA", { delta: p.delta }]] : [];
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.enqueueLiveNotification("test/delta", { delta: "x" });
  // Tab hides before the scheduled flush fires → event must still apply.
  window.document.dispatchEvent(new window.Event("visibilitychange"));
  pass(conv.querySelector(".assistant-message").textContent === "x", "hide drains the queue");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: visibilitychange drains the frame queue");
  process.exit(0);
})();
