// Issue #37: the tool-row hover meta (.tool-meta — timestamp · runtime) must
// show REAL server times or nothing at all. The renderer used to fall back to
// the client wall clock (new Date()) when an event carried no timing, so every
// stamp was "when the browser drew the row," not when the action happened on
// the server — and on hydration of a past session every row showed page-load
// time. Server truth rides the events as startedAt/completedAt (unix seconds)
// and durationMs; the renderer must never invent a time.
//
// The meta is also laid out fixed (position: absolute, anchored top-right of
// the row) so revealing it — on hover, focus, or when the duration lands at
// tool end — never reflows the row's text.
const { JSDOM } = require("jsdom");
const fs = require("fs");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;

// Fixed server stamps (unix seconds). 2020-01-02T03:04:05Z is far from any
// plausible test-run wall clock, so a client-clock fallback cannot satisfy the
// equality assertions by coincidence.
const START_UNIX = Date.UTC(2020, 0, 2, 3, 4, 5) / 1000;
const END_UNIX = START_UNIX + 7;
const clockOf = (unix) => new Date(unix * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

const startCall = (callId, extra) => R.handleData("TOOL_CALL_START", Object.assign({
  call_id: callId,
  tool_name: "shell",
  arguments_json: JSON.stringify({ command: "echo hi" }),
}, extra));
const endCall = (callId, extra) => R.handleData("TOOL_CALL_END", Object.assign({
  call_id: callId,
  tool_name: "shell",
  output: "hi",
}, extra));
// Rows don't carry the call id in the DOM; track by creation order instead.
const metaTextAt = (i) => {
  const rows = conv.querySelectorAll(".tool-call .tool-meta");
  return rows[i] ? rows[i].textContent : null;
};

(async () => {
  // Let the cold-load /tasks fetch resolve so events render instead of buffering.
  await new Promise((r) => setTimeout(r, 30));

  // ── 1. Server-stamped start: the meta shows the SERVER time, not client now.
  startCall("c1", { startedAt: START_UNIX });
  pass(metaTextAt(0) === clockOf(START_UNIX),
    "meta shows the server-provided start time (want " + clockOf(START_UNIX) + ", got " + JSON.stringify(metaTextAt(0)) + ")");

  // Server-stamped end: clock · runtime, both from the wire.
  endCall("c1", { completedAt: END_UNIX, durationMs: 2500 });
  pass(metaTextAt(0) === clockOf(START_UNIX) + " · 2.5s",
    "meta shows server start time · server duration (got " + JSON.stringify(metaTextAt(0)) + ")");

  // ── 2. No server timing anywhere: no meta at all (never client wall clock).
  startCall("c2", {});
  pass(metaTextAt(1) === "",
    "no start timestamp from the server → empty meta, not client now (got " + JSON.stringify(metaTextAt(1)) + ")");
  endCall("c2", {});
  pass(metaTextAt(1) === "",
    "no end timestamp/duration from the server → meta stays empty (got " + JSON.stringify(metaTextAt(1)) + ")");

  // ── 3. Derived runtime from real stamps only; a same-second (0s) derived
  // span is too coarse to display honestly, so it is omitted.
  startCall("c3", { startedAt: START_UNIX });
  endCall("c3", { completedAt: START_UNIX + 3 });
  pass(metaTextAt(2) === clockOf(START_UNIX) + " · 3s",
    "runtime derived from real started/completed stamps (got " + JSON.stringify(metaTextAt(2)) + ")");
  startCall("c4", { startedAt: START_UNIX });
  endCall("c4", { completedAt: START_UNIX });
  pass(metaTextAt(3) === clockOf(START_UNIX),
    "same-second derived span is omitted, not displayed as a fake 1ms (got " + JSON.stringify(metaTextAt(3)) + ")");

  // ── 4. Layout: the meta is fixed-position (out of flow), so revealing it
  // can never reflow the row's text — on any breakpoint.
  const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
  const metaRule = css.match(/\.tool-call \.tool-meta \{([^}]*)\}/);
  pass(!!metaRule && /position:\s*absolute/.test(metaRule[1]),
    ".tool-call .tool-meta is position:absolute (out of flow, no reflow)");
  pass(/\.tool-call \{[^}]*position:\s*relative/.test(css),
    ".tool-call establishes the positioning context (position:relative)");
  pass(!/\.tool-call \.tool-meta \{\s*display:\s*none;\s*\}/.test(css),
    "no breakpoint hides the meta with display:none (display↔inline reflows text)");
  pass(!/\.tool-call:hover \.tool-meta,\s*\n?\.tool-call:focus-within \.tool-meta \{\s*display:/.test(css),
    "hover/focus reveals via opacity, never display (display toggling reflows text)");

  if (failures.length) {
    console.error(failures.join("\n"));
    process.exit(1);
  }
  console.log("ok tool meta timing");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
