// Subagent surfacing: the aggregated "Subagents (N)" module.
//
// Spawned subagents (via delegate / serf/job notifications) aggregate into ONE
// inline module per fan-out cluster. Each row: status glyph · name · last
// action / result · duration · always-visible (dim) "view →" link into the
// subagent's transcript. The header tallies running / done / failed. A child
// that FAILED-TO-RUN surfaces in the error color at the module level — never
// averaged into a "N done" count.
//
// The stale-"running" fix: a subagent's completion reconciles from a SUCCESSFUL
// job_read_output / job_list / job_send_message for that job id, not only from a
// JOB_FINISHED event (which often never arrives).
const fs = require("fs");
const { JSDOM } = require("jsdom");

const STYLE_PATH = "../assets/style.css";
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
async function scenario(name, eventSeq, check) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  for (const [t, d] of eventSeq) window.SerfRenderer.handleData(t, d);
  await new Promise(r => setTimeout(r, 10));
  const result = check({ conv, window });
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  HTML: " + conv.innerHTML);
  }
}

function spawnDelegate(callId, jobId, task, transcriptRef) {
  return [
    ["TOOL_CALL_START", { call_id: callId, tool_name: "delegate", arguments_json: JSON.stringify({ task }) }],
    ["TOOL_CALL_END", { call_id: callId, tool_name: "delegate", output: JSON.stringify({
      job_id: jobId, type: "delegate", status: "running", transcript_ref: transcriptRef, task,
    }) }],
  ];
}

(async () => {

await scenario("one module aggregates multiple spawned subagents", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "trace callers", "local:child-A"),
  ...spawnDelegate("d2", "job_B", "find tests", "local:child-B"),
], ({ conv }) => {
  const modules = conv.querySelectorAll(".subs");
  if (modules.length !== 1) return { ok: false, detail: "expected exactly one .subs module, got " + modules.length };
  const mod = modules[0];
  const rows = mod.querySelectorAll(".sub-r");
  if (rows.length !== 2) return { ok: false, detail: "expected 2 subagent rows, got " + rows.length };
  // No standalone job-ref rows leak outside the module.
  const standalone = conv.querySelector(".subagent-reference");
  if (standalone) return { ok: false, detail: "standalone .subagent-reference leaked outside module" };
  const header = mod.querySelector(".subs-h");
  if (!header || !header.textContent.includes("Subagents")) return { ok: false, detail: "missing module header" };
  if (!header.textContent.includes("2")) return { ok: false, detail: "header should show count of 2: " + header.textContent };
  return { ok: true };
});

await scenario("each row shows name, running state, and a view link to the transcript", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "trace callers", "local:child-A"),
], ({ conv }) => {
  const row = conv.querySelector(".subs .sub-r");
  if (!row) return { ok: false, detail: "missing subagent row" };
  if (row.dataset.jobId !== "job_A") return { ok: false, detail: "row missing jobId dataset" };
  const name = row.querySelector(".nm");
  if (!name || !name.textContent.includes("trace callers")) return { ok: false, detail: "row missing name/label: " + (name && name.textContent) };
  // glyph pairs with the running (blue) status
  const glyph = row.querySelector(".g");
  if (!glyph || !glyph.classList.contains("run")) return { ok: false, detail: "running row should carry .g.run glyph" };
  // view link present and always visible (not opacity:0 in default state)
  const link = row.querySelector(".lk");
  if (!link) return { ok: false, detail: "missing view link" };
  if (!/view/.test(link.textContent)) return { ok: false, detail: "view link text missing: " + link.textContent };
  if (row.dataset.transcriptRef !== "local:child-A") return { ok: false, detail: "row missing transcriptRef dataset" };
  // The row is keyboard-reachable and clickable.
  if (row.tagName !== "BUTTON" && row.tagName !== "A") {
    return { ok: false, detail: "row must be a real interactive element, was " + row.tagName };
  }
  return { ok: true };
});

await scenario("a successful job_read_output flips a stale running subagent to done", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "trace callers", "local:child-A"),
  // No JOB_FINISHED ever arrives — only the agent reading the job's output.
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: JSON.stringify({
    job_id: "job_A", type: "delegate", status: "completed", content: "found 7 call sites", total_bytes: 18,
  }) }],
], ({ conv }) => {
  const row = conv.querySelector('.subs .sub-r[data-job-id="job_A"]');
  if (!row) return { ok: false, detail: "missing subagent row" };
  const glyph = row.querySelector(".g");
  if (!glyph || glyph.classList.contains("run")) return { ok: false, detail: "row should no longer be running after job_read_output" };
  if (!glyph.classList.contains("done")) return { ok: false, detail: "completed row should carry .g.done glyph, classes=" + glyph.className };
  // result preview from the job output
  const res = row.querySelector(".res");
  if (!res || !res.textContent.includes("found 7 call sites")) return { ok: false, detail: "missing result preview: " + (res && res.textContent) };
  // header tally reflects the reconciliation
  const tally = conv.querySelector(".subs .subs-h .tally");
  if (!tally || !/done/.test(tally.textContent)) return { ok: false, detail: "header tally should report done: " + (tally && tally.textContent) };
  if (tally && /running/.test(tally.textContent)) return { ok: false, detail: "header tally should not still say running: " + tally.textContent };
  return { ok: true };
});

await scenario("a failed child surfaces in the error color at module and row level", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "ok worker", "local:child-A"),
  ...spawnDelegate("d2", "job_B", "billing worker", "local:child-B"),
  ["JOB_FINISHED", { jobId: "job_A", jobType: "delegate", status: "completed", outputBytes: 10, transcriptRef: "local:child-A" }],
  ["JOB_FINISHED", { jobId: "job_B", jobType: "delegate", status: "failed", reason: "assertion failed", outputBytes: 5, transcriptRef: "local:child-B" }],
], ({ conv }) => {
  const failedRow = conv.querySelector('.subs .sub-r[data-job-id="job_B"]');
  if (!failedRow) return { ok: false, detail: "missing failed row" };
  const glyph = failedRow.querySelector(".g");
  if (!glyph || !glyph.classList.contains("err")) return { ok: false, detail: "failed row should carry .g.err glyph, classes=" + (glyph && glyph.className) };
  // failure reason promoted into the result and styled as error
  const res = failedRow.querySelector(".res");
  if (!res || !res.textContent.includes("assertion failed")) return { ok: false, detail: "failed row should show reason: " + (res && res.textContent) };
  // module header surfaces the failure (✕ ... failed) — not folded into "N done"
  const tally = conv.querySelector(".subs .subs-h .tally");
  if (!tally || !/failed/.test(tally.textContent)) return { ok: false, detail: "header tally must surface failed: " + (tally && tally.textContent) };
  const failMarker = tally.querySelector(".f");
  if (!failMarker) return { ok: false, detail: "header should carry a dedicated .f failure marker" };
  // module flagged so CSS can color the box-level signal
  const mod = conv.querySelector(".subs");
  if (mod.dataset.hasFailure !== "true") return { ok: false, detail: "module should flag data-has-failure when a child failed" };
  return { ok: true };
});

await scenario("a subagent that ran fine but found bad news stays neutral (done), not red", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "check tests", "local:child-A"),
  // status completed (it ran fine); the bad news lives in the content.
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: JSON.stringify({
    job_id: "job_A", type: "delegate", status: "completed", content: "3 tests FAILED", total_bytes: 14,
  }) }],
], ({ conv }) => {
  const row = conv.querySelector('.subs .sub-r[data-job-id="job_A"]');
  if (!row) return { ok: false, detail: "missing row" };
  const glyph = row.querySelector(".g");
  if (glyph.classList.contains("err")) return { ok: false, detail: "ran-fine subagent must not be marked failed (red)" };
  if (!glyph.classList.contains("done")) return { ok: false, detail: "should be done/neutral, classes=" + glyph.className };
  const mod = conv.querySelector(".subs");
  if (mod.dataset.hasFailure === "true") return { ok: false, detail: "module must NOT flag failure for ran-fine-bad-news" };
  return { ok: true };
});

await scenario("job_list reconciles several subagents at once", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_1", "worker one", "local:c1"),
  ...spawnDelegate("d2", "job_2", "worker two", "local:c2"),
  ["TOOL_CALL_START", { call_id: "jl1", tool_name: "job_list", arguments_json: JSON.stringify({}) }],
  ["TOOL_CALL_END", { call_id: "jl1", tool_name: "job_list", output: JSON.stringify({
    count: 2,
    jobs: [
      { job_id: "job_1", type: "delegate", status: "completed", output_bytes: 30 },
      { job_id: "job_2", type: "delegate", status: "running" },
    ],
  }) }],
], ({ conv }) => {
  const r1 = conv.querySelector('.subs .sub-r[data-job-id="job_1"] .g');
  const r2 = conv.querySelector('.subs .sub-r[data-job-id="job_2"] .g');
  if (!r1 || !r1.classList.contains("done")) return { ok: false, detail: "job_1 should reconcile to done from job_list" };
  if (!r2 || !r2.classList.contains("run")) return { ok: false, detail: "job_2 should remain running per job_list" };
  return { ok: true };
});

await scenario("heavy fan-out collapses overflow rows behind +N more · expand", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_1", "one", "local:c1"),
  ...spawnDelegate("d2", "job_2", "two", "local:c2"),
  ...spawnDelegate("d3", "job_3", "three", "local:c3"),
  ...spawnDelegate("d4", "job_4", "four", "local:c4"),
  ...spawnDelegate("d5", "job_5", "five", "local:c5"),
  ...spawnDelegate("d6", "job_6", "six", "local:c6"),
  ...spawnDelegate("d7", "job_7", "seven", "local:c7"),
  ...spawnDelegate("d8", "job_8", "eight", "local:c8"),
], ({ conv }) => {
  const mod = conv.querySelector(".subs");
  if (!mod) return { ok: false, detail: "missing module" };
  const rows = mod.querySelectorAll(".sub-r");
  if (rows.length !== 8) return { ok: false, detail: "all 8 rows should exist in the DOM, got " + rows.length };
  // header still counts all 8
  if (!mod.querySelector(".subs-h").textContent.includes("8")) return { ok: false, detail: "header should count 8" };
  // overflow control present and labelled
  const more = mod.querySelector(".subs-more");
  if (!more) return { ok: false, detail: "missing +N more · expand control under heavy fan-out" };
  if (!/more/.test(more.textContent) || !/expand/.test(more.textContent)) {
    return { ok: false, detail: "overflow control should read '+N more · expand': " + more.textContent };
  }
  // before expansion, the module is collapsed (some rows hidden via class)
  if (mod.dataset.expanded === "true") return { ok: false, detail: "module should start collapsed under heavy fan-out" };
  // expanding reveals all rows
  more.click();
  if (mod.dataset.expanded !== "true") return { ok: false, detail: "clicking expand should mark module expanded" };
  return { ok: true };
});

await scenario("a new conversation entry closes the current module so the next fan-out is separate", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "first", "local:cA"),
  ["ASSISTANT_TEXT_START", {}],
  ["ASSISTANT_TEXT_DELTA", { delta: "now spawning more" }],
  ["ASSISTANT_TEXT_END", { text: "now spawning more" }],
  ...spawnDelegate("d2", "job_B", "second", "local:cB"),
], ({ conv }) => {
  const modules = conv.querySelectorAll(".subs");
  if (modules.length !== 2) return { ok: false, detail: "expected 2 separate modules, got " + modules.length };
  if (!modules[0].querySelector('.sub-r[data-job-id="job_A"]')) return { ok: false, detail: "first module missing job_A" };
  if (!modules[1].querySelector('.sub-r[data-job-id="job_B"]')) return { ok: false, detail: "second module missing job_B" };
  return { ok: true };
});

await scenario("CSS defines the subagents module section with the four-color glyphs", [], () => {
  if (!/\.subs\s*\{/.test(styleSrc)) return { ok: false, detail: "missing .subs rule" };
  if (!/\.sub-r\s*\{/.test(styleSrc)) return { ok: false, detail: "missing .sub-r rule" };
  if (!/\.g\.run\b/.test(styleSrc)) return { ok: false, detail: "missing running glyph color" };
  if (!/\.g\.done\b/.test(styleSrc)) return { ok: false, detail: "missing done glyph color" };
  if (!/\.g\.err\b/.test(styleSrc)) return { ok: false, detail: "missing error glyph color" };
  return { ok: true };
});

if (!allPass) { console.error("FAIL: subagent tests failed"); process.exit(1); }
console.log("OK\ttest-subagents.js");
process.exit(0);

})();
