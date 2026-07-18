// Live notifications batch: N deliveries before a flush apply together with a
// single settle; flush() drains synchronously for tests; a transcript reset
// invalidates queued events.
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
  R.appwireHydrated = true; // live mode: deliveries batch
  pass(typeof R.flush === "function", "flush() exists");
  pass(Array.isArray(R.frameQueue), "frameQueue exists");

  // Simulate the live delivery path with a stubbed projector.
  const realEvents = window.SerfAppwire.eventsFromNotification;
  window.SerfAppwire.eventsFromNotification = (method, params) => {
    if (method === "test/delta") return [["ASSISTANT_TEXT_DELTA", { delta: params.delta }]];
    return realEvents ? realEvents(method, params) : [];
  };
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  // Queue three deltas via the internal enqueue used by deliverNotification.
  R.enqueueLiveNotification("test/delta", { delta: "a" });
  R.enqueueLiveNotification("test/delta", { delta: "b" });
  R.enqueueLiveNotification("test/delta", { delta: "c" });
  pass(conv.querySelector(".assistant-message").textContent === "", "nothing renders before flush");
  R.flush();
  pass(conv.querySelector(".assistant-message").textContent === "abc", "flush applies all queued events");

  // Generation guard: queued events from before a reset never land.
  R.enqueueLiveNotification("test/delta", { delta: "STALE" });
  R.resetTranscriptReplay();
  R.flush();
  pass(!conv.textContent.includes("STALE"), "reset invalidates the queue");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: live notifications batch per frame; reset invalidates the queue");
  process.exit(0);
})();
