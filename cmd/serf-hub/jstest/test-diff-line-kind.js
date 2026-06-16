// Tests for per-line data-line-kind attributes on diff body spans (kata 4xbf).
// renderDiffLines must emit spans with data-line-kind="add"|"del"|"hunk"|"ctx".
const fs = require("fs");
const { JSDOM } = require("jsdom");


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
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
function check(name, ok, detail) {
  console.log((ok ? "PASS" : "FAIL") + " — " + name);
  if (!ok) {
    allPass = false;
    console.log("  detail: " + detail);
  }
}

async function scenario(name, eventSeq, fn) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  for (const [t, d] of eventSeq) window.SerfRenderer.handleData(t, d);
  await new Promise(r => setTimeout(r, 10));
  fn({ conv, window });
}

(async () => {

// edit_file: each diff line span must have data-line-kind set.
await scenario("edit_file diff spans have data-line-kind", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file",
    arguments_json: JSON.stringify({ file_path: "main.go", old_string: "old line\nkept", new_string: "new line\nkept" }) }],
  ["TOOL_CALL_END", { call_id: "e1", output: "edited main.go", tool_name: "edit_file" }],
], ({ conv }) => {
  const diff = conv.querySelector(".diff-body");
  if (!diff) { check("diff body exists", false, "no .diff-body"); return; }
  const spans = Array.from(diff.querySelectorAll("span"));
  check("diff has spans", spans.length > 0, "no spans in diff body");
  const withKind = spans.filter(s => s.dataset.lineKind);
  check("all spans have data-line-kind", withKind.length === spans.length,
    withKind.length + "/" + spans.length + " spans have data-line-kind");
  const addSpan = spans.find(s => s.dataset.lineKind === "add");
  check("at least one add span", !!addSpan, "no span with data-line-kind=add");
  const delSpan = spans.find(s => s.dataset.lineKind === "del");
  check("at least one del span", !!delSpan, "no span with data-line-kind=del");
});

// write_file with diff-like output: + lines become add, others ctx.
// write_file uses diffRenderer() which feeds the tool output into renderDiff.
await scenario("write_file diff spans: + lines get kind=add when output has diff format", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "w1", tool_name: "write_file",
    arguments_json: JSON.stringify({ file_path: "out.go", content: "hello\n" }) }],
  ["TOOL_CALL_END", { call_id: "w1", output: "+added line\n context line\n", tool_name: "write_file" }],
], ({ conv }) => {
  const diff = conv.querySelector(".diff-body");
  if (!diff) { check("write_file has diff-body", false, "no .diff-body"); return; }
  const spans = Array.from(diff.querySelectorAll("span"));
  const addSpans = spans.filter(s => s.dataset.lineKind === "add");
  check("write_file + line has kind=add", addSpans.length >= 1, "no add spans; kinds=" + spans.map(s => s.dataset.lineKind));
  const ctxSpans = spans.filter(s => s.dataset.lineKind === "ctx");
  check("write_file context line has kind=ctx", ctxSpans.length >= 1, "no ctx spans");
});

// apply_patch: @@ lines become hunk kind.
await scenario("apply_patch @@ lines get kind=hunk", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "p1", tool_name: "apply_patch",
    arguments_json: JSON.stringify({ patch: "*** Begin Patch\n*** Update File: x.go\n@@ -1,2 +1,2 @@\n-old\n+new\n*** End Patch" }) }],
  ["TOOL_CALL_END", { call_id: "p1", output: "applied", tool_name: "apply_patch" }],
], ({ conv }) => {
  const preview = conv.querySelector(".patch-preview");
  if (!preview) { check("apply_patch preview exists", false, "no .patch-preview"); return; }
  const spans = Array.from(preview.querySelectorAll("span"));
  const hunkSpans = spans.filter(s => s.dataset.lineKind === "hunk");
  check("apply_patch @@ lines get kind=hunk", hunkSpans.length >= 1,
    "no hunk spans; kinds=" + spans.map(s => s.dataset.lineKind));
  const addSpan = spans.find(s => s.dataset.lineKind === "add");
  check("apply_patch + line gets kind=add", !!addSpan, "no add span in patch preview");
  const delSpan = spans.find(s => s.dataset.lineKind === "del");
  check("apply_patch - line gets kind=del", !!delSpan, "no del span in patch preview");
});

// --- header lines (+++/---) must NOT get add/del kind.
await scenario("--- and +++ header lines do not get add/del kind", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e2", tool_name: "edit_file",
    arguments_json: JSON.stringify({ file_path: "z.go", old_string: "x", new_string: "y" }) }],
  ["TOOL_CALL_END", { call_id: "e2", output: "ok", tool_name: "edit_file" }],
], ({ conv }) => {
  const diff = conv.querySelector(".diff-body");
  if (!diff) { check("diff body exists for header test", false, "no .diff-body"); return; }
  const spans = Array.from(diff.querySelectorAll("span"));
  // If there are --- / +++ header spans they must not be kind=del or kind=add.
  const headerDel = spans.find(s => s.textContent.startsWith("---") && s.dataset.lineKind === "del");
  check("--- header is not kind=del", !headerDel, "--- header span incorrectly tagged as del");
  const headerAdd = spans.find(s => s.textContent.startsWith("+++") && s.dataset.lineKind === "add");
  check("+++ header is not kind=add", !headerAdd, "+++ header span incorrectly tagged as add");
});

process.exit(allPass ? 0 : 1);
})();
