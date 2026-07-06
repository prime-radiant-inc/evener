// Sidebar rows used to tint their background per-state (awaiting/active/
// warning/errored), with a parallel [data-theme="light"] override. That
// wash was removed (sweep/sidebar-polish v2): the status icon is now the
// sole state indicator, and rows only carry a thin left-border accent.
// Assert the background washes are gone and the border-left accent remains
// for the errored state, in both themes.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
const fails = [];

if (/\.sb-row\[data-state="errored"\]\s*\{[^}]*background:/.test(css)) {
  fails.push("base .sb-row[data-state=\"errored\"] must not set a background");
}
if (/\[data-theme="light"\]\s+\.sb-row\[data-state="errored"\]/.test(css)) {
  fails.push("light-theme errored background override should have been removed");
}
if (!/\.sb-row\[data-state="errored"\]\s*\{\s*border-left-color:\s*var\(--error\);\s*\}/.test(css)) {
  fails.push("base .sb-row[data-state=\"errored\"] must still set border-left-color");
}

if (fails.length) {
  fails.forEach((f) => console.log("FAIL: " + f));
  process.exit(1);
}
console.log("PASS: state background washes removed, border-left accent remains");
process.exit(0);
