// Streaming shell output updates one <pre> in place (no fold rebuild per
// delta); the fold chrome appears once at bodyEnd.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
require("./load-renderer").evalRenderer(window);
const { toolRendererFor } = window.SerfRendererInternal || {};
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const shell = (window.SerfRendererInternal.toolRendererFor || window.toolRendererFor)("shell", {});
const host = window.document.createElement("div");
const state = { body: shell.body({ command: "make test" }, host), el: host };

shell.bodyDelta(state, "line1\nline2");
const pre = state.body.pre;
pass(pre && pre.textContent === "line1\nline2", "delta writes the pre directly");
pass(!state.body.wrap.querySelector(".tool-output-more"), "no fold chrome while streaming");
pass(state.body.wrap.classList.contains("streaming"), "wrap carries .streaming for the CSS clamp");
const moreLines = Array.from({ length: 20 }, (_, i) => "l" + i).join("\n");
shell.bodyDelta(state, moreLines);
pass(!state.body.wrap.querySelector(".tool-output-more"), "still no fold after >5 lines stream in");
shell.bodyEnd(state, { tool_state: JSON.stringify({ exit_code: 0 }) }, moreLines);
pass(!state.body.wrap.classList.contains("streaming"), "bodyEnd drops .streaming");
pass(!!state.body.wrap.querySelector(".tool-output-more"), "fold chrome built once at bodyEnd");
pass(state.body.pre.textContent.split("\n").length === 5, "head shows the first 5 lines");

if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
console.log("PASS: shell output streams append-only; fold builds at bodyEnd");
process.exit(0);
