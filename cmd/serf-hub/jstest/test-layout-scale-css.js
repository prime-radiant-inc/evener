// Width scale: one measure for prose, a wider machine bleed, capped container.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");

function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/--measure:\s*720px;/.test(css), "--measure: 720px defined on :root");
assert(/--measure-machine:\s*1000px;/.test(css), "--measure-machine: 1000px defined on :root");
assert(/--workspace-content-max-w:\s*var\(--measure-machine\);/.test(css),
  "workspace content cap follows --measure-machine");
assert(!/832px/.test(css), "shipped 832px cap is gone");
// Prose rows default to the measure; machine rows bleed right to the container.
assert(/\.conversation\s*>\s*\*\s*\{[^}]*max-width:\s*var\(--measure\)/.test(css),
  "conversation children default to the prose measure");
const bleed = css.match(/\.conversation\s*>\s*\.tool-call[\s\S]*?\{[^}]*max-width:\s*none/);
assert(bleed, "machine rows (.tool-call, …) bleed past the measure");
for (const sel of [".tool-call-cluster", ".subs", ".notification-card", ".task-card"]) {
  assert(bleed[0].includes(sel), "bleed list includes " + sel);
}
assert(!/680px/.test(css), "legacy 680px caps snapped to var(--measure)");
console.log("ok layout width scale");
