// Task B4 (WS2 working-state-metrics): the status row (#input-status) must
// refresh on real signals — thread status changes, turn start/completion,
// and (while a turn is actively running) a 10s freshness tick — instead of
// relying only on the old dumb "every 2s" poll. htmx's
// `serf-hub:status-refresh from:body` trigger only ever sees
// htmx.trigger(document.body, ...); a plain document.dispatchEvent() never
// reaches an `every ... from:body` listener, so every call site below must
// go through window.htmx.trigger.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="idle"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

// Stub htmx: record every trigger call as [el, name].
const triggers = [];
window.htmx = {
  trigger: (el, name) => { triggers.push([el, name]); },
};

// Intercept setInterval/clearInterval so the 10s active-only tick can be
// driven synchronously (invoke its callback directly) instead of waiting on
// real wall-clock time, and so "cleared on leaving active" is observable.
let nextTimerId = 1;
const timers = new Map(); // id -> { fn, ms, cleared }
window.setInterval = (fn, ms) => {
  const id = nextTimerId++;
  timers.set(id, { fn, ms, cleared: false });
  return id;
};
window.clearInterval = (id) => {
  const t = timers.get(id);
  if (t) t.cleared = true;
};

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;

function triggeredStatusRefresh() {
  return triggers.some(t => t[0] === window.document.body && t[1] === "serf-hub:status-refresh");
}

// The most recently created, still-running 10s interval (ms === 10000).
function activeRefreshTimer() {
  let found = null;
  for (const t of timers.values()) {
    if (t.ms === 10000 && !t.cleared) found = t;
  }
  return found;
}

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // let the cold-load /tasks fetch flush

  // ── THREAD_STATUS_CHANGED dispatches a refresh, and entering "active" ────
  // starts the 10s tick.
  triggers.length = 0;
  R.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  pass(triggeredStatusRefresh(),
    "THREAD_STATUS_CHANGED should htmx.trigger(document.body, 'serf-hub:status-refresh')");

  const tick = activeRefreshTimer();
  pass(!!tick, "entering the active state should start a 10s active-refresh interval");
  if (tick) {
    triggers.length = 0;
    tick.fn();
    pass(triggeredStatusRefresh(),
      "the 10s active tick should fire serf-hub:status-refresh while a turn is active");
  }

  // Leaving active must clear that interval — the tick must not run once idle.
  triggers.length = 0;
  R.handleData("THREAD_STATUS_CHANGED", { status: "idle" });
  pass(!!tick && tick.cleared, "leaving the active state should clear the 10s active-refresh interval");
  pass(!activeRefreshTimer(), "no active-refresh interval should be running once idle");

  // ── TURN_STARTED dispatches a refresh ─────────────────────────────────────
  triggers.length = 0;
  R.handleData("TURN_STARTED", { turnId: "turn_1" });
  pass(triggeredStatusRefresh(),
    "TURN_STARTED should htmx.trigger(document.body, 'serf-hub:status-refresh')");

  // ── TURN_COMPLETED dispatches a refresh ───────────────────────────────────
  triggers.length = 0;
  R.handleData("TURN_COMPLETED", { turnId: "turn_1" });
  pass(triggeredStatusRefresh(),
    "TURN_COMPLETED should htmx.trigger(document.body, 'serf-hub:status-refresh')");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: status row refresh is event-driven (dispatch + 10s active tick)");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
