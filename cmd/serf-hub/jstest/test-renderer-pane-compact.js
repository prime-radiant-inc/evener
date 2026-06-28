// Compact rendering for sessions shown in a pane iframe.
//
// When the renderer detects it is running inside a framed context
// (window.self !== window.top), it adds the "pane-compact" class to
// document.body. CSS scoped under .pane-compact applies denser layout.
// In compact mode, cheap-tool clusters are marked with [data-compact] so
// CSS collapses them by default (body hidden, summary shown).
//
// Tests:
//   1. pane-compact class applied when framed (isInPane returns true)
//   2. pane-compact class NOT applied when not framed (isInPane returns false)
//   3. In compact mode: new cheap-tool clusters carry [data-compact]
//   4. In non-compact mode: cheap-tool clusters do NOT carry [data-compact]
//   5. pane-compact CSS scope exists in style.css

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── helpers ──────────────────────────────────────────────────────────────────

function makeDOM() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <main id="workspace">
      <header class="workspace-header" data-session-id="01PANE"><div class="workspace-title-row"><span class="title">Pane child</span></div></header>
      <div class="conversation" id="conversation"
           data-session-id="01PANE" data-state="ended"></div>
      <form class="workspace-input" data-input-form data-session-id="01PANE">
        <textarea class="message-input"></textarea>
        <button type="button" data-steer-trigger>steer <kbd>⇧↵</kbd></button><button class="send-btn" type="submit">send <kbd>⌘↵</kbd></button>
      </form>
    </main>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({
    ok: true,
    json: () => Promise.resolve([]),
    text: () => Promise.resolve(""),
  });

  require("./load-renderer").evalRenderer(window);
  return dom;
}

// ── test 1: framed → pane-compact applied ────────────────────────────────────
(function testFramedAppliesClass() {
  const dom = makeDOM();
  const { window } = dom;
  // Stub isInPane to simulate framed context without touching window.top.
  window.SerfRenderer.isInPane = () => true;

  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  pass(
    window.document.body.classList.contains("pane-compact"),
    "framed context should add pane-compact class to body"
  );
})();

// ── test 2: non-framed → pane-compact NOT applied ────────────────────────────
(function testNonFramedNoClass() {
  const dom = makeDOM();
  const { window } = dom;
  window.SerfRenderer.isInPane = () => false;

  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  pass(
    !window.document.body.classList.contains("pane-compact"),
    "non-framed context must NOT add pane-compact class to body"
  );
})();

// ── tests 3 & 4: compact cluster data-compact attribute ──────────────────────
async function testClusterCompact() {
  // --- compact mode (framed) ---
  {
    const dom = makeDOM();
    const { window } = dom;
    window.SerfRenderer.isInPane = () => true;

    const conv = window.document.getElementById("conversation");
    window.SerfRenderer.init(conv);
    await new Promise(r => setTimeout(r, 30)); // flush cold-load buffer

    window.SerfRenderer.handleData("TOOL_CALL_START", {
      call_id: "c1", tool_name: "read_file",
      arguments_json: JSON.stringify({ file_path: "/foo/bar.go" }),
    });

    const cluster = conv.querySelector(".tool-call-cluster");
    pass(!!cluster, "compact: a cheap tool cluster should be created");
    pass(
      cluster && cluster.hasAttribute("data-compact"),
      "compact: cheap-tool cluster must carry [data-compact] attribute"
    );
  }

  // --- non-compact mode (non-framed) ---
  {
    const dom = makeDOM();
    const { window } = dom;
    window.SerfRenderer.isInPane = () => false;

    const conv = window.document.getElementById("conversation");
    window.SerfRenderer.init(conv);
    await new Promise(r => setTimeout(r, 30));

    window.SerfRenderer.handleData("TOOL_CALL_START", {
      call_id: "c2", tool_name: "read_file",
      arguments_json: JSON.stringify({ file_path: "/foo/baz.go" }),
    });

    const cluster = conv.querySelector(".tool-call-cluster");
    pass(!!cluster, "non-compact: a cheap tool cluster should be created");
    pass(
      cluster && !cluster.hasAttribute("data-compact"),
      "non-compact: cheap-tool cluster must NOT carry [data-compact]"
    );
  }
}

// ── test 5: CSS scope presence ────────────────────────────────────────────────
(function testCSSScope() {
  const cssPath = path.resolve(__dirname, "../assets/style.css");
  const css = fs.readFileSync(cssPath, "utf8");
  pass(css.includes(".pane-compact"), "style.css must define a .pane-compact scope");
  pass(css.includes(".pane-compact .workspace-header"), "style.css must trim .workspace-header in pane-compact");
  pass(/\.pane-compact \.workspace-title-row\s*\{[^}]*display:\s*none/.test(css), "style.css must hide inner title row in pane-compact");
  pass(/\.pane-compact kbd\s*\{[^}]*display:\s*none/.test(css), "style.css must hide composer hotkey labels in pane-compact");
  pass(css.includes(".pane-compact .conversation"), "style.css must tighten .conversation in pane-compact");
  pass(
    css.includes(".pane-compact .tool-call-cluster[data-compact]"),
    "style.css must collapse [data-compact] clusters in pane-compact"
  );
})();

// ── run async tests then report ───────────────────────────────────────────────
testClusterCompact().then(() => {
  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: pane-compact rendering (framed detection, CSS scope, cluster collapse)");
  process.exit(0);
});
