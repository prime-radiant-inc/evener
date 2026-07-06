"use strict";
// Regression: the living plan/task card's per-row status glyph
// (planGlyphForStatus, feeding buildTaskRowLine's .plan-glyph span) used
// literal text characters ("✓"/"⟳"/"✕") instead of the unified SerfIcons set
// adopted elsewhere (sidebar dots, notifications, connection banner, subagent
// glyphs — see Task 7/9-13). planGlyphForStatus must return icon markup for
// the unified states (done/in_progress/cancelled) while leaving "pending" as
// a literal "○" — it is not one of the unified states.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM("<!doctype html><html><body></body></html>", {
  runScripts: "outside-only",
  pretendToBeVisual: true,
});
const { window } = dom;
window.marked = { parse: (t) => t };
require("./load-renderer.js").evalRenderer(window);

const r = window.SerfRendererFormatInternal || window.SerfRendererInternal;
assert.ok(r.planGlyphForStatus("done").includes("<svg"), "done plan glyph must be an svg icon");
assert.ok(r.planGlyphForStatus("in_progress").includes("<svg"), "in_progress plan glyph must be an svg icon");
assert.ok(r.planGlyphForStatus("cancelled").includes("<svg"), "cancelled plan glyph must be an svg icon");
assert.strictEqual(r.planGlyphForStatus("pending"), "○", "pending (default) stays a neutral literal circle — not a unified-vocabulary state");

console.log("test-renderer-format-plan-glyphs.js: OK");
