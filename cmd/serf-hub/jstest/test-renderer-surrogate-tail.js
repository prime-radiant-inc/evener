// The 8KB live/final tail slices (shell + read_file, streaming AND bodyEnd)
// must never begin on a low surrogate: a slice that starts mid-pair renders
// U+FFFD as the first visible char. Fixture: "A" + "😀" + "x".repeat(7999)
// is 8002 code units, so slice(-8000) starts exactly on 😀's low surrogate.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
require("./load-renderer").evalRenderer(window);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const out = "A" + "😀" + "x".repeat(7999);
pass(out.length === 8002, "fixture is 8002 code units (slice(-8000) lands on the low surrogate)");
pass(out.charCodeAt(2) >= 0xDC00 && out.charCodeAt(2) <= 0xDFFF, "fixture code unit 2 is a low surrogate");

const startsClean = (text, label) => {
  const code = text.charCodeAt(0);
  pass(!(code >= 0xDC00 && code <= 0xDFFF), label + " begins with a lone low surrogate (renders U+FFFD)");
  pass(code !== 0xFFFD, label + " begins with U+FFFD");
  pass(code === "x".charCodeAt(0), label + " should begin on the next whole code unit ('x'), got U+" + code.toString(16));
};

// ── shell ──────────────────────────────────────────────────────────────────
const shell = window.SerfRendererInternal.toolRendererFor("shell", {});
const shellHost = window.document.createElement("div");
const shellState = { body: shell.body({ command: "yes" }, shellHost), el: shellHost };

shell.bodyDelta(shellState, out); // streaming live tail
startsClean(shellState.body.pre.textContent, "shell streaming tail");
pass(shellState.body.pre.textContent.endsWith("x".repeat(100)), "shell streaming tail keeps the tail bytes");

shell.bodyEnd(shellState, { tool_state: JSON.stringify({ exit_code: 0 }) }, out); // post-bodyEnd fold
const shellPre = shellState.body.pre.textContent;
const shellMore = shellState.body.wrap.querySelector(".tool-output-more pre");
const shellFull = shellPre + (shellMore ? "\n" + shellMore.textContent : "");
pass(shellFull.startsWith("…\n"), "shell bodyEnd keeps the elision prefix");
startsClean(shellFull.slice(2), "shell bodyEnd tail");
pass(!shellFull.includes("😀"), "shell bodyEnd elides the split head bytes");

// ── read_file ──────────────────────────────────────────────────────────────
const read = window.SerfRendererInternal.toolRendererFor("read_file", {});
const readHost = window.document.createElement("div");
const readState = { body: read.body({ file_path: "/f" }, readHost), el: readHost };

read.bodyDelta(readState, out); // streaming live tail
startsClean(readState.body.outputPre.textContent, "read_file streaming tail");

read.bodyEnd(readState, { output: out }, out); // post-bodyEnd fold
const readPre = readState.body.wrap.querySelector(".read-tool-preview").textContent;
const readMore = readState.body.wrap.querySelector(".read-tool-more pre");
const readFull = readPre + (readMore ? "\n" + readMore.textContent : "");
pass(readFull.startsWith("…\n"), "read_file bodyEnd keeps the elision prefix");
startsClean(readFull.slice(2), "read_file bodyEnd tail");

// ── shared helper contract ─────────────────────────────────────────────────
const { tailSlice } = window.SerfRendererInternal;
pass(typeof tailSlice === "function", "shared surrogate-safe tailSlice helper is exported");
if (typeof tailSlice === "function") {
  pass(tailSlice("short", 8000) === "short", "tailSlice returns short text untouched");
  pass(tailSlice(out, 8000) === "x".repeat(7999), "tailSlice drops the orphaned low surrogate");
  pass(tailSlice("ab😀cd", 3) === "cd",
    "tailSlice never starts on a low surrogate (got " + JSON.stringify(tailSlice("ab😀cd", 3)) + ")");
}

if (failures.length) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: 8KB tail slices are surrogate-safe for shell and read_file, streaming and post-bodyEnd");
process.exit(0);
