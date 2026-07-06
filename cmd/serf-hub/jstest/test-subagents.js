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
// job_read_output / job_list / delegate_send for that job id, not only from a
// JOB_FINISHED event (which often never arrives).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const STYLE_PATH = path.resolve(__dirname, "../assets/style.css");
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
  const result = await check({ conv, window });
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

function jobStarted(jobId, delegateId, task, transcriptRef, callId) {
  return ["JOB_STARTED", {
    jobId, jobType: "delegate", status: "running", delegateId, task,
    transcriptRef, originToolCallId: callId, originItemId: "item_" + callId,
  }];
}

function jobFinished(jobId, delegateId, task, transcriptRef, callId, status) {
  return ["JOB_FINISHED", {
    jobId, jobType: "delegate", status: status || "completed", delegateId, task,
    transcriptRef, originToolCallId: callId, originItemId: "item_" + callId, outputBytes: 42,
  }];
}

(async () => {

await scenario("JOB_STARTED before delegate TOOL_CALL_END merges into one row", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_01KW0LONGSTARTIDABCDEFG", "dlg_01KW0DELEGATEABCDEFG", "inspect billing", "local:child-A", "d1"),
  ...spawnDelegate("d1", "job_01KW0LONGSTARTIDABCDEFG", "inspect billing", "local:child-A"),
], ({ conv }) => {
  const rows = conv.querySelectorAll(".subs .sub-r");
  if (rows.length !== 1) return { ok: false, detail: "expected one merged row, got " + rows.length };
  const row = rows[0];
  if (row.dataset.delegateId !== "dlg_01KW0DELEGATEABCDEFG") return { ok: false, detail: "missing delegateId dataset" };
  if (row.dataset.fullDelegateId !== "dlg_01KW0DELEGATEABCDEFG") return { ok: false, detail: "missing fullDelegateId dataset" };
  if (row.dataset.fullJobId !== "job_01KW0LONGSTARTIDABCDEFG") return { ok: false, detail: "missing fullJobId dataset" };
  if (row.dataset.originToolCallId !== "d1") return { ok: false, detail: "missing origin call dataset" };
  if (row.dataset.originItemId !== "item_d1") return { ok: false, detail: "missing origin item dataset" };
  const meta = row.querySelector(".sub-meta");
  if (!meta || !meta.textContent.includes("job 01KW0L…BCDEFG")) return { ok: false, detail: "missing shortened job metadata: " + (meta && meta.textContent) };
  if (!meta || meta.textContent.includes("job_01KW0LONGSTARTIDABCDEFG")) return { ok: false, detail: "raw long job id used in metadata: " + (meta && meta.textContent) };
  if (!row.textContent.includes("inspect billing")) return { ok: false, detail: "task should be primary label: " + row.textContent };
  if (row.querySelector(".nm").textContent.includes("job_01KW0LONGSTARTIDABCDEFG")) return { ok: false, detail: "raw long job id used as primary label" };
  return { ok: true };
});

await scenario("JOB_FINISHED updates originating delegate row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "inspect billing", "local:child-A"),
  jobFinished("job_A", "dlg_A", "inspect billing", "local:child-A", "d1", "completed"),
], ({ conv }) => {
  const rows = conv.querySelectorAll(".subs .sub-r");
  if (rows.length !== 1) return { ok: false, detail: "expected one row after finish, got " + rows.length };
  const glyph = rows[0].querySelector(".g");
  if (!glyph || !glyph.classList.contains("done")) return { ok: false, detail: "finished row should be done: " + (glyph && glyph.className) };
  if (rows[0].dataset.status !== "completed") return { ok: false, detail: "status dataset not terminal" };
  return { ok: true };
});

await scenario("delegate_send second job creates second row under same delegate", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_first", "dlg_same", "inspect billing", "local:child", "d1"),
  jobFinished("job_first", "dlg_same", "inspect billing", "local:child", "d1", "completed"),
  jobStarted("job_second", "dlg_same", "inspect billing", "local:child", "send1"),
], ({ conv }) => {
  const rows = conv.querySelectorAll('.subs .sub-r[data-delegate-id="dlg_same"]');
  if (rows.length !== 2) return { ok: false, detail: "expected two runs under delegate, got " + rows.length };
  const first = conv.querySelector('.sub-r[data-job-id="job_first"] .g');
  const second = conv.querySelector('.sub-r[data-job-id="job_second"] .g');
  if (!first || !first.classList.contains("done")) return { ok: false, detail: "first run overwritten or not done" };
  if (!second || !second.classList.contains("run")) return { ok: false, detail: "second run should be running" };
  return { ok: true };
});

await scenario("resumed delegate job with same origin creates a new run row", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_first", "dlg_same", "inspect billing", "local:child-first", "d1"),
  jobFinished("job_first", "dlg_same", "inspect billing", "local:child-first", "d1", "completed"),
  ["JOB_STARTED", {
    jobId: "job_second", jobType: "delegate", status: "running", delegateId: "dlg_same", task: "inspect billing",
    transcriptRef: "local:child-second", originToolCallId: "d1", originItemId: "item_d1",
  }],
], ({ conv }) => {
  const rows = conv.querySelectorAll('.subs .sub-r[data-delegate-id="dlg_same"]');
  if (rows.length !== 2) return { ok: false, detail: "expected two runs under delegate, got " + rows.length };
  const first = conv.querySelector('.subs .sub-r[data-job-id="job_first"]');
  const second = conv.querySelector('.subs .sub-r[data-job-id="job_second"]');
  if (!first) return { ok: false, detail: "missing first run row" };
  if (!second) return { ok: false, detail: "missing second run row" };
  if (first.dataset.status !== "completed") return { ok: false, detail: "first run status overwritten: " + first.dataset.status };
  const firstGlyph = first.querySelector(".g");
  if (!firstGlyph || !firstGlyph.classList.contains("done")) return { ok: false, detail: "first run should remain done" };
  if (second.dataset.status !== "running") return { ok: false, detail: "second run should be running: " + second.dataset.status };
  const secondGlyph = second.querySelector(".g");
  if (!secondGlyph || !secondGlyph.classList.contains("run")) return { ok: false, detail: "second run should show running glyph" };
  return { ok: true };
});

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
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, 18 bytes", tool_state: JSON.stringify({
    job_id: "job_A", type: "delegate", status: "completed", output: "found 7 call sites", total_bytes: 18,
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
  // status completed (it ran fine); the bad news lives in the output.
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, 14 bytes", tool_state: JSON.stringify({
    job_id: "job_A", type: "delegate", status: "completed", output: "3 tests FAILED", total_bytes: 14,
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
  ["TOOL_CALL_END", { call_id: "jl1", tool_name: "job_list", output: "2 jobs", tool_state: JSON.stringify({
    count: 2,
    jobs: [
      { job_id: "job_1", type: "delegate", status: "completed", total_bytes: 30 },
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

await scenario("live activity: child frames push to the row's activity line, steps + honest aging", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_L", "trace the retry callers", "local:child-L"),
], ({ conv, window }) => {
  const R = window.SerfRenderer;
  const row = conv.querySelector('.sub-r[data-job-id="job_L"]');
  if (!row) return { ok: false, detail: "no row" };
  // A frame for the watched child is routed to the row (and swallowed — never
  // rendered into the parent transcript).
  const before = conv.querySelectorAll(".tool-call, .assistant-message").length;
  if (!R.handleChildFrame("item/started", { ref: "local:child-L", item: { toolName: "shell", description: "go test ./..." } })) {
    return { ok: false, detail: "a frame for a watched child must be handled" };
  }
  if (conv.querySelectorAll(".tool-call, .assistant-message").length !== before) {
    return { ok: false, detail: "a child frame must NOT render into the parent transcript" };
  }
  if (row.dataset.steps !== "1") return { ok: false, detail: "first activity should set steps=1, got " + row.dataset.steps };
  const live = row.querySelector(".res .live");
  if (!live || !/shell: go test/.test(live.textContent)) return { ok: false, detail: "activity line should show the latest step: " + (live && live.textContent) };
  // A frame for an UNwatched ref is not ours — must not be swallowed.
  if (R.handleChildFrame("item/started", { ref: "local:other", item: { toolName: "grep" } })) {
    return { ok: false, detail: "a frame for an unwatched ref must not be swallowed" };
  }
  // New activity advances the step count; an identical repeat does NOT (honest).
  R.handleChildFrame("item/started", { ref: "local:child-L", item: { toolName: "read_file", description: "auth/session.go" } });
  if (row.dataset.steps !== "2") return { ok: false, detail: "new activity should advance steps to 2, got " + row.dataset.steps };
  R.handleChildFrame("item/started", { ref: "local:child-L", item: { toolName: "read_file", description: "auth/session.go" } });
  if (row.dataset.steps !== "2") return { ok: false, detail: "identical activity must not advance steps, got " + row.dataset.steps };
  // Honest aging: rewind the last change and re-age → silent + a 'quiet Ns' clock.
  row.dataset.lastChangeAt = String(Date.now() - 50000);
  R.ageSubagentRow(row);
  if (!row.classList.contains("act-silent")) return { ok: false, detail: "50s without a change should read as silent, not a spinning lie" };
  const age = row.querySelector(".res .age");
  if (!age || !/quiet/.test(age.textContent)) return { ok: false, detail: "a silent row should show a 'quiet Ns' clock: " + (age && age.textContent) };
  return { ok: true };
});

await scenario("a long-lived (background) shell job joins the rail; a foreground one does not", [
  ["SESSION_START", { session_id: "01TEST" }],
  // Foreground shell: stays a tool card, never a rail row.
  ["JOB_STARTED", { jobId: "job_FG", jobType: "shell", command: "ls -la", status: "running" }],
  ["JOB_FINISHED", { jobId: "job_FG", jobType: "shell", status: "completed", outputBytes: 12 }],
  // Background shell: joins the rail, named by its command.
  ["JOB_STARTED", { jobId: "job_BG", jobType: "shell", background: true, command: "go test ./... -count=1", status: "running" }],
], ({ conv }) => {
  if (conv.querySelector('.sub-r[data-job-id="job_FG"]')) return { ok: false, detail: "a foreground shell must NOT join the rail" };
  const row = conv.querySelector('.sub-r[data-job-id="job_BG"]');
  if (!row) return { ok: false, detail: "a background shell should join the rail" };
  if (row.dataset.statusKind !== "running") return { ok: false, detail: "background shell row should be running" };
  const nm = row.querySelector(".nm");
  if (!nm || !/go test/.test(nm.textContent)) return { ok: false, detail: "the row should be named by its command: " + (nm && nm.textContent) };
  return { ok: true };
});

await scenario("a job notification ties to its rail row: shared headline + cross-links", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("dt", "job_T", "port the webhook verification", "local:ct"),
  ["JOB_FINISHED", { jobId: "job_T", status: "completed", outputBytes: 233, transcriptRef: "local:ct" }],
  ["STEERING_INJECTED", { text: `<job-notification job_id="job_T" event="completed" job_type="delegate" status="completed" transcript_ref="local:ct">
Job job_T completed.
excerpt:
${JSON.stringify({ message: "Status: DONE", data: { status: "DONE", test_summary: "go test ./agent passed", commit_hashes: ["4ad69c0e"], concerns: [] }, artifacts: [] })}
</job-notification>` }],
], ({ conv }) => {
  const row = conv.querySelector('.sub-r[data-job-id="job_T"]');
  const card = conv.querySelector('.notification-card[data-job-id="job_T"]');
  if (!row) return { ok: false, detail: "no rail row for job_T" };
  if (!card) return { ok: false, detail: "no notification card for job_T" };
  // Both ends are marked tied.
  if (row.dataset.tied !== "job_T" || card.dataset.tied !== "job_T") {
    return { ok: false, detail: "both ends should be marked tied (row=" + row.dataset.tied + " card=" + card.dataset.tied + ")" };
  }
  // The row pulls the notification's headline instead of "done · 233 bytes".
  const res = row.querySelector(".res");
  if (!res || !/go test \.\/agent passed/.test(res.textContent)) {
    return { ok: false, detail: "row result should show the notification headline: " + (res && res.textContent) };
  }
  if (!/4ad69c0e/.test(res.textContent)) return { ok: false, detail: "headline should include the short commit: " + res.textContent };
  // Each carries a cross-link to the other.
  if (!card.querySelector(".notification-card-tie")) return { ok: false, detail: "card missing the '↑ in rail' cross-link" };
  if (!row.querySelector(".tie-link.sub-report")) return { ok: false, detail: "row missing the 'report ↓' cross-link" };
  return { ok: true };
});

await scenario("done subagents fold behind a count; running stay visible", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_1", "one", "local:c1"),
  ...spawnDelegate("d2", "job_2", "two", "local:c2"),
  ...spawnDelegate("d3", "job_3", "three", "local:c3"),
  ...spawnDelegate("d4", "job_4", "four", "local:c4"),
  ...spawnDelegate("d5", "job_5", "five", "local:c5"),
  ...spawnDelegate("d6", "job_6", "six", "local:c6"),
  ...spawnDelegate("d7", "job_7", "seven", "local:c7"),
  ...spawnDelegate("d8", "job_8", "eight", "local:c8"),
  // five finish; three keep running.
  ["JOB_FINISHED", { jobId: "job_1", status: "completed", outputBytes: 1, transcriptRef: "local:c1" }],
  ["JOB_FINISHED", { jobId: "job_2", status: "completed", outputBytes: 1, transcriptRef: "local:c2" }],
  ["JOB_FINISHED", { jobId: "job_3", status: "completed", outputBytes: 1, transcriptRef: "local:c3" }],
  ["JOB_FINISHED", { jobId: "job_4", status: "completed", outputBytes: 1, transcriptRef: "local:c4" }],
  ["JOB_FINISHED", { jobId: "job_5", status: "completed", outputBytes: 1, transcriptRef: "local:c5" }],
], ({ conv }) => {
  const mod = conv.querySelector(".subs");
  if (!mod) return { ok: false, detail: "missing module" };
  const rows = Array.from(mod.querySelectorAll(".sub-r"));
  if (rows.length !== 8) return { ok: false, detail: "all 8 rows should exist in the DOM, got " + rows.length };
  // Collapsed: the three running rows stay visible; the five done fold away.
  const visible = rows.filter(r => !r.hidden);
  if (visible.length !== 3 || !visible.every(r => r.dataset.statusKind === "running")) {
    return { ok: false, detail: "only the 3 running rows should be visible when collapsed, got " + visible.map(r => r.dataset.statusKind).join(",") };
  }
  const more = mod.querySelector(".subs-more");
  if (!more || !/5 done/.test(more.textContent)) return { ok: false, detail: "done should fold behind a '✓ 5 done' count: " + (more && more.textContent) };
  if (!more.innerHTML.includes("<svg") || more.innerHTML.includes("✓")) return { ok: false, detail: "the fold button's done glyph should be a SerfIcons svg, not a literal '✓': " + more.innerHTML };
  if (mod.dataset.expanded === "true") return { ok: false, detail: "module should start collapsed" };
  // Expanding reveals the done rows too.
  more.click();
  if (mod.dataset.expanded !== "true") return { ok: false, detail: "clicking should expand" };
  if (rows.some(r => r.hidden)) return { ok: false, detail: "expanding should reveal all rows" };
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
  // Extract each glyph rule block and verify the semantic color token it carries.
  const runBlock  = (styleSrc.match(/\.g\.run\b[^{]*\{[^}]*\}/g) || []).join(" ");
  const doneBlock = (styleSrc.match(/\.g\.done\b[^{]*\{[^}]*\}/g) || []).join(" ");
  const errBlock  = (styleSrc.match(/\.g\.err\b[^{]*\{[^}]*\}/g) || []).join(" ");
  const unkBlock  = (styleSrc.match(/\.g\.unk\b[^{]*\{[^}]*\}/g) || []).join(" ");
  if (!runBlock)  return { ok: false, detail: "missing .g.run rule" };
  if (!doneBlock) return { ok: false, detail: "missing .g.done rule" };
  if (!errBlock)  return { ok: false, detail: "missing .g.err rule" };
  if (!unkBlock)  return { ok: false, detail: "missing .g.unk rule (mockup #8 honest-clock demotion)" };
  // running glyph must carry the active/working token — not muted or dim.
  if (!/color:[^;]*var\(--state-working\)/.test(runBlock)) return { ok: false, detail: ".g.run must use --state-working so the running glyph signals an active worker" };
  // error glyph must carry an error-semantic color so failed workers render red.
  if (!/color:[^;]*var\(--error\)/.test(errBlock)) return { ok: false, detail: ".g.err must use --error so a failed worker is visually distinct" };
  return { ok: true };
});

await scenario("subagent module header names the section + tallies the workers", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "trace callers", "local:child-A"),
  ...spawnDelegate("d2", "job_B", "find tests", "local:child-B"),
  ...spawnDelegate("d3", "job_C", "update docs", "local:child-C"),
], ({ conv }) => {
  const mod = conv.querySelector(".subs");
  if (!mod) return { ok: false, detail: "missing .subs module" };
  if (mod.dataset.count !== "3") return { ok: false, detail: "module should carry data-count=3 (it's a fan-out, not a lone row): " + mod.dataset.count };
  const header = mod.querySelector(".subs-h");
  const label = header.querySelector(".t");
  if (!label || !label.textContent.includes("Subagents")) return { ok: false, detail: "header label must name 'Subagents': " + (label && label.textContent) };
  const tally = header.querySelector(".tally");
  if (!tally || !/3 running/.test(tally.textContent)) return { ok: false, detail: "tally should count the workers (3 running): " + (tally && tally.textContent) };
  return { ok: true };
});

await scenario("subagent module header is absent when there are no subagents", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["ASSISTANT_TEXT_START", {}],
  ["ASSISTANT_TEXT_DELTA", { delta: "hello" }],
  ["ASSISTANT_TEXT_END", { text: "hello" }],
], ({ conv }) => {
  const mod = conv.querySelector(".subs");
  if (mod) return { ok: false, detail: "no .subs module should exist when no subagents have been spawned" };
  return { ok: true };
});

await scenario("CSS: the subagents module is a left rail, not a box", [], () => {
  // Sibling of the living plan card: ONE neutral left rail (rail = "status"),
  // never a filled box (a box reads as "needs-you"). The retired uppercase-mono
  // section-header treatment is gone — the label is plain muted sans.
  const blocks = styleSrc.match(/\.subs\s*\{[^}]*\}/g) || [];
  if (!blocks.length) return { ok: false, detail: "missing .subs rule" };
  if (!blocks.some(b => /border-left:[^;]*var\(--rule\)/.test(b))) return { ok: false, detail: ".subs must use a neutral left rail" };
  if (blocks.some(b => /\bbackground:\s*var\(--bg-raised\)/.test(b))) return { ok: false, detail: ".subs must not be a filled box" };
  if (!/\.subs\[data-count="1"\]/.test(styleSrc)) return { ok: false, detail: "a lone subagent (data-count=1) must drop the rail/header to read as a row" };
  return { ok: true };
});

// ============================================================
// Mockup #8 — honest-clock demotion + "?" unknown state.
// A subagent left "⟳ running" on a session that is no longer live (no
// completion signal ever arrives) must NOT keep a spinner forever. When the
// thread goes terminal (ended/closed/notLoaded) — or idle with no active turn
// — every still-running row demotes to a neutral terminal "?" UNKNOWN state,
// freezing its last-known elapsed and labelling it "last seen Ns ago".
// ============================================================

await scenario("a dangling running subagent demotes to '?' unknown when the session ends", [
  ["SESSION_START", { session_id: "01TEST", status: "active" }],
  ...spawnDelegate("d1", "job_A", "trace callers", "local:child-A"),
  ...spawnDelegate("d2", "job_B", "search indexer", "local:child-B"),
  // job_A reports done via a read; job_B never reports back.
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, 18 bytes", tool_state: JSON.stringify({
    job_id: "job_A", type: "delegate", status: "completed", output: "found 7 call sites", total_bytes: 18,
  }) }],
  // The session goes dark with job_B still "running" and no JOB_FINISHED.
  ["THREAD_STATUS_CHANGED", { status: "closed" }],
], ({ conv }) => {
  const dangling = conv.querySelector('.subs .sub-r[data-job-id="job_B"]');
  if (!dangling) return { ok: false, detail: "missing dangling row" };
  const glyph = dangling.querySelector(".g");
  if (!glyph || glyph.classList.contains("run")) return { ok: false, detail: "dangling row must NOT keep the running spinner after the session ends" };
  if (!glyph.classList.contains("unk")) return { ok: false, detail: "dangling row should demote to unknown (.g.unk), classes=" + glyph.className };
  if (glyph.textContent !== "?") return { ok: false, detail: "unknown glyph should be '?', was " + glyph.textContent };
  if (dangling.dataset.statusKind !== "unknown") return { ok: false, detail: "row statusKind should be unknown, was " + dangling.dataset.statusKind };
  // the settled job_A keeps its done state (not demoted)
  const settled = conv.querySelector('.subs .sub-r[data-job-id="job_A"] .g');
  if (!settled || !settled.classList.contains("done")) return { ok: false, detail: "settled done row must not be demoted" };
  // result text is honest about never reporting back, and the frozen duration
  // is marked approximate (the ~ prefix).
  const res = dangling.querySelector(".res");
  if (!res || !/never reported|last seen|unknown/i.test(res.textContent)) return { ok: false, detail: "unknown row should explain it never reported back: " + (res && res.textContent) };
  const dur = dangling.querySelector(".dur");
  if (dur && dur.textContent && !dur.textContent.startsWith("~")) return { ok: false, detail: "frozen unknown duration should be approximate (~): " + dur.textContent };
  return { ok: true };
});

await scenario("module header tally surfaces the '? N unknown' count after demotion", [
  ["SESSION_START", { session_id: "01TEST", status: "active" }],
  ...spawnDelegate("d1", "job_A", "done one", "local:child-A"),
  ...spawnDelegate("d2", "job_B", "stuck one", "local:child-B"),
  ["JOB_FINISHED", { jobId: "job_A", jobType: "delegate", status: "completed", outputBytes: 10, transcriptRef: "local:child-A" }],
  ["THREAD_STATUS_CHANGED", { status: "ended" }],
], ({ conv }) => {
  const tally = conv.querySelector(".subs .subs-h .tally");
  if (!tally) return { ok: false, detail: "missing tally" };
  if (!/unknown/.test(tally.textContent)) return { ok: false, detail: "tally should report unknown after demotion: " + tally.textContent };
  if (/running/.test(tally.textContent)) return { ok: false, detail: "tally must not still claim running after the session ended: " + tally.textContent };
  const u = tally.querySelector(".u");
  if (!u) return { ok: false, detail: "tally should carry a dedicated .u unknown marker" };
  // the module wears a neutral stale flag (honest, not a fake spinner)
  const mod = conv.querySelector(".subs");
  if (mod.dataset.stale !== "true") return { ok: false, detail: "module should flag data-stale when it has demoted-unknown rows" };
  return { ok: true };
});

await scenario("idle with no active turn demotes dangling running rows", [
  ["SESSION_START", { session_id: "01TEST", status: "active" }],
  ["TURN_STARTED", { turnId: "turn_1" }],
  ...spawnDelegate("d1", "job_A", "background worker", "local:child-A"),
  // Parent turn completes; no completion signal for job_A ever arrives.
  ["TURN_COMPLETED", { turnId: "turn_1" }],
], ({ conv }) => {
  const row = conv.querySelector('.subs .sub-r[data-job-id="job_A"] .g');
  if (!row) return { ok: false, detail: "missing row" };
  if (row.classList.contains("run")) return { ok: false, detail: "row should demote once the parent turn ends idle with no completion signal" };
  if (!row.classList.contains("unk")) return { ok: false, detail: "row should be unknown, classes=" + row.className };
  return { ok: true };
});

await scenario("a still-active session does NOT demote a legitimately running subagent", [
  ["SESSION_START", { session_id: "01TEST", status: "active" }],
  ["TURN_STARTED", { turnId: "turn_1" }],
  ...spawnDelegate("d1", "job_A", "live worker", "local:child-A"),
  // session stays active; the subagent is genuinely live.
  ["THREAD_STATUS_CHANGED", { status: "active" }],
], ({ conv }) => {
  const row = conv.querySelector('.subs .sub-r[data-job-id="job_A"] .g');
  if (!row) return { ok: false, detail: "missing row" };
  if (!row.classList.contains("run")) return { ok: false, detail: "a genuinely-live subagent must keep its running spinner while the session is active, classes=" + row.className };
  return { ok: true };
});

await scenario("fixed spawn-order: running + failed stay visible, done fold (no reshuffle)", [
  ["SESSION_START", { session_id: "01TEST", status: "active" }],
  ...spawnDelegate("d1", "job_1", "done one", "local:c1"),
  ...spawnDelegate("d2", "job_2", "done two", "local:c2"),
  ...spawnDelegate("d3", "job_3", "done three", "local:c3"),
  ...spawnDelegate("d4", "job_4", "done four", "local:c4"),
  ...spawnDelegate("d5", "job_5", "done five", "local:c5"),
  ...spawnDelegate("d6", "job_6", "done six", "local:c6"),
  ...spawnDelegate("d7", "job_7", "failed one", "local:c7"),
  ...spawnDelegate("d8", "job_8", "running one", "local:c8"),
  // settle the six "done" jobs and fail job_7; job_8 stays running.
  ["JOB_FINISHED", { jobId: "job_1", status: "completed", outputBytes: 1, transcriptRef: "local:c1" }],
  ["JOB_FINISHED", { jobId: "job_2", status: "completed", outputBytes: 1, transcriptRef: "local:c2" }],
  ["JOB_FINISHED", { jobId: "job_3", status: "completed", outputBytes: 1, transcriptRef: "local:c3" }],
  ["JOB_FINISHED", { jobId: "job_4", status: "completed", outputBytes: 1, transcriptRef: "local:c4" }],
  ["JOB_FINISHED", { jobId: "job_5", status: "completed", outputBytes: 1, transcriptRef: "local:c5" }],
  ["JOB_FINISHED", { jobId: "job_6", status: "completed", outputBytes: 1, transcriptRef: "local:c6" }],
  ["JOB_FINISHED", { jobId: "job_7", status: "failed", reason: "boom", transcriptRef: "local:c7" }],
], ({ conv }) => {
  const mod = conv.querySelector(".subs");
  if (!mod) return { ok: false, detail: "missing module" };
  const all = Array.from(mod.querySelectorAll(".sub-r"));
  const visible = all.filter(r => !r.hidden);
  // The six done fold away; the failure + the running one stay visible — never
  // buried, but also never sorted to the top (rows keep spawn order so they
  // don't jump as statuses change live).
  if (visible.length !== 2) return { ok: false, detail: "only the failed + running rows should be visible, got " + visible.map(r => r.dataset.statusKind).join(",") };
  const kinds = visible.map(r => r.dataset.statusKind);
  if (!kinds.includes("failed") || !kinds.includes("running")) return { ok: false, detail: "failed and running must stay visible: " + kinds.join(",") };
  // Fixed spawn-order: job_7 (failed, spawn 7) stays before job_8 (running, spawn 8).
  const spawn = visible.map(r => Number(r.dataset.spawnIndex));
  if (spawn[0] >= spawn[1]) return { ok: false, detail: "rows must stay in spawn order, not reshuffle by status: " + spawn.join(",") };
  const more = mod.querySelector(".subs-more");
  if (!more || !/6 done/.test(more.textContent)) return { ok: false, detail: "the six done should fold behind a '✓ 6 done' count: " + (more && more.textContent) };
  return { ok: true };
});

await scenario("expanded subagent row lazy-loads bounded preview", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobFinished("job_preview", "dlg_preview", "inspect billing", "local:child-preview", "dpreview", "completed"),
], async ({ conv, window }) => {
  let requested = "";
  window.fetch = (url) => {
    requested = String(url);
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ ref: "local:child-preview", truncated: true, items: [
      { type: "agentMessage", text: "found three callers" },
      { type: "commandExecution", toolName: "grep_files", description: "search billing", status: "completed" },
      { type: "agentMessage", text: "recommended fix" },
      { type: "agentMessage", text: "extra item should not render when limit is 3" },
    ] }) });
  };
  const row = conv.querySelector('.sub-r[data-job-id="job_preview"]');
  if (!row) return { ok: false, detail: "missing preview row" };
  row.click();
  await new Promise(r => setTimeout(r, 20));
  const preview = row.parentElement.querySelector('.sub-preview');
  if (!requested.includes("local%3Achild-preview")) return { ok: false, detail: "preview endpoint not requested: " + requested };
  if (!preview) return { ok: false, detail: "missing preview container" };
  if (!preview.textContent.includes("found three callers") || !preview.textContent.includes("search billing")) return { ok: false, detail: "missing preview snippets: " + preview.textContent };
  if (!preview.textContent.includes("recommended fix")) return { ok: false, detail: "preview should render third item (lower bound of limit is 3): " + preview.textContent };
  if (preview.textContent.includes("extra item should not render")) return { ok: false, detail: "preview rendered more than bounded limit" };
  return { ok: true };
});

if (!allPass) { console.error("FAIL: subagent tests failed"); process.exit(1); }
console.log("OK\ttest-subagents.js");
process.exit(0);

})();
