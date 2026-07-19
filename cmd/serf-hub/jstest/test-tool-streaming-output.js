// Streaming shell output updates one <pre> in place (no fold rebuild per
// delta); the fold chrome appears once at bodyEnd.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
require("./load-renderer").evalRenderer(window);
const { toolRendererFor } = window.SerfRendererInternal || {};
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const shell = (window.SerfRendererInternal.toolRendererFor || window.toolRendererFor)("shell", {});
const host = window.document.createElement("div");
const state = { body: shell.body({ command: "make test" }, host), el: host };

shell.bodyDelta(state, "line1\nline2");
const pre = state.body.pre;
pass(pre && pre.textContent === "line1\nline2", "delta writes the pre directly");
pass(!state.body.wrap.querySelector(".tool-output-more"), "no fold chrome while streaming");
pass(state.body.wrap.classList.contains("streaming"), "wrap carries .streaming for the CSS clamp");
const moreLines = Array.from({ length: 20 }, (_, i) => "l" + i).join("\n");
shell.bodyDelta(state, moreLines);
pass(!state.body.wrap.querySelector(".tool-output-more"), "still no fold after >5 lines stream in");
shell.bodyEnd(state, { tool_state: JSON.stringify({ exit_code: 0 }) }, moreLines);
pass(!state.body.wrap.classList.contains("streaming"), "bodyEnd drops .streaming");
pass(!!state.body.wrap.querySelector(".tool-output-more"), "fold chrome built once at bodyEnd");
pass(state.body.pre.textContent.split("\n").length === 5, "head shows the first 5 lines");

// Past the 8000-char budget, STREAMING keeps the live tail (a head-clip
// freezes the pane — the tail stops moving and the command looks stalled).
const marker = "HEAD-MARKER" + "y".repeat(9000) + "TAIL-MARKER";
shell.bodyDelta(state, marker);
pass(pre.textContent.length === 8000, "streaming caps the pane at 8000 chars");
pass(pre.textContent.endsWith("TAIL-MARKER"), "streaming shows the LAST 8000 chars (live tail)");
pass(!pre.textContent.startsWith("HEAD-MARKER"), "the head scrolls off while streaming");
// bodyEnd keeps the existing head-based clip + fold semantics.
shell.bodyEnd(state, { tool_state: JSON.stringify({ exit_code: 0 }) }, marker);
pass(state.body.pre.textContent.startsWith("HEAD-MARKER"), "bodyEnd keeps the head-based clip");
pass(state.body.pre.textContent.includes("…"), "bodyEnd marks the truncation");

// The read renderer shares the live-tail streaming behavior.
const read = (window.SerfRendererInternal.toolRendererFor || window.toolRendererFor)("read_file", {});
const readHost = window.document.createElement("div");
const readState = { body: read.body({ file_path: "/f" }, readHost), el: readHost };
read.bodyDelta(readState, marker);
const readPre = readState.body.outputPre;
pass(readPre.textContent.length === 8000 && readPre.textContent.endsWith("TAIL-MARKER"),
  "read streams the live tail past 8000 chars too");
read.bodyEnd(readState, { output: marker }, marker);
pass(readState.body.wrap.querySelector(".read-tool-preview").textContent.startsWith("HEAD-MARKER"),
  "read bodyEnd keeps the head-based clip");

// ── Frame-batched live deltas write the pre ONCE per frame (F4) ──────────
// Three shell deltas inside one batched flush() must coalesce through the
// per-call last-wins map and drain at settleFrame: a single full-body write
// with the final text, not one textContent replace (+ scrollHeight read) per
// delta. Probe: MutationObserver childList records on the pre (jsdom emits
// exactly one record per textContent assignment — verified).
(async () => {
  const dom2 = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const win2 = dom2.window;
  win2.marked = { parse: (t) => t };
  win2.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(win2);
  const conv2 = win2.document.getElementById("conversation");
  const R2 = win2.SerfRenderer;
  R2.init(conv2);
  win2.SerfAppwire = {
    eventsFromNotification: (m, p) => m === "test/tool-delta"
      ? [["TOOL_CALL_OUTPUT_DELTA", { call_id: p.call_id, delta: p.delta }]] : [],
  };
  await new Promise((r) => setTimeout(r, 30));
  R2.handleData("TURN_STARTED", { turnId: "t9" });
  R2.handleData("TOOL_CALL_START", { call_id: "c9", tool_name: "shell", arguments_json: JSON.stringify({ command: "make test" }) });
  const pre2 = conv2.querySelector(".tool-call.shell pre");
  pass(!!pre2, "shell body pre exists at TOOL_CALL_START");
  let writes = 0;
  const mo = new win2.MutationObserver((recs) => { for (const r of recs) if (r.type === "childList") writes++; });
  mo.observe(pre2, { childList: true });
  R2.enqueueLiveNotification("test/tool-delta", { call_id: "c9", delta: "one\n" });
  R2.enqueueLiveNotification("test/tool-delta", { call_id: "c9", delta: "two\n" });
  R2.enqueueLiveNotification("test/tool-delta", { call_id: "c9", delta: "three\n" });
  R2.flush(); // one frame: apply all three, settle once
  pass(pre2.textContent === "one\ntwo\nthree\n", "the drain writes the FINAL accumulated text (got " + JSON.stringify(pre2.textContent) + ")");
  await new Promise((r) => setTimeout(r, 0)); // deliver mutation records
  pass(writes === 1, "three deltas in one flush → the pre is written ONCE (got " + writes + " writes)");
  mo.disconnect();
  // The sync path is unchanged: handle→settleFrame drains within the same call.
  R2.handleData("TOOL_CALL_OUTPUT_DELTA", { call_id: "c9", delta: "four\n" });
  pass(pre2.textContent === "one\ntwo\nthree\nfour\n", "sync handleData drains the deferred delta in the same call");

  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: shell output streams append-only; fold builds at bodyEnd; live deltas write once per frame");
  process.exit(0);
})();
