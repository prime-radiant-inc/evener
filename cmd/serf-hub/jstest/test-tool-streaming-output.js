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

// Past the 8000-char budget, STREAMING keeps the live tail (a head-clip
// freezes the pane — the tail stops moving and the command looks stalled).
const marker = "HEAD-MARKER" + "y".repeat(9000) + "TAIL-MARKER";
shell.bodyDelta(state, marker);
pass(pre.textContent.length === 8000, "streaming caps the pane at 8000 chars");
pass(pre.textContent.endsWith("TAIL-MARKER"), "streaming shows the LAST 8000 chars (live tail)");
pass(!pre.textContent.startsWith("HEAD-MARKER"), "the head scrolls off while streaming");
// bodyEnd keeps the existing head-based clip + fold semantics.
shell.bodyEnd(state, { tool_state: JSON.stringify({ exit_code: 0 }) }, marker);
pass(state.body.pre.textContent.startsWith("HEAD-MARKER"), "bodyEnd keeps the head-based clip");
pass(state.body.pre.textContent.includes("…"), "bodyEnd marks the truncation");

// The read renderer shares the live-tail streaming behavior.
const read = (window.SerfRendererInternal.toolRendererFor || window.toolRendererFor)("read_file", {});
const readHost = window.document.createElement("div");
const readState = { body: read.body({ file_path: "/f" }, readHost), el: readHost };
read.bodyDelta(readState, marker);
const readPre = readState.body.outputPre;
pass(readPre.textContent.length === 8000 && readPre.textContent.endsWith("TAIL-MARKER"),
  "read streams the live tail past 8000 chars too");
read.bodyEnd(readState, { output: marker }, marker);
pass(readState.body.wrap.querySelector(".read-tool-preview").textContent.startsWith("HEAD-MARKER"),
  "read bodyEnd keeps the head-based clip");

if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
console.log("PASS: shell output streams append-only; fold builds at bodyEnd");
process.exit(0);
