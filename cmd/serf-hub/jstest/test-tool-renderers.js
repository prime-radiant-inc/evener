// Smoke-test the per-tool renderer registry: shell with stdout,
// edit_file with diff, web_fetch, web_search, spawn_agent, and the
// cheap-cluster grouping for read_file/grep/list_dir/glob.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_PATH = "../assets/renderer.js";
const rendererSrc = fs.readFileSync(RENDERER_PATH, "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
    <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  window.eval(rendererSrc);
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
  const purpose = body.querySelector(".read-tool-purpose");
  if (!purpose || purpose.textContent !== "Inspect main entry point.") return { ok: false, detail: "read purpose missing" };
  if (!purpose.classList.contains("read-tool-purpose")) return { ok: false, detail: "read purpose class missing" };
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

// edit_file — should produce a diff body.
await scenario("edit_file diff body", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file", arguments_json: JSON.stringify({ file_path: "x.go" }) }],
  ["TOOL_CALL_OUTPUT_DELTA", { call_id: "e1", delta: "+ added line\n- removed line\n" }],
  ["TOOL_CALL_END", { call_id: "e1", output: "+ added line\n- removed line\n", tool_name: "edit_file" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.edit_file");
  if (!card) return { ok: false, detail: "no edit tool-call" };
  if (!card.textContent.includes("x.go")) return { ok: false, detail: "missing target" };
  const disclosure = card.querySelector("details.edit-body");
  if (!disclosure) return { ok: false, detail: "no edit disclosure" };
  if (!disclosure.querySelector("summary") || !disclosure.querySelector("summary").textContent.includes("edit")) return { ok: false, detail: "no edit summary" };
  const diff = disclosure.querySelector(".diff-body");
  if (!diff) return { ok: false, detail: "no diff body" };
  if (!diff.querySelector(".add")) return { ok: false, detail: "no .add lines" };
  if (!diff.querySelector(".del")) return { ok: false, detail: "no .del lines" };
  if (!card.querySelector(".tool-status-good")) return { ok: false, detail: "missing left success icon" };
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
  if (!more.querySelector(".patch-rest") || !more.querySelector(".patch-rest").textContent.includes("+added")) return { ok: false, detail: "patch rest missing hidden diff lines" };
  return { ok: true };
});

// shell — collapsible details body, exit code result.
await scenario("shell with stdout and exit code", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "ls -la" }) }],
  ["TOOL_CALL_OUTPUT_DELTA", { call_id: "s1", delta: "total 8\nfile1\nfile2\n" }],
  ["TOOL_CALL_END", { call_id: "s1", output: "total 8\nfile1\nfile2\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  if (!card.textContent.includes("ls -la")) return { ok: false, detail: "missing command" };
  if (!card.textContent.includes("exit 0")) return { ok: false, detail: "missing exit code" };
  if (!card.querySelector(".tool-status-good")) return { ok: false, detail: "missing shell success icon" };
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
  if (!card.querySelector(".tool-status-bad")) return { ok: false, detail: "missing shell error icon" };
  const body = conv.querySelector(".shell-body");
  if (!body) return { ok: false, detail: "no failed shell body" };
  const pre = body.querySelector(".shell-output");
  if (!pre || !pre.textContent.includes("stderr detail")) return { ok: false, detail: "stderr/error output missing" };
  if (body.style.display === "none") return { ok: false, detail: "failed shell body hidden" };
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

process.exit(allPass ? 0 : 1);
})();
