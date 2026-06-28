// Smoke-test the per-tool renderer registry: shell with stdout,
// edit_file with diff, web_fetch, web_search, delegate, and the
// cheap-cluster grouping for read_file/grep/list_dir/glob.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const STYLE_PATH = path.join(__dirname, "../assets/style.css");
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
    <form data-input-form data-session-id="01TEST">
      <div class="task-status-row">
        <button type="button" class="status-item tasks-status" data-tasks-trigger title="task list"><span class="status-key">tasks</span><span class="status-value" data-task-status-text>loading…</span></button>
      </div>
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") };
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

async function streamingMarkdownScenario() {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("SESSION_START", { session_id: "01TEST" });
  window.SerfRenderer.handleData("ASSISTANT_TEXT_START", {});
  window.SerfRenderer.handleData("ASSISTANT_TEXT_DELTA", { delta: "Harness is **serf**" });
  await new Promise(r => setTimeout(r, 550));

  const rendered = conv.querySelector(".assistant-message");
  if (!rendered || !rendered.querySelector("strong")) {
    allPass = false;
    console.log("FAIL — streamed markdown renders before final event");
    console.log("  detail: initial streamed markdown did not render");
    console.log("  HTML: " + conv.innerHTML);
    return;
  }

  window.SerfRenderer.handleData("ASSISTANT_TEXT_DELTA", { delta: " and stable" });
  await new Promise(r => setTimeout(r, 10));

  const updated = conv.querySelector(".assistant-message");
  const rawVisible = updated && updated.innerHTML.includes("**serf**");
  const appended = updated && updated.textContent.includes("and stable");
  const stillRendered = updated && updated.querySelector("strong");
  const ok = updated && !rawVisible && appended && stillRendered;
  console.log((ok ? "PASS" : "FAIL") + " — streamed markdown stays rendered after later deltas");
  if (!ok) {
    allPass = false;
    console.log("  detail: rawVisible=" + rawVisible + " appended=" + appended + " stillRendered=" + !!stillRendered);
    console.log("  HTML: " + conv.innerHTML);
  }
}

(async () => {

// Cheap-cluster — read_file should land in .tool-call-cluster and show read metadata inline.
await scenario("read_file in cheap cluster with inline range, purpose, and five-line preview", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "src/main.go", offset: 10, limit: 8, purpose: "Inspect main entry point." }) }],
  ["TOOL_CALL_END", { call_id: "r1", output: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n", tool_name: "read_file" }],
], ({ conv, window }) => {
  const cluster = conv.querySelector(".tool-call-cluster");
  if (!cluster) return { ok: false, detail: "no cheap cluster" };
  const call = cluster.querySelector(".tool-call.read_file");
  if (!call) return { ok: false, detail: "no read_file tool-call" };
  if (!call.textContent.includes("src/main.go")) return { ok: false, detail: "missing path" };
  if (!call.textContent.includes("lines 10-17")) return { ok: false, detail: "missing line range" };
  if (!call.querySelector(".tool-status-good")) return { ok: false, detail: "missing read success icon" };
  if (call.querySelector(".cheap-tool-args")) return { ok: false, detail: "read_file should not render JSON args" };
  const body = call.querySelector(".read-tool-body");
  if (!body) return { ok: false, detail: "no read tool body" };
  const intent = call.querySelector(".tool-intent");
  if (!intent || intent.textContent !== "Inspect main entry point.") return { ok: false, detail: "read intent missing" };
  const preview = body.querySelector(".read-tool-preview");
  if (!preview || preview.textContent !== "one\ntwo\nthree\nfour\nfive") return { ok: false, detail: "read preview should contain first five lines" };
  if (preview.textContent.includes("six")) return { ok: false, detail: "read preview includes more than five lines" };
  const more = body.querySelector(".read-tool-more");
  if (!more) return { ok: false, detail: "missing read more disclosure" };
  if (!more.querySelector("summary") || !more.querySelector("summary").textContent.includes("3 more lines")) return { ok: false, detail: "missing read more summary" };
  const rest = more.querySelector(".read-tool-rest");
  if (!rest || rest.textContent !== "six\nseven\neight") return { ok: false, detail: "read rest output missing" };
  return { ok: true };
});

await scenario("tool purpose leads as the prominent line; command is demoted beneath it", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "i1", tool_name: "shell", arguments_json: JSON.stringify({ command: "go test ./...", purpose: "Verify the repository before handing off." }) }],
  ["TOOL_CALL_END", { call_id: "i1", output: "ok\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  // A call with a stated purpose is flagged so the command can recede beneath it.
  if (!card.classList.contains("has-purpose")) return { ok: false, detail: "card with a purpose should carry .has-purpose" };
  const intent = card.querySelector(".tool-intent");
  if (!intent) return { ok: false, detail: "missing tool intent" };
  if (intent.textContent !== "Verify the repository before handing off.") return { ok: false, detail: "wrong intent text: " + intent.textContent };
  // The verb/target/result line is wrapped so it can be demoted as a whole.
  const command = card.querySelector(".tool-command");
  if (!command) return { ok: false, detail: "command should be wrapped in .tool-command" };
  if (!command.querySelector(".target") || !command.textContent.includes("go test ./...")) return { ok: false, detail: "command missing the verb/target line" };
  const body = card.querySelector(".shell-body");
  if (!body) return { ok: false, detail: "missing shell body" };
  const children = Array.from(card.children);
  // DOM order keeps the purpose ahead of both the command and the body.
  if (children.indexOf(intent) < 0 || children.indexOf(body) < 0 || children.indexOf(intent) > children.indexOf(body)) {
    return { ok: false, detail: "intent should be before tool results/body" };
  }
  if (children.indexOf(intent) > children.indexOf(command)) {
    return { ok: false, detail: "intent should be before the demoted command in the DOM" };
  }
  if (!/\.tool-call \.tool-intent\s*\{[^}]*font-family:\s*var\(--font-sans\)/.test(styleSrc)) return { ok: false, detail: "intent stylesheet should use variable-width sans font" };
  // The purpose is now the PRIMARY line: prominent (full contrast, readable
  // size, order 1), no longer clamped to a single receding line. The command
  // recedes to a quiet demoted line beneath it via .has-purpose.
  if (/\.tool-call \.tool-intent\s*\{[^}]*-webkit-line-clamp:\s*1/.test(styleSrc)) return { ok: false, detail: "intent should no longer clamp to a single receding line" };
  if (!/\.tool-call \.tool-intent\s*\{[^}]*font-size:\s*var\(--text-base\)/.test(styleSrc)) return { ok: false, detail: "intent should be a prominent --text-base line" };
  if (!/\.tool-call\.has-purpose \.tool-command\s*\{/.test(styleSrc)) return { ok: false, detail: "stylesheet should demote the command under .has-purpose" };
  return { ok: true };
});

await scenario("use_skill target omits purpose already shown as intent", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "use_skill", arguments_json: JSON.stringify({ purpose: "Apply required debugging workflow before fixing the reported web UI rendering bug.", skill_name: "superpowers:systematic-debugging" }) }],
  ["TOOL_CALL_END", { call_id: "s1", output: "Skill loaded", tool_name: "use_skill" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.use_skill");
  if (!card) return { ok: false, detail: "no use_skill card" };
  const target = card.querySelector(".target");
  if (!target || target.textContent !== "superpowers:systematic-debugging") return { ok: false, detail: "target should show only skill name, got: " + (target && target.textContent) };
  const intent = card.querySelector(".tool-intent");
  if (!intent || intent.textContent !== "Apply required debugging workflow before fixing the reported web UI rendering bug.") return { ok: false, detail: "intent should show purpose once" };
  const visible = card.textContent;
  const purposeCount = (visible.match(/Apply required debugging workflow/g) || []).length;
  if (purposeCount !== 1) return { ok: false, detail: "purpose rendered " + purposeCount + " times: " + visible };
  return { ok: true };
});

await scenario("use_skill grouped activation renders inside tool card", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "use_skill", arguments_json: JSON.stringify({ skill_name: "superpowers:using-superpowers" }) }],
  ["TOOL_CALL_END", { call_id: "s1", tool_name: "use_skill", output: "Skill loaded", tool_state: JSON.stringify({ skill_activation: { name: "superpowers:using-superpowers", text: "Activated skill: superpowers:using-superpowers" } }) }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.use_skill");
  if (!card) return { ok: false, detail: "no use_skill card" };
  if (conv.querySelector(".system-message")) return { ok: false, detail: "grouped activation rendered standalone system message" };
  if (!card.textContent.includes("superpowers:using-superpowers")) return { ok: false, detail: "skill name missing from card" };
  card.dataset.expanded = "true";
  const body = card.querySelector(".tool-body");
  if (!body || !body.textContent.includes("Activated skill: superpowers:using-superpowers")) return { ok: false, detail: "activation detail missing from body" };
  return { ok: true };
});

await scenario("standalone skill activation system message still renders", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["SYSTEM_MESSAGE", { title: "Skill activated", text: "Activated skill: standalone" }],
], ({ conv }) => {
  const msg = conv.querySelector(".system-message");
  if (!msg) return { ok: false, detail: "missing standalone system message" };
  if (!msg.textContent.includes("Skill activated")) return { ok: false, detail: "missing title" };
  if (!msg.textContent.includes("Activated skill: standalone")) return { ok: false, detail: "missing activation text" };
  return { ok: true };
});

await scenario("job_read_output renders status, truncation, and output preview", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, 128 bytes, truncated", tool_state: JSON.stringify({
    job_id: "job_A",
    type: "shell",
    status: "completed",
    output: "line one\nline two\nline three",
    total_bytes: 128,
    truncated: true,
  }) }],
], ({ conv }) => {
  const call = conv.querySelector(".tool-call.job_read_output");
  if (!call) return { ok: false, detail: "no job_read_output card" };
  const result = call.querySelector(".result-detail");
  if (!result || !result.textContent.includes("completed")) return { ok: false, detail: "missing status summary" };
  if (!result.textContent.includes("128 bytes")) return { ok: false, detail: "missing byte summary: " + (result && result.textContent) };
  if (!result.textContent.includes("truncated")) return { ok: false, detail: "missing truncation summary" };
  const output = call.querySelector(".job-output");
  if (!output || !output.textContent.includes("line one\nline two")) return { ok: false, detail: "missing job output preview" };
  return { ok: true };
});

await scenario("job_read_output grep state renders matches before the output window", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A", grep: "needle" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, 2 matches", tool_state: JSON.stringify({
    job_id: "job_A",
    type: "shell",
    status: "completed",
    output: "generic output window",
    matches: [
      { line: "needle one" },
      { line: "needle two" },
    ],
    total_bytes: 256,
  }) }],
], ({ conv }) => {
  const call = conv.querySelector(".tool-call.job_read_output");
  if (!call) return { ok: false, detail: "no job_read_output card" };
  const result = call.querySelector(".result-detail");
  if (!result || !result.textContent.includes("2 matches")) return { ok: false, detail: "missing match summary" };
  const output = call.querySelector(".job-output");
  if (!output || !output.textContent.includes("needle one\nneedle two")) return { ok: false, detail: "missing grep matches" };
  if (output.textContent.includes("generic output window")) return { ok: false, detail: "grep view used generic output window" };
  return { ok: true };
});

await scenario("delegate output drops its tool row and adds a clickable subagents-module row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "d1", tool_name: "delegate", arguments_json: JSON.stringify({ task: "Write focused tests" }) }],
  ["TOOL_CALL_END", { call_id: "d1", tool_name: "delegate", output: JSON.stringify({
    job_id: "job_01KW0VERYVERYLONGIDENTIFIER",
    type: "delegate",
    status: "running",
    transcript_ref: "local:delegate-child",
  }) }],
], ({ conv }) => {
  if (conv.querySelector(".tool-call.delegate")) return { ok: false, detail: "delegate tool row was not removed" };
  if (conv.querySelector(".subagent-reference")) return { ok: false, detail: "standalone subagent-reference should no longer be used" };
  const ref = conv.querySelector(".subs .sub-r");
  if (!ref) return { ok: false, detail: "missing subagents-module row" };
  if (ref.dataset.jobId !== "job_01KW0VERYVERYLONGIDENTIFIER") return { ok: false, detail: "missing job id dataset" };
  if (ref.dataset.fullJobId !== "job_01KW0VERYVERYLONGIDENTIFIER") return { ok: false, detail: "missing full job id dataset" };
  if (ref.dataset.transcriptRef !== "local:delegate-child") return { ok: false, detail: "missing transcript ref dataset" };
  const meta = ref.querySelector(".sub-meta");
  if (!meta) return { ok: false, detail: "missing subagent machine metadata" };
  if (meta.textContent.includes("job_01KW0VERYVERYLONGIDENTIFIER")) return { ok: false, detail: "primary visible label leaked raw long job id: " + meta.textContent };
  if (!meta.textContent.includes("job ")) return { ok: false, detail: "primary visible label missing short job label: " + meta.textContent };
  if (!meta.title.includes("job job_01KW0VERYVERYLONGIDENTIFIER")) return { ok: false, detail: "details title missing raw job id: " + meta.title };
  const text = ref.textContent;
  for (const want of ["Write focused tests", "running"]) {
    if (!text.includes(want)) return { ok: false, detail: "subagent row missing " + want + ": " + text };
  }
  if (ref.tagName !== "BUTTON") return { ok: false, detail: "subagent row should be a clickable button" };
  return { ok: true };
});

await scenario("job control tools render structured summaries", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "ds1", tool_name: "delegate_send", arguments_json: JSON.stringify({ to: "dlg_01", message: "continue" }) }],
  ["TOOL_CALL_END", { call_id: "ds1", tool_name: "delegate_send", output: "started delegate turn", tool_state: JSON.stringify({
    delegate_id: "dlg_01",
    started_job_id: "job_STARTED",
    current_job_id: "job_STARTED",
    status: "running",
    action: "started",
    output: "delegate reply",
  }) }],
  ["TOOL_CALL_START", { call_id: "js1", tool_name: "job_send_message", arguments_json: JSON.stringify({ target: "job_A", message: "continue" }) }],
  ["TOOL_CALL_END", { call_id: "js1", tool_name: "job_send_message", output: JSON.stringify({
    target: "job_A",
    job_id: "job_B",
    status: "completed",
    action: "resumed",
    transcript_ref: "local:child",
    output: "final delegate note",
  }) }],
  ["TOOL_CALL_START", { call_id: "jl1", tool_name: "job_list", arguments_json: JSON.stringify({ status: ["running"] }) }],
  ["TOOL_CALL_END", { call_id: "jl1", tool_name: "job_list", output: "2 jobs\njob_1 running\njob_2 completed", tool_state: JSON.stringify({
    count: 2,
    jobs: [
      { job_id: "job_1", type: "delegate", status: "running", description: "write tests" },
      { job_id: "job_2", type: "shell", status: "completed", total_bytes: 42 },
    ],
  }) }],
  ["TOOL_CALL_START", { call_id: "jstop1", tool_name: "job_stop", arguments_json: JSON.stringify({ job_id: "job_1" }) }],
  ["TOOL_CALL_END", { call_id: "jstop1", tool_name: "job_stop", output: "stopped job_1", tool_state: JSON.stringify({ job_id: "job_1", status: "stopped", reason: "user" }) }],
], ({ conv }) => {
  const delegateSend = conv.querySelector(".tool-call.delegate_send .result-detail");
  if (!delegateSend || !delegateSend.textContent.includes("started") || !delegateSend.textContent.includes("running") || !delegateSend.textContent.includes("job_STARTED")) {
    return { ok: false, detail: "delegate_send summary missing structured fields: " + (delegateSend && delegateSend.textContent) };
  }
  const delegateSendHeader = conv.querySelector(".tool-call.delegate_send .target");
  if (!delegateSendHeader || !delegateSendHeader.textContent.includes("dlg_01")) {
    return { ok: false, detail: "delegate_send target missing delegate id: " + (delegateSendHeader && delegateSendHeader.textContent) };
  }
  const delegateSendOutput = conv.querySelector(".tool-call.delegate_send .job-message-output");
  if (!delegateSendOutput || !delegateSendOutput.textContent.includes("delegate reply")) {
    return { ok: false, detail: "delegate_send output missing tool_state body" };
  }
  const send = conv.querySelector(".tool-call.job_send_message .result-detail");
  if (!send || !send.textContent.includes("resumed") || !send.textContent.includes("completed") || !send.textContent.includes("job_B")) {
    return { ok: false, detail: "job_send_message summary missing structured fields: " + (send && send.textContent) };
  }
  const sendOutput = conv.querySelector(".tool-call.job_send_message .job-message-output");
  if (!sendOutput || !sendOutput.textContent.includes("final delegate note")) return { ok: false, detail: "job_send_message output missing" };
  const list = conv.querySelector(".tool-call.job_list .result-detail");
  if (!list || !list.textContent.includes("2 jobs")) return { ok: false, detail: "job_list count missing: " + (list && list.textContent) };
  const listOutput = conv.querySelector(".tool-call.job_list .job-list-output");
  if (!listOutput || !["job_1", "delegate", "running", "write tests"].every(part => listOutput.textContent.includes(part))) {
    return { ok: false, detail: "job_list body missing entry" };
  }
  const stop = conv.querySelector(".tool-call.job_stop .result-detail");
  if (!stop || !stop.textContent.includes("stopped") || !stop.textContent.includes("user")) {
    return { ok: false, detail: "job_stop summary missing structured fields: " + (stop && stop.textContent) };
  }
  return { ok: true };
});

await scenario("job_list renders delegate and watch state sections", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "jl1", tool_name: "job_list", arguments_json: JSON.stringify({}) }],
  ["TOOL_CALL_END", { call_id: "jl1", tool_name: "job_list", output: "0 jobs with delegate and watch sections", tool_state: JSON.stringify({
    count: 0,
    jobs: [],
    delegates: [
      { delegate_id: "dlg_obs", status: "idle", latest_job_id: "job_done", transcript_ref: "local:child", resumable: true },
    ],
    watches: [
      { id: "watch_live", target: "job_target", condition: "events: [job.notification]", send_to: "dlg_obs", deliveries: 0 },
    ],
    recent_watches: [
      { id: "watch_old", target: "job_target", condition: "output_match: ready", end_reason: "cleared", deliveries: 1 },
    ],
  }) }],
], ({ conv }) => {
  const list = conv.querySelector(".tool-call.job_list .result-detail");
  if (!list || !list.textContent.includes("0 jobs")) return { ok: false, detail: "job_list count missing: " + (list && list.textContent) };
  const output = conv.querySelector(".tool-call.job_list .job-list-output");
  if (!output) return { ok: false, detail: "job_list body missing" };
  const text = output.textContent;
  for (const want of ["delegate dlg_obs", "latest_job_id job_done", "watch watch_live", "send_to dlg_obs", "recent watch watch_old", "cleared"]) {
    if (!text.includes(want)) return { ok: false, detail: "job_list body missing " + want + ": " + text };
  }
  return { ok: true };
});

await scenario("orphan JOB_FINISHED creates a completed subagents-module row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["JOB_FINISHED", { jobId: "job_ORPHAN", jobType: "delegate", status: "completed", outputBytes: 77, transcriptRef: "local:child" }],
], ({ conv }) => {
  if (conv.querySelector(".subagent-reference")) return { ok: false, detail: "standalone subagent-reference should no longer be used" };
  const ref = conv.querySelector(".subs .sub-r");
  if (!ref) return { ok: false, detail: "missing fallback subagent row" };
  if (ref.dataset.jobId !== "job_ORPHAN") return { ok: false, detail: "missing job id dataset" };
  if (ref.dataset.transcriptRef !== "local:child") return { ok: false, detail: "missing transcript ref dataset" };
  if (ref.querySelector(".g").classList.contains("run")) return { ok: false, detail: "orphan completion should not be running" };
  const text = ref.textContent;
  for (const want of ["delegate", "77 bytes"]) {
    if (!text.includes(want)) return { ok: false, detail: "fallback row missing " + want + ": " + text };
  }
  if (ref.tagName !== "BUTTON") return { ok: false, detail: "fallback row should be clickable" };
  return { ok: true };
});

await scenario("shell job lifecycle events do not render as subagents", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "find cmd/serf-hub -maxdepth 3 -type f" }) }],
  ["JOB_STARTED", { jobId: "job_SHELL", jobType: "shell", status: "running" }],
  ["JOB_FINISHED", { jobId: "job_SHELL", jobType: "shell", status: "completed", outputBytes: 87157 }],
  ["TOOL_CALL_END", { call_id: "s1", output: "cmd/serf-hub/web_workspace.go\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell" }],
], ({ conv }) => {
  if (conv.querySelector(".subs")) return { ok: false, detail: "shell job lifecycle created subagents module: " + conv.innerHTML };
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "shell card disappeared" };
  const output = card.querySelector(".shell-output");
  if (!output || !output.textContent.includes("cmd/serf-hub/web_workspace.go")) return { ok: false, detail: "shell output missing" };
  return { ok: true };
});

await scenario("a subagents module separates surrounding cheap tool clusters", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "a.go" }) }],
  ["TOOL_CALL_END", { call_id: "r1", output: "alpha\n", tool_name: "read_file" }],
  ["JOB_FINISHED", { jobId: "job_ORPHAN", jobType: "delegate", status: "completed", outputBytes: 77, transcriptRef: "local:child" }],
  ["TOOL_CALL_START", { call_id: "g1", tool_name: "grep", arguments_json: JSON.stringify({ pattern: "TODO" }) }],
  ["TOOL_CALL_END", { call_id: "g1", output: "a.go:1:TODO\n", tool_name: "grep" }],
], ({ conv }) => {
  const children = Array.from(conv.children);
  const firstCluster = children.find(el => el.classList.contains("tool-call-cluster"));
  const mod = children.find(el => el.classList.contains("subs"));
  const clusters = children.filter(el => el.classList.contains("tool-call-cluster"));
  if (clusters.length !== 2) return { ok: false, detail: "expected two cheap clusters, got " + clusters.length };
  if (!firstCluster || !firstCluster.querySelector(".tool-call.read_file")) return { ok: false, detail: "first cheap cluster missing read_file" };
  if (!mod) return { ok: false, detail: "missing subagents module" };
  if (!clusters[1].querySelector(".tool-call.grep")) return { ok: false, detail: "second cheap cluster missing grep" };
  if (children.indexOf(firstCluster) > children.indexOf(mod) || children.indexOf(mod) > children.indexOf(clusters[1])) {
    return { ok: false, detail: "wrong order: " + children.map(el => el.className).join(" | ") };
  }
  return { ok: true };
});


await scenario("bottom task status shows progress and current task text", [
  ["SESSION_START", { session_id: "01TEST" }],
], ({ window }) => {
  window.SerfRenderer.applyTasks([
    { id: 1, description: "done task", status: "done" },
    { id: 2, description: "Implement the footer task status", status: "in_progress" },
    { id: 3, description: "later task", status: "open" },
  ]);
  const headerTasks = window.document.querySelector(".workspace-actions [data-tasks-trigger]");
  if (headerTasks) return { ok: false, detail: "tasks trigger should not be in header actions" };
  const trigger = window.document.querySelector(".task-status-row [data-tasks-trigger]");
  if (!trigger) return { ok: false, detail: "missing bottom task trigger" };
  const text = trigger.querySelector("[data-task-status-text]");
  if (!text) return { ok: false, detail: "missing task status text element" };
  if (text.textContent !== "1/3 · Implement the footer task status") return { ok: false, detail: "wrong task status text: " + text.textContent };
  const badge = trigger.querySelector(".panel-toggle-badge");
  if (!badge || badge.textContent !== "1/3") return { ok: false, detail: "missing task badge" };
  if (!/\.task-status-row\s*\{[^}]*font-family:\s*var\(--font-mono\)/.test(styleSrc)) return { ok: false, detail: "task status row should use compact mono styling" };
  if (!/\.tasks-status\s*\{[^}]*display:\s*inline-flex/.test(styleSrc)) return { ok: false, detail: "task status trigger should align key/value inline" };
  if (/\.tasks-status \.panel-toggle-badge\s*\{[^}]*display:\s*none/.test(styleSrc)) return { ok: false, detail: "task status should keep the progress badge visible" };
  return { ok: true };
});

await scenario("grep in cheap cluster exposes output and args", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "g1", tool_name: "grep_files", arguments_json: JSON.stringify({ pattern: "TODO", path: "agent", glob_filter: "*.go" }) }],
  ["TOOL_CALL_END", { call_id: "g1", output: "agent/a.go:12:TODO one\nagent/b.go:3:TODO two\n", tool_name: "grep_files" }],
], ({ conv }) => {
  const cluster = conv.querySelector(".tool-call-cluster");
  if (!cluster) return { ok: false, detail: "no cheap cluster" };
  const call = cluster.querySelector(".tool-call.grep_files");
  if (!call) return { ok: false, detail: "no grep_files tool-call" };
  if (!call.textContent.includes("2 hits")) return { ok: false, detail: "missing hit count" };
  if (!call.textContent.includes("*.go")) return { ok: false, detail: "missing grep glob filter" };
  const body = cluster.querySelector(".cheap-tool-body");
  if (!body) return { ok: false, detail: "no grep disclosure body" };
  if (!body.querySelector(".cheap-tool-args").textContent.includes("TODO")) return { ok: false, detail: "grep args missing" };
  if (!body.querySelector(".cheap-tool-output").textContent.includes("agent/a.go:12")) return { ok: false, detail: "grep output missing" };
  return { ok: true };
});

await scenario("list_dir in cheap cluster exposes entries", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "l1", tool_name: "list_dir", arguments_json: JSON.stringify({ path: "cmd/serf-hub", pattern: "*.go" }) }],
  ["TOOL_CALL_END", { call_id: "l1", output: "assets\njstest\nweb.go\n", tool_name: "list_dir" }],
], ({ conv }) => {
  const call = conv.querySelector(".tool-call.list_dir");
  if (!call) return { ok: false, detail: "no list_dir tool-call" };
  if (!call.textContent.includes("3 entries")) return { ok: false, detail: "missing entry count" };
  if (!call.textContent.includes("*.go")) return { ok: false, detail: "missing list pattern" };
  const body = call.querySelector(".cheap-tool-body");
  if (!body) return { ok: false, detail: "no list disclosure body" };
  if (!body.querySelector(".cheap-tool-args").textContent.includes("cmd/serf-hub")) return { ok: false, detail: "list args missing" };
  if (!body.querySelector(".cheap-tool-output").textContent.includes("web.go")) return { ok: false, detail: "list output missing" };
  return { ok: true };
});

// edit_file — collapses to a +N −N stat (mockup #19 alt A) with the full
// unified diff one caret-click away in the same box.
await scenario("edit_file collapses to a stat with the diff one click away", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file", arguments_json: JSON.stringify({ file_path: "x.go", old_string: "old line\nkept line", new_string: "new line\nkept line" }) }],
  ["TOOL_CALL_END", { call_id: "e1", output: "edited x.go: 1 replacement(s)", tool_name: "edit_file" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.edit_file");
  if (!card) return { ok: false, detail: "no edit tool-call" };
  if (!card.textContent.includes("x.go")) return { ok: false, detail: "missing target" };
  // Diff collapses by default: the row shows the +N −N stat, the body is one click away.
  if (card.dataset.expanded !== "false") return { ok: false, detail: "edit body should collapse by default, got data-expanded=" + card.dataset.expanded };
  const stat = card.querySelector(".result-detail");
  if (!stat || stat.textContent.trim() !== "+2 -2") return { ok: false, detail: "collapsed stat should be '+2 -2', got " + (stat && JSON.stringify(stat.textContent)) };
  const body = card.querySelector(".edit-body");
  if (!body) return { ok: false, detail: "no edit body" };
  const diff = body.querySelector(".diff-body");
  if (!diff) return { ok: false, detail: "no diff body" };
  if (diff.textContent.includes("edited x.go")) return { ok: false, detail: "edit output shown instead of diff" };
  if (!diff.textContent.includes("-old line") || !diff.textContent.includes("+new line")) return { ok: false, detail: "argument diff text missing" };
  if (!diff.querySelector(".add")) return { ok: false, detail: "no .add lines" };
  if (!diff.querySelector(".del")) return { ok: false, detail: "no .del lines" };
  if (!card.querySelector(".tool-status-good")) return { ok: false, detail: "missing left success icon" };
  // Caret button should exist and default to ▸ (collapsed); clicking expands.
  const caret = card.querySelector(".tool-expand-btn");
  if (!caret) return { ok: false, detail: "no expand caret button" };
  if (caret.textContent !== "▸") return { ok: false, detail: "caret should be ▸ when collapsed, got " + caret.textContent };
  caret.dispatchEvent(new conv.ownerDocument.defaultView.MouseEvent("click", { bubbles: true, cancelable: true }));
  if (card.dataset.expanded !== "true" || caret.textContent !== "▾") return { ok: false, detail: "caret click should expand to ▾" };
  return { ok: true };
});

await scenario("apply_patch diff body with five-line preview", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "p1", tool_name: "apply_patch", arguments_json: JSON.stringify({ patch: "*** Begin Patch\n*** Update File: x.go\n@@\n-old\n+new\n-context\n+added\n*** End Patch" }) }],
  ["TOOL_CALL_END", { call_id: "p1", output: "applied patch to:\nx.go", tool_name: "apply_patch" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.apply_patch");
  if (!card) return { ok: false, detail: "no apply_patch tool-call" };
  if (!card.textContent.includes("x.go")) return { ok: false, detail: "missing patch target" };
  const body = card.querySelector(".patch-body");
  if (!body) return { ok: false, detail: "no patch body" };
  const preview = body.querySelector(".patch-preview");
  if (!preview) return { ok: false, detail: "no patch preview" };
  if (!preview.textContent.includes("*** Begin Patch")) return { ok: false, detail: "preview should render patch content, not apply_patch stdout" };
  if (preview.textContent.includes("+added")) return { ok: false, detail: "preview should be capped to first five lines" };
  if (!preview.querySelector(".add")) return { ok: false, detail: "no patch add lines" };
  if (!preview.querySelector(".del")) return { ok: false, detail: "no patch del lines" };
  const more = body.querySelector(".patch-output-more");
  if (!more) return { ok: false, detail: "missing patch more disclosure" };
  if (body.querySelectorAll(".patch-output-more").length !== 1) return { ok: false, detail: "duplicate patch more disclosures" };
  if (!more.querySelector("summary") || !more.querySelector("summary").textContent.includes("3 more lines")) return { ok: false, detail: "wrong patch more summary" };
  more.open = true;
  more.dispatchEvent(new conv.ownerDocument.defaultView.Event("toggle"));
  if (!more.querySelector("summary").textContent.includes("hide 3 lines")) return { ok: false, detail: "open patch summary should switch to hide" };
  if (!more.querySelector(".patch-rest") || !more.querySelector(".patch-rest").textContent.includes("+added")) return { ok: false, detail: "patch rest missing hidden diff lines" };
  return { ok: true };
});

// shell — collapsible details body, exit code result.
await scenario("shell with stdout, exit code, and right-aligned timing", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "ls -la" }), startedAt: 1763714096 }],
  ["TOOL_CALL_OUTPUT_DELTA", { call_id: "s1", delta: "total 8\nfile1\nfile2\n" }],
  ["TOOL_CALL_END", { call_id: "s1", output: "total 8\nfile1\nfile2\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell", completedAt: 1763714098, durationMs: 1250 }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  if (!card.textContent.includes("ls -la")) return { ok: false, detail: "missing command" };
  // Success recedes fully: no "exit 0", and no ✓ glyph either — only failures
  // are worth the eye. The status slot stays (class) so content aligns.
  if (card.textContent.includes("exit 0")) return { ok: false, detail: "successful shell should not print 'exit 0'" };
  const sgood = card.querySelector(".tool-status-good");
  if (!sgood) return { ok: false, detail: "missing shell success status slot" };
  if (sgood.textContent.trim() !== "") return { ok: false, detail: "successful row should show NO checkmark, got " + JSON.stringify(sgood.textContent) };
  const meta = card.querySelector(".tool-meta");
  if (!meta) return { ok: false, detail: "missing tool metadata" };
  if (!meta.textContent.includes("1.3s")) return { ok: false, detail: "missing duration metadata: " + meta.textContent };
  if (!/\d{1,2}:\d{2}:\d{2}/.test(meta.textContent)) return { ok: false, detail: "missing timestamp metadata: " + meta.textContent };
  const body = conv.querySelector(".shell-body");
  if (!body) return { ok: false, detail: "no shell body" };
  const pre = body.querySelector(".shell-output");
  if (!pre || !pre.textContent.includes("file1")) return { ok: false, detail: "stdout missing" };
  return { ok: true };
});

await scenario("failed shell shows error output", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s2", tool_name: "shell", arguments_json: JSON.stringify({ command: "false" }) }],
  ["TOOL_CALL_END", { call_id: "s2", output: "", error: "boom\nstderr detail\n", tool_state: JSON.stringify({ exit_code: 1 }), tool_name: "shell" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  const sbad = card.querySelector(".tool-status-bad");
  if (!sbad || sbad.textContent.trim() !== "✕") return { ok: false, detail: "a failed row must show the ✕ glyph, got " + (sbad && JSON.stringify(sbad.textContent)) };
  const body = conv.querySelector(".shell-body");
  if (!body) return { ok: false, detail: "no failed shell body" };
  const pre = body.querySelector(".shell-output");
  if (!pre || !pre.textContent.includes("stderr detail")) return { ok: false, detail: "stderr/error output missing" };
  if (body.style.display === "none") return { ok: false, detail: "failed shell body hidden" };
  return { ok: true };
});

// The expand caret sits on the RIGHT of the header line (order 3), so the status
// glyph leads a clean, aligned left edge; the disclosure recedes to the right.
await scenario("expand caret is right-aligned; card disclosures use a right chevron", [
  ["SESSION_START", { session_id: "01TEST" }],
], () => {
  if (!/\.tool-expand-btn\s*\{[^}]*order:\s*3/.test(styleSrc)) {
    return { ok: false, detail: "expand caret must be order: 3 (right of the header line)" };
  }
  // Card disclosures (raw notification / excerpt / show raw error) put the
  // chevron on the right via a removed native marker + a right ::after.
  if (!/\.notification-card-raw > summary::after/.test(styleSrc) || !/\.notification-card-raw > summary[\s\S]{0,400}justify-content:\s*space-between/.test(styleSrc)) {
    return { ok: false, detail: "card disclosures must put a right ▸ chevron on .notification-card-raw summary" };
  }
  return { ok: true };
});

// Success is silent, but a nonzero exit is the failure signal and stays.
await scenario("nonzero exit shows 'exit N'; clean exit shows nothing", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "ok1", tool_name: "shell", arguments_json: JSON.stringify({ command: "true" }) }],
  ["TOOL_CALL_END", { call_id: "ok1", output: "done\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell" }],
  ["TOOL_CALL_START", { call_id: "bad1", tool_name: "shell", arguments_json: JSON.stringify({ command: "exit 2" }) }],
  ["TOOL_CALL_END", { call_id: "bad1", output: "nope\n", tool_state: JSON.stringify({ exit_code: 2 }), tool_name: "shell" }],
], ({ conv }) => {
  const cards = conv.querySelectorAll(".tool-call.shell");
  if (cards.length !== 2) return { ok: false, detail: "expected two shell cards, got " + cards.length };
  const okStat = cards[0].querySelector(".result-detail").textContent.trim();
  if (okStat !== "") return { ok: false, detail: "clean exit should print nothing, got " + JSON.stringify(okStat) };
  const badStat = cards[1].querySelector(".result-detail").textContent.trim();
  if (badStat !== "exit 2") return { ok: false, detail: "nonzero exit should print 'exit 2', got " + JSON.stringify(badStat) };
  return { ok: true };
});

// web_search — list of result lines.
await scenario("web_search renders top results", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "w1", tool_name: "web_search", arguments_json: JSON.stringify({ query: "go context cancellation" }) }],
  ["TOOL_CALL_END", { call_id: "w1", output: "Result A\nResult B\nResult C\n", tool_name: "web_search" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.web_search");
  if (!card) return { ok: false, detail: "no search card" };
  if (!card.textContent.includes("go context")) return { ok: false, detail: "missing query" };
  if (!card.textContent.includes("3 results")) return { ok: false, detail: "missing result count" };
  const ul = conv.querySelector(".search-body");
  if (!ul) return { ok: false, detail: "no search body" };
  const items = ul.querySelectorAll("li");
  if (items.length !== 3) return { ok: false, detail: "expected 3 result items, got " + items.length };
  return { ok: true };
});

// communicate — renders as assistant block, no tool-call card.
await scenario("communicate renders as assistant block", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "communicate", arguments_json: JSON.stringify({ message: "Hello" }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "{}", tool_name: "communicate" }],
], ({ conv }) => {
  const tc = conv.querySelector(".tool-call");
  if (tc) return { ok: false, detail: "communicate should not produce a tool-call card" };
  const am = conv.querySelector(".assistant-message");
  if (!am || !am.textContent.includes("Hello")) return { ok: false, detail: "no assistant message" };
  return { ok: true };
});

// communicate with an empty/whitespace message — should not leave a bubble.
await scenario("communicate ignores empty message", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "communicate", arguments_json: JSON.stringify({ message: "\n\n" }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "{}", tool_name: "communicate" }],
], ({ conv }) => {
  const assistants = conv.querySelectorAll(".assistant-message");
  if (assistants.length !== 0) return { ok: false, detail: "expected 0 assistant-message, got " + assistants.length };
  const tc = conv.querySelector(".tool-call");
  if (tc) return { ok: false, detail: "communicate should not produce a tool-call card" };
  return { ok: true };
});

// communicate after streamed assistant deltas — final tool start should not duplicate.
await scenario("streamed communicate is not duplicated by tool completion", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["ASSISTANT_TEXT_START", {}],
  ["ASSISTANT_TEXT_DELTA", { delta: "Hel" }],
  ["ASSISTANT_TEXT_DELTA", { delta: "lo" }],
  ["ASSISTANT_TEXT_END", {}],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "communicate", arguments_json: JSON.stringify({ message: "Hello" }) }],
  ["TOOL_CALL_END", { call_id: "c1", output: "{}", tool_name: "communicate" }],
], ({ conv }) => {
  const assistants = conv.querySelectorAll(".assistant-message");
  if (assistants.length !== 1) return { ok: false, detail: "expected 1 assistant-message, got " + assistants.length };
  if (assistants[0].textContent !== "Hello") return { ok: false, detail: "assistant text wrong: " + assistants[0].textContent };
  const tc = conv.querySelector(".tool-call");
  if (tc) return { ok: false, detail: "communicate should not produce a tool-call card" };
  return { ok: true };
});

await streamingMarkdownScenario();

// communicate after an equivalent markdown assistant message — should not
// duplicate when rendered textContent differs from raw markdown.
await scenario("markdown communicate after assistant text is not duplicated", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["ASSISTANT_TEXT_END", { text: "The harness is **serf**." }],
  ["TOOL_CALL_START", { call_id: "c1", tool_name: "communicate", arguments_json: JSON.stringify({ message: "The harness is **serf**." }) }],
  ["TOOL_CALL_END", { call_id: "c1", arguments_json: JSON.stringify({ message: "The harness is **serf**." }), output: "{}", tool_name: "communicate" }],
], ({ conv }) => {
  const assistants = conv.querySelectorAll(".assistant-message");
  if (assistants.length !== 1) return { ok: false, detail: "expected 1 assistant-message, got " + assistants.length };
  if (assistants[0].textContent.trim() !== "The harness is serf.") return { ok: false, detail: "assistant text wrong: " + assistants[0].textContent };
  const tc = conv.querySelector(".tool-call");
  if (tc) return { ok: false, detail: "communicate should not produce a tool-call card" };
  return { ok: true };
});

// Cheap-cluster summary (mockup #6 alt A): once a recon cluster is behind us
// (a non-cheap entry follows), it folds to one quiet "✓ N steps · <targets>"
// line that leads with the consequential step, hiding the individual rows.
await scenario("cheap cluster collapses to a mutating-step-first summary once done", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "r1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "cache.go" }) }],
  ["TOOL_CALL_END", { call_id: "r1", output: "package cache\n", tool_name: "read_file" }],
  ["TOOL_CALL_START", { call_id: "g1", tool_name: "grep", arguments_json: JSON.stringify({ pattern: "TokenCache", path: "." }) }],
  ["TOOL_CALL_END", { call_id: "g1", output: "cache.go:1:TokenCache\n", tool_name: "grep" }],
  // A non-cheap entry (assistant prose) ends the cluster: it is now behind us.
  ["ASSISTANT_TEXT_START", {}],
  ["ASSISTANT_TEXT_DELTA", { delta: "Found the cause." }],
], ({ conv }) => {
  const cluster = conv.querySelector(".tool-call-cluster");
  if (!cluster) return { ok: false, detail: "no cheap cluster" };
  if (!cluster.classList.contains("done")) return { ok: false, detail: "finished cluster should be marked done" };
  const summary = cluster.querySelector(".tool-cluster-summary");
  if (!summary) return { ok: false, detail: "no cluster summary line" };
  if (!/2 steps/.test(summary.textContent)) return { ok: false, detail: "summary should count steps: " + summary.textContent };
  if (!/cache\.go/.test(summary.textContent)) return { ok: false, detail: "summary should name a target: " + summary.textContent };
  // The rows are hidden behind the summary until expanded.
  const body = cluster.querySelector(".tool-cluster-body");
  if (!body) return { ok: false, detail: "no cluster body" };
  if (cluster.querySelectorAll(".tool-cluster-body .tool-call").length !== 2) return { ok: false, detail: "body should hold both rows" };
  // Clicking the summary expands the rows.
  summary.click();
  if (!cluster.classList.contains("open")) return { ok: false, detail: "summary click should open the cluster" };
  return { ok: true };
});

// Honest long output (mockup #6 alt D): when bytes are present (client-
// collapsed) the affordance reads "expand · N more" (interactive blue); when
// the daemon dropped bytes at the source the body carries an honest drop note,
// never a fake expand.
await scenario("client-collapsed long output offers an honest 'expand · N more'", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "seq 12" }) }],
  ["TOOL_CALL_END", { call_id: "s1", output: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", tool_name: "shell" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  const more = card.querySelector(".tool-output-more summary");
  if (!more) return { ok: false, detail: "no expand affordance for long output" };
  if (!/expand · 7 more lines/.test(more.textContent)) return { ok: false, detail: "wrong expand label: " + more.textContent };
  if (card.querySelector(".tool-output-dropped")) return { ok: false, detail: "present bytes must not show a drop note" };
  return { ok: true };
});

await scenario("server-truncated job output shows an honest drop note, not a fake expand", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "jr1", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_A" }) }],
  ["TOOL_CALL_END", { call_id: "jr1", tool_name: "job_read_output", output: "job_A completed, output truncated", tool_state: JSON.stringify({
    job_id: "job_A", type: "shell", status: "completed",
    output: "kept line one\nkept line two\nkept line three",
    total_bytes: 128, truncated: true,
  }) }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.job_read_output");
  if (!card) return { ok: false, detail: "no job_read_output card" };
  const drop = card.querySelector(".tool-output-dropped");
  if (!drop) return { ok: false, detail: "truncated output should carry a drop note" };
  if (!/truncated at the source/.test(drop.textContent)) return { ok: false, detail: "drop note wrong: " + drop.textContent };
  if (!/128 bytes/.test(drop.textContent)) return { ok: false, detail: "drop note should state kept bytes: " + drop.textContent };
  return { ok: true };
});

// relativizePath: tool-call target shows cwd-relative path when data-cwd is set.
await (async function () {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01CWD"></header>
    <div id="conversation-cwd" data-session-id="01CWD" data-state="ended" data-cwd="/home/user/myproject"></div>
    <form data-input-form data-session-id="01CWD"><textarea class="message-input"></textarea></form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation-cwd");
  window.SerfRenderer.init(conv);

  // Wait for renderer initialization (mirrors the pattern in scenario())
  await new Promise(r => setTimeout(r, 30));

  // Fire SESSION_START then a read_file of an absolute path under cwd
  window.SerfRenderer.handleData("SESSION_START", { session_id: "01CWD" });
  window.SerfRenderer.handleData("TOOL_CALL_START", {
    call_id: "rel1",
    tool_name: "read_file",
    arguments_json: JSON.stringify({ file_path: "/home/user/myproject/handlers/signup.go" }),
  });
  window.SerfRenderer.handleData("TOOL_CALL_END", {
    call_id: "rel1", output: "ok\n", tool_name: "read_file",
  });

  await new Promise(r => setTimeout(r, 10));

  const cluster = conv.querySelector(".tool-call-cluster");
  const call = cluster && cluster.querySelector(".tool-call.read_file");
  const targetEl = call && call.querySelector(".target");
  const targetText = targetEl ? targetEl.textContent : "(missing)";
  const ok = targetText === "handlers/signup.go";
  console.log((ok ? "PASS" : "FAIL") + " — read_file target shows cwd-relative path when data-cwd set");
  if (!ok) {
    allPass = false;
    console.log("  expected: handlers/signup.go  got: " + targetText);
    if (conv.innerHTML.length < 800) console.log("  HTML: " + conv.innerHTML);
  }
})();

// relativizePath: home-dir substitution and middle-truncation (vh63).
await (async function () {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01HOME"></header>
    <div id="conversation-home"
         data-session-id="01HOME"
         data-state="ended"
         data-cwd="/home/user/myproject"
         data-home="/home/user"></div>
    <form data-input-form data-session-id="01HOME"><textarea class="message-input"></textarea></form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation-home");
  window.SerfRenderer.init(conv);
  await new Promise(r => setTimeout(r, 30));

  const failures = [];
  const check = (desc, got, want) => {
    if (got !== want) failures.push(desc + ": expected " + JSON.stringify(want) + " got " + JSON.stringify(got));
  };

  const R = window.SerfRenderer;

  // Under cwd: stays relative (unchanged from before).
  check("under cwd", R.relativizePath("/home/user/myproject/src/main.go"), "src/main.go");

  // Under home but outside cwd: tilde substitution.
  check("under home outside cwd", R.relativizePath("/home/user/dotfiles/bashrc"), "~/.files/bashrc".replace("/.files/", "/dotfiles/"));
  // Simpler: just check it starts with ~/
  const hpath = R.relativizePath("/home/user/.config/app/settings.json");
  check("home prefix becomes ~", hpath.startsWith("~/"), true);
  check("home path value", hpath, "~/.config/app/settings.json");

  // Path outside both cwd and home, length > 96: middle truncate, but keep more
  // context than the old 40-character cap.
  const longPath = "/var/log/some/deeply/nested/application/service/output/with/a/very/long/component/name/output.log";
  const truncated = R.relativizePath(longPath);
  check("moderately long path is not truncated", R.relativizePath("/var/log/some/deeply/nested/application/service/output.log"), "/var/log/some/deeply/nested/application/service/output.log");
  check("long path truncated length <= 96", truncated.length <= 96, true);
  check("long path has ellipsis", truncated.includes("…"), true);
  check("long path ends with basename", truncated.endsWith("output.log"), true);

  // Short path outside cwd/home: returned unchanged.
  const shortPath = "/etc/hosts";
  check("short outside path unchanged", R.relativizePath(shortPath), "/etc/hosts");

  if (failures.length === 0) {
    console.log("PASS — relativizePath home-dir tilde and middle-truncation (vh63)");
  } else {
    allPass = false;
    for (const f of failures) console.log("FAIL — " + f);
  }
})();

// x1gj: tool-call bodies collapse by default; caret toggles. Diffs (edit/write/
// patch) also collapse to a +N −N stat (mockup #19 alt A).
await (async function () {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("SESSION_START", { session_id: "01TEST" });

  // read_file should be collapsed by default.
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "x1", tool_name: "read_file", arguments_json: JSON.stringify({ file_path: "foo.go" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "x1", output: "package main\n", tool_name: "read_file" });

  // write_file collapses by default too (mockup #19 alt A): the +N −N stat
  // leads, the diff is one caret-click away.
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: "x2", tool_name: "write_file", arguments_json: JSON.stringify({ file_path: "bar.go", content: "+package bar\n" }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: "x2", output: "written", tool_name: "write_file" });

  await new Promise(r => setTimeout(r, 10));

  const failures = [];
  const check = (desc, got, want) => {
    if (got !== want) failures.push(desc + ": expected " + JSON.stringify(want) + " got " + JSON.stringify(got));
  };

  // read_file: data-expanded="false", caret shows ▸.
  const readCall = conv.querySelector(".tool-call.read_file");
  check("read_file data-expanded false", readCall && readCall.dataset.expanded, "false");
  const readCaret = readCall && readCall.querySelector(".tool-expand-btn");
  check("read_file caret exists", !!readCaret, true);
  check("read_file caret glyph collapsed", readCaret && readCaret.textContent, "▸");

  // write_file: collapsed by default (alt A), data-expanded="false", caret ▸.
  const writeCall = conv.querySelector(".tool-call.write_file");
  check("write_file data-expanded false", writeCall && writeCall.dataset.expanded, "false");
  const writeCaret = writeCall && writeCall.querySelector(".tool-expand-btn");
  check("write_file caret glyph collapsed", writeCaret && writeCaret.textContent, "▸");

  // Click read_file caret to expand.
  if (readCaret) {
    readCaret.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    check("read_file expanded after click", readCall.dataset.expanded, "true");
    check("read_file caret glyph after expand", readCaret.textContent, "▾");
    // Click again to collapse.
    readCaret.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    check("read_file collapsed after second click", readCall.dataset.expanded, "false");
    check("read_file caret glyph after collapse", readCaret.textContent, "▸");
  }

  // Keyboard: Enter on caret triggers expand.
  if (readCaret) {
    const enterEvt = new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    readCaret.dispatchEvent(enterEvt);
    // keydown listener calls .click(), which toggles.
    check("read_file expanded after Enter", readCall.dataset.expanded, "true");
  }

  if (failures.length === 0) {
    console.log("PASS — tool-call body collapsed by default; caret toggles; edit/write/patch collapse to a +N −N stat (x1gj)");
  } else {
    allPass = false;
    for (const f of failures) console.log("FAIL — " + f);
  }
})();

process.exit(allPass ? 0 : 1);
})();
