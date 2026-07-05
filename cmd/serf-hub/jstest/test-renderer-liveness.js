// Honest liveness: while a session is actively working, a gap with no frames
// surfaces "still working · no updates for Ns" and stops the reassuring status
// dot pulse — so a hung agent never looks identical to a busy one. Frames
// (incl. the model's reasoning) reset the clock. The liveness line renders in
// the status row's inline [data-liveness] span (WS2 B5), not a standalone
// element beside the transcript.
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
const dot = window.document.querySelector(".status-dot");

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // let the cold-load fetch flush

  // Incoming frames stamp lastFrameAt.
  // Pin lastFrameAt to a known past value so the assertion cannot pass trivially
  // from the value set during init — if handle() stops updating it, the clock
  // will still be 10s in the past after the call and the assertion fails.
  R.lastFrameAt = Date.now() - 10000;
  const tBefore = Date.now();
  R.handleData("THREAD_STATUS_CHANGED", { status: "active" });
  const tAfter = Date.now();
  pass(R.lastFrameAt >= tBefore && R.lastFrameAt <= tAfter, "frames stamp lastFrameAt");

  // Working + a long silent gap past the stall threshold → honest stall notice,
  // and the dot stops pulsing.
  R.state = "active";
  conv.dataset.state = "active";
  R.lastFrameAt = Date.now() - 200000; // 200s of silence ≥ stall threshold
  R.refreshLiveness();
  const live = window.document.querySelector("#input-status [data-liveness]");
  pass(live && !live.hidden, "a stalled working session shows a liveness notice");
  pass(live && /no updates for/.test(live.textContent), "the notice states the gap honestly");
  pass(conv.getAttribute("data-stalled") === "true", "stalled flag set on the conversation");
  pass(dot && !dot.hasAttribute("data-pulse"), "reassuring dot pulse stops while stalled");

  // A fresh frame clears the notice and resumes the normal pulse.
  R.lastFrameAt = Date.now();
  R.refreshLiveness();
  pass(live.hidden, "a fresh frame hides the liveness notice");
  pass(!conv.hasAttribute("data-stalled"), "stalled flag cleared when fresh");
  pass(dot && dot.hasAttribute("data-pulse"), "dot pulse resumes when frames flow again");

  // Idle (not working) never reads as stalled, even after a long gap.
  R.state = "idle";
  R.lastFrameAt = Date.now() - 60000;
  R.refreshLiveness();
  pass(live.hidden, "an idle session is never 'no updates' — it isn't working");

  // ── Calm → concern escalation (mockup #13) ───────────────────────────────
  // Calm-quiet band (QUIET ≤ gap < STALL): a coarse quantized bucket, NOT a
  // rising per-second counter; the breathing dot keeps breathing.
  R.state = "active";
  conv.dataset.state = "active";
  R.lastFrameAt = Date.now() - 32000; // 32s of silence → "~30s" bucket
  R.refreshLiveness();
  pass(live && !live.hidden, "calm-quiet band shows the liveness line");
  pass(live && /quiet ~30s/.test(live.textContent), "calm-quiet shows a quantized bucket (got " + (live && live.textContent) + ")");
  pass(live && !/32s/.test(live.textContent), "calm-quiet shows no exact second count (got " + (live && live.textContent) + ")");
  pass(!conv.hasAttribute("data-stalled"), "calm-quiet does not raise the stalled flag");
  pass(dot && dot.hasAttribute("data-pulse"), "the breathing pulse survives the calm-quiet band");

  // A longer-but-still-calm gap rolls to the ~1m bucket.
  R.lastFrameAt = Date.now() - 70000; // 70s → "~1m"
  R.refreshLiveness();
  pass(live && /quiet ~1m/.test(live.textContent), "calm-quiet rolls to a coarser bucket (got " + (live && live.textContent) + ")");
  pass(dot && dot.hasAttribute("data-pulse"), "the breathing pulse still survives a longer calm gap");

  // Concern band (gap ≥ STALL): escalate to amber + glyph, drop the pulse.
  R.lastFrameAt = Date.now() - 200000; // 200s ≥ 180s stall threshold
  R.refreshLiveness();
  pass(live && !live.hidden, "concern band shows the liveness line");
  pass(live && /may be stalled/.test(live.textContent), "concern wording warns of a possible stall (got " + (live && live.textContent) + ")");
  pass(live && live.dataset.level === "concern", "concern band marks the line for amber styling");
  pass(live && live.querySelector(".liveness-glyph"), "concern band is glyph-paired (colorblind-safe)");
  pass(conv.getAttribute("data-stalled") === "true", "concern band raises the stalled flag");
  pass(dot && !dot.hasAttribute("data-pulse"), "the pulse drops at the stall threshold (honest: not alive)");

  // Recovery: a fresh frame clears concern and resumes the pulse.
  R.lastFrameAt = Date.now();
  R.refreshLiveness();
  pass(live.hidden, "a fresh frame clears the concern band");
  pass(!conv.hasAttribute("data-stalled"), "the stalled flag clears on recovery");
  pass(dot && dot.hasAttribute("data-pulse"), "the pulse resumes after recovery");

  // ── afterSwap re-bind (WS2 B5) ───────────────────────────────────────────
  // #input-status is an htmx innerHTML-swap target (task B4's status-row
  // refresh): every swap detaches whatever [data-liveness] node livenessEl
  // was pointing at and mounts a structurally-identical new one. The renderer
  // must re-acquire a fresh handle on htmx:afterSwap rather than keep writing
  // into the node htmx just orphaned, and must not throw or repaint a stale
  // handle in the window before that listener fires.
  const statusRow = window.document.getElementById("input-status");
  const staleEl = R.livenessEl;
  pass(staleEl === live, "livenessEl starts out pointing at the mounted inline span");

  // Simulate htmx swapping the row's innerHTML: the current span is detached
  // and a fresh, otherwise-identical span takes its place.
  statusRow.innerHTML = '<span class="status-item liveness-inline" data-liveness hidden></span>';
  pass(!staleEl.isConnected, "the pre-swap span is detached once the row's innerHTML is replaced");

  // Before any afterSwap listener runs, refreshLiveness must no-op on the now-
  // stale handle rather than throw or repaint a node no one can see.
  R.livenessEl = staleEl;
  R.state = "active";
  R.lastFrameAt = Date.now() - 200000; // deep in the concern band
  R.refreshLiveness();
  pass(staleEl.hidden === true, "refreshLiveness does not write into a detached livenessEl");

  // htmx:afterSwap targeting #input-status re-acquires the fresh span.
  statusRow.dispatchEvent(new window.Event("htmx:afterSwap", { bubbles: true }));
  const freshEl = window.document.querySelector("#input-status [data-liveness]");
  pass(R.livenessEl === freshEl, "htmx:afterSwap on #input-status re-acquires the fresh span");
  pass(R.livenessEl !== staleEl, "the re-acquired span is not the stale pre-swap node");

  // The next tick writes into the freshly re-acquired node.
  R.refreshLiveness();
  pass(freshEl && !freshEl.hidden && /may be stalled/.test(freshEl.textContent),
    "refreshLiveness writes into the freshly re-acquired span");

  // An afterSwap on an unrelated target must not disturb the current handle
  // (mirrors sidebar.js's e.target.id-scoped resync — only #input-status
  // swaps trigger a re-acquire).
  const handleBeforeUnrelatedSwap = R.livenessEl;
  conv.dispatchEvent(new window.Event("htmx:afterSwap", { bubbles: true }));
  pass(R.livenessEl === handleBeforeUnrelatedSwap, "an afterSwap on an unrelated target does not re-acquire livenessEl");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: renderer liveness is honest about silent gaps");
  process.exit(0); // the renderer's pollers keep the event loop alive otherwise
})();
