// Recent-prompts rows must truncate to 2 lines on every width, not just
// phone. Assert the BASE .spawn-recent-row rule (the first occurrence in the
// stylesheet) carries -webkit-line-clamp: 2.
//
// Note: we deliberately do NOT split the file on the first
// "@media (max-width: 767px)" string (as a naive approach might) — an
// earlier, unrelated one-line media query for .side-panes/.pane-splitter
// appears before the base .spawn-recent-row rule and would make that split
// point wrong. Matching the first ".spawn-recent-row { ... }" block directly
// is robust regardless of how many media queries precede it.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");

const m = css.match(/\.spawn-recent-row\s*\{[^}]*\}/);
if (!m) {
  console.log("FAIL: no .spawn-recent-row rule found in style.css");
  process.exit(1);
}
if (!/-webkit-line-clamp:\s*2/.test(m[0])) {
  console.log("FAIL: base .spawn-recent-row must carry the 2-line clamp (desktop truncation), got: " + m[0]);
  process.exit(1);
}
console.log("PASS: recent-prompts clamp applies on desktop");
process.exit(0);
