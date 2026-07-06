// Regression: per-subagent-row status glyphs and the subagent tally line used
// literal text characters ("✓"/"✕"/"⟳") instead of the unified SerfIcons set
// adopted elsewhere in renderer.js (sidebar dots, notifications, connection
// banner — see Task 7/9-12). subagentGlyph must return icon markup for the
// unified states (done/failed/running) while leaving "unknown" as a literal
// "?" — it is a subagent tool-status kind, not one of the unified states.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
require("./load-renderer").evalRenderer(window);

const r = window.SerfRenderer;
assert.ok(r.subagentGlyph("done").includes("<svg"), "done glyph must be an svg icon");
assert.ok(r.subagentGlyph("failed").includes("<svg"), "failed glyph must be an svg icon");
assert.ok(r.subagentGlyph("running").includes("<svg"), "running glyph must be an svg icon");
assert.strictEqual(r.subagentGlyph("unknown"), "?", "unknown stays a literal ? — not a unified-vocabulary state");

console.log("test-renderer-subagent-glyphs.js: OK");
process.exit(0);
