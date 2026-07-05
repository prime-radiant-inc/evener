// When an active session goes silent past the stall threshold, the renderer
// should self-heal by re-subscribing/re-hydrating (connectAppwire) — not just
// paint a warning. This recovers a silently-stalled subscription where the
// browser↔hub socket is healthy but no thread frames flow. It must fire once
// per concern episode and re-arm after recovery.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="active"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <div id="input-status" class="input-status">
    <span class="status-item liveness-inline" data-liveness hidden></span>
  </div>
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
  await new Promise((r) => setTimeout(r, 30));

  // Presence of SerfAppwire gates the self-heal; the renderer only needs it to
  // exist (set after init so init doesn't run the real connectAppwire).
  window.SerfAppwire = { refForSession: (id) => "local:" + id };

  // Stand in a live subscription and spy on the re-hydrate path.
  let healCalls = 0;
  R.connectAppwire = () => { healCalls++; };
  R.liveStream = { close() {} };
  R.sessionId = "01TEST";
  R.state = "active";
  conv.dataset.state = "active";

  // The self-heal coupling below only means something if it's driven off the
  // inline status-row span (WS2 B5) rather than the old standalone sibling.
  pass(R.livenessEl && R.livenessEl.matches("[data-liveness]"), "refreshLiveness is driven off the inline status-row span");

  // Calm-quiet gap: no self-heal yet.
  R.lastFrameAt = Date.now() - 30000;
  R.refreshLiveness();
  pass(healCalls === 0, "calm-quiet band does not self-heal");

  // Cross into concern: heal exactly once.
  R.lastFrameAt = Date.now() - 200000;
  R.refreshLiveness();
  pass(healCalls === 1, "entering concern triggers one self-heal (got " + healCalls + ")");

  // Still concern on the next tick: must not re-fire.
  R.refreshLiveness();
  pass(healCalls === 1, "staying in concern does not re-fire self-heal (got " + healCalls + ")");

  // Recovery (a fresh frame) then a new silence: re-arms and heals again.
  R.lastFrameAt = Date.now();
  R.refreshLiveness();
  R.lastFrameAt = Date.now() - 200000;
  R.refreshLiveness();
  pass(healCalls === 2, "a new concern episode after recovery heals again (got " + healCalls + ")");

  // Without a live stream, no self-heal (e.g. viewing a past session).
  R.liveStream = null;
  R.lastFrameAt = Date.now();
  R.refreshLiveness();
  R.lastFrameAt = Date.now() - 200000;
  R.refreshLiveness();
  pass(healCalls === 2, "no self-heal without an active live stream (got " + healCalls + ")");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: renderer self-heals a stalled subscription on the concern band");
  process.exit(0);
})();
