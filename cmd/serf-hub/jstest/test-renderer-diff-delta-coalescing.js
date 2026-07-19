// Diff renderers (edit/write/apply_patch) rebuild the whole diff DOM per
// bodyDelta call, so output deltas for one diff tool inside a single batched
// flush must coalesce to ONE render per frame (last output wins), drained at
// settleFrame. Cheap single-textContent renderers (shell) still stream per
// delta, and a pending coalesced delta must never clobber bodyEnd's final
// render at TOOL_CALL_END.
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
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("TOOL_CALL_START", { call_id: "w1", tool_name: "write_file", arguments_json: JSON.stringify({ file_path: "/a.js", content: "x" }) });

  // Spy on the write renderer's bodyDelta (the renderDiff-based path).
  const writeRenderer = window.SerfRendererInternal.toolRendererFor("write_file");
  let renders = 0;
  const realBodyDelta = writeRenderer.bodyDelta;
  writeRenderer.bodyDelta = (state, out) => { renders++; return realBodyDelta(state, out); };

  // Two deltas inside one frame: applyEvent directly, settleFrame once
  // (mirrors what the batched live flush does per frame).
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "w1", delta: "+line1\n" });
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "w1", delta: "+line2\n" });
  pass(renders === 0, "no diff render before the frame settles (got " + renders + ")");
  R.settleFrame(false, conv.children.length);
  pass(renders === 1, "two deltas in one flush coalesce to one diff render (got " + renders + ")");
  const pre = conv.querySelector(".write-body pre.diff-body");
  pass(pre && pre.textContent.includes("+line1") && pre.textContent.includes("+line2"),
    "the drain renders the full accumulated output");

  // The next frame renders again — coalescing is per frame, not a latch.
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "w1", delta: "+line3\n" });
  R.settleFrame(false, conv.children.length);
  pass(renders === 2, "next frame renders the next delta batch (got " + renders + ")");
  pass(pre.textContent.includes("+line3"), "second drain applied");

  // A still-pending coalesced delta must not clobber bodyEnd's authoritative
  // final render when TOOL_CALL_END lands in the same frame.
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "w1", delta: "+stale-pending\n" });
  R.handleData("TOOL_CALL_END", { call_id: "w1", tool_name: "write_file", output: "+final\n" });
  pass(renders === 2, "pending delta dropped at finalize — no stale re-render at settle (got " + renders + ")");
  pass(pre.textContent.includes("+final") && !pre.textContent.includes("stale-pending"),
    "bodyEnd's final output stands after the settle");

  // Cheap single-textContent renderers are untouched: shell still streams per delta.
  R.handleData("TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "make" }) });
  const shellRenderer = window.SerfRendererInternal.toolRendererFor("shell");
  let shellRenders = 0;
  const realShellDelta = shellRenderer.bodyDelta;
  shellRenderer.bodyDelta = (state, out) => { shellRenders++; return realShellDelta(state, out); };
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "s1", delta: "a" });
  R.applyEvent("TOOL_CALL_OUTPUT_DELTA", { call_id: "s1", delta: "b" });
  pass(shellRenders === 2, "shell bodyDelta stays per-delta, not coalesced (got " + shellRenders + ")");

  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: diff output deltas coalesce to one render per frame; cheap renderers unaffected");
  process.exit(0);
})();
