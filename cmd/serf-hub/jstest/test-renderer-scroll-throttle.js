// Scroll handling is rAF-throttled (N events → one handler run) and error
// anchors are cached between invalidations.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  pass(typeof R.onScrollAffordance === "function", "onScrollAffordance exists");
  pass(typeof R.errorAnchors === "function", "errorAnchors exists");
  // Throttle: 5 scroll events in one tick → 1 handler run.
  let runs = 0;
  const real = R.onScrollAffordance.bind(R);
  R.onScrollAffordance = () => { runs++; real(); };
  for (let i = 0; i < 5; i++) conv.dispatchEvent(new window.Event("scroll"));
  pass(runs <= 1, "scroll handler coalesced to at most one run per frame (got " + runs + ")");
  await new Promise((r) => setTimeout(r, 50));
  pass(runs === 1, "the coalesced run executes on the next frame");
  // Regression: a same-tick scheduleFrame on another key (prepend-settle) must
  // not cancel the scroll tick's pending frame — before keyed slots it did,
  // leaving scrollAffordanceTick stuck true and killing scroll affordance.
  runs = 0;
  conv.dispatchEvent(new window.Event("scroll"));
  R.scheduleFrame(() => {}, "prepend-settle");
  await new Promise((r) => setTimeout(r, 50));
  pass(runs === 1, "scroll tick frame survives a prepend-settle scheduleFrame (got " + runs + ")");
  // Anchor cache: querySelectorAll runs once until invalidated.
  let queries = 0;
  const realQSA = conv.querySelectorAll.bind(conv);
  conv.querySelectorAll = (sel) => { if (sel.includes('data-attention="error"')) queries++; return realQSA(sel); };
  R.errorAnchors();
  R.errorAnchors();
  pass(queries === 1, "error anchors cached between calls (got " + queries + ")");
  R.handleData("TOOL_CALL_END", { callId: "nope" });
  R.errorAnchors();
  pass(queries === 2, "TOOL_CALL_END invalidates the cache");
  // Session swap: init() against a fresh conversation element must invalidate
  // the anchor cache — session A's anchors are detached after the swap and
  // must not produce a phantom error pill on session B.
  const anchor = window.document.createElement("div");
  anchor.className = "tool-call";
  anchor.dataset.attention = "error";
  conv.appendChild(anchor);
  R.errorAnchorCache = null; // a real error row invalidates via TOOL_CALL_END
  pass(R.errorAnchors().length === 1, "anchor cache populated from session A");
  // F6: the dirty-assistant set is the same per-conversation hazard.
  R.dirtyAssistantMessages = new Set([{ el: anchor, textBuf: "stale" }]);
  const convB = window.document.createElement("div");
  convB.id = "conversation";
  convB.dataset.sessionId = "01TESTB";
  convB.dataset.state = "idle";
  conv.replaceWith(convB);
  R.init(convB);
  pass(R.dirtyAssistantMessages.size === 0, "init clears dirtyAssistantMessages on session swap");
  let queriesB = 0;
  const realQSB = convB.querySelectorAll.bind(convB);
  convB.querySelectorAll = (sel) => { if (sel.includes('data-attention="error"')) queriesB++; return realQSB(sel); };
  const anchorsB = R.errorAnchors();
  pass(queriesB === 1, "errorAnchors() re-queries after session swap, no stale cache (got " + queriesB + ")");
  pass(anchorsB.length === 0, "no detached anchors from session A leak into session B");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scroll handler throttled; error anchors cached with invalidation");
  process.exit(0);
})();
