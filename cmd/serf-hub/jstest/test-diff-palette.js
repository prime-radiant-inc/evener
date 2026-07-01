// Diff/patch rendering (mockup #19, Alt A): an edit collapses to a one-line
// "+N −N" stat that expands to the full unified diff in the one box. The diff
// add/del palette is DESATURATED and deliberately separate from the semantic
// error/success tokens — a removed line is not an error, an added line is not
// good news. The +/− gutter sign carries the meaning; color only reinforces.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const STYLE_PATH = "../assets/style.css";
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
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

(async () => {

// edit_file collapses to a +N −N stat; expanding shows the classed diff lines.
await scenario("edit_file collapses to +N −N stat and expands to a unified diff", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e1", tool_name: "edit_file", arguments_json: JSON.stringify({
    file_path: "internal/auth/cache.go",
    old_string: "if now.Sub(e.lastUsed) > c.sweepInterval {\n\tdelete(c.entries, k)",
    new_string: "if now.After(e.expiresAt) {\n\tdelete(c.entries, k)\n\tc.metrics.Evicted++",
  }) }],
  ["TOOL_CALL_END", { call_id: "e1", tool_name: "edit_file", output: "edited cache.go: 1 replacement(s)" }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.edit_file");
  if (!card) return { ok: false, detail: "no edit tool-call" };
  // Collapsed by default — the diff body is hidden until expanded.
  if (card.dataset.expanded !== "false") return { ok: false, detail: "edit should collapse by default, got data-expanded=" + card.dataset.expanded };
  const caret = card.querySelector(".tool-disclosure[data-expand-toggle]");
  if (!caret || caret.textContent !== "▸") return { ok: false, detail: "caret should be ▸ when collapsed, got " + (caret && caret.textContent) };

  // The collapsed line carries the +N −N stat (computed from the diff): the
  // change is 2 removed lines, 3 added lines. The +++/--- path headers must NOT
  // be counted in the stat.
  const stat = card.querySelector(".result-detail");
  if (!stat || stat.textContent.trim() !== "+3 -2") {
    return { ok: false, detail: "collapsed stat should be '+3 -2' (headers excluded), got " + (stat && JSON.stringify(stat.textContent)) };
  }

  // The full unified diff exists in the one box (hidden, but present) with the
  // classed lines reusing renderDiff.
  const diff = card.querySelector(".diff-body");
  if (!diff) return { ok: false, detail: "no diff body" };
  if (!diff.querySelector(".add")) return { ok: false, detail: "no add lines in diff body" };
  if (!diff.querySelector(".del")) return { ok: false, detail: "no del lines in diff body" };

  // Expand: clicking the caret reveals the body (data-expanded flips true).
  caret.dispatchEvent(new conv.ownerDocument.defaultView.MouseEvent("click", { bubbles: true, cancelable: true }));
  if (card.dataset.expanded !== "true") return { ok: false, detail: "caret click should expand the diff" };
  if (caret.textContent !== "▾") return { ok: false, detail: "caret should be ▾ when expanded" };
  return { ok: true };
});

// The diff palette is its own desaturated pair, NOT the semantic error/success
// tokens. Assert via the stylesheet: .add/.del bind to diff tokens, never
// --error / --success.
await scenario("diff add/del palette is desaturated and NOT the semantic error/success tokens", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "e2", tool_name: "edit_file", arguments_json: JSON.stringify({
    file_path: "x.go", old_string: "old line", new_string: "new line",
  }) }],
  ["TOOL_CALL_END", { call_id: "e2", tool_name: "edit_file", output: "ok" }],
], ({ conv, window }) => {
  // Verify the renderer actually produced .add/.del spans in the DOM — without
  // this, removing the class assignments from renderDiffLines() would escape.
  const diff = conv.querySelector(".diff-body");
  if (!diff) return { ok: false, detail: "no diff body rendered" };
  if (!diff.querySelector(".add")) return { ok: false, detail: "no .add spans in rendered diff body" };
  if (!diff.querySelector(".del")) return { ok: false, detail: "no .del spans in rendered diff body" };

  // The diff tokens must be defined.
  for (const tok of ["--diff-add-gutter", "--diff-del-gutter", "--diff-add-bg", "--diff-del-bg"]) {
    if (styleSrc.indexOf(tok + ":") < 0 && styleSrc.indexOf(tok + " :") < 0) {
      return { ok: false, detail: "missing diff token " + tok };
    }
  }
  // Add/del text colors must bind to the diff tokens, not --error / --success.
  const addRule = styleSrc.match(/\.diff-body \.add\s*\{[^}]*\}/);
  const delRule = styleSrc.match(/\.diff-body \.del\s*\{[^}]*\}/);
  if (!addRule || !delRule) return { ok: false, detail: "missing .diff-body .add/.del rules" };
  if (/var\(--success\)/.test(addRule[0])) return { ok: false, detail: ".add must not use --success: " + addRule[0] };
  if (/var\(--error\)/.test(delRule[0])) return { ok: false, detail: ".del must not use --error: " + delRule[0] };
  if (!/var\(--diff-add/.test(addRule[0])) return { ok: false, detail: ".add should use a --diff-add token: " + addRule[0] };
  if (!/var\(--diff-del/.test(delRule[0])) return { ok: false, detail: ".del should use a --diff-del token: " + delRule[0] };
  // Background bars also off semantic colors.
  const addBg = styleSrc.match(/\.diff-body span\[data-line-kind="add"\]\s*\{[^}]*\}/);
  const delBg = styleSrc.match(/\.diff-body span\[data-line-kind="del"\]\s*\{[^}]*\}/);
  if (addBg && /var\(--success\)/.test(addBg[0])) return { ok: false, detail: "add bg must not use --success: " + addBg[0] };
  if (delBg && /var\(--error\)/.test(delBg[0])) return { ok: false, detail: "del bg must not use --error: " + delBg[0] };

  // Resolved computed colors: the diff gutter color must differ from --error.
  // Resolve both tokens off the document root and compare.
  const root = window.document.documentElement;
  // jsdom doesn't compute custom-property cascades, so resolve from the source
  // hex values we asserted are defined above.
  const errMatch = styleSrc.match(/--error:\s*(#[0-9a-fA-F]+)/);
  const delGutMatch = styleSrc.match(/--diff-del-gutter:\s*(#[0-9a-fA-F]+)/);
  const succMatch = styleSrc.match(/--success:\s*(#[0-9a-fA-F]+)/);
  const addGutMatch = styleSrc.match(/--diff-add-gutter:\s*(#[0-9a-fA-F]+)/);
  if (!errMatch || !delGutMatch || errMatch[1].toLowerCase() === delGutMatch[1].toLowerCase()) {
    return { ok: false, detail: "del gutter must differ from --error" };
  }
  if (!succMatch || !addGutMatch || succMatch[1].toLowerCase() === addGutMatch[1].toLowerCase()) {
    return { ok: false, detail: "add gutter must differ from --success" };
  }
  // Touch root so the variable is unused-lint-clean (jsdom has no real cascade).
  void root;
  return { ok: true };
});

process.exit(allPass ? 0 : 1);
})();
