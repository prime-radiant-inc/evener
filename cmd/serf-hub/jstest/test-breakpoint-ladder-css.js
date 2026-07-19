// Breakpoint ladder: phone ≤767 (unchanged), tablet 768–1199 (panes hidden),
// desktop 1200–1799, wide ≥1800 (machine bleed widens to 1200px).
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/@media \(max-width: 1199px\) \{\s*\.side-panes, \.pane-splitter \{ display: none !important; \}\s*\}/.test(css),
  "side panes hidden through the tablet band (max-width: 1199px)");
assert(!/@media \(max-width: 767px\) \{ \.side-panes/.test(css),
  "old 767px pane-hiding rule is gone");
assert(/@media \(min-width: 1800px\) \{\s*:root \{\s*--measure-machine:\s*1200px;\s*\}\s*\}/.test(css),
  "wide band widens the machine bleed to 1200px");
// Phone band untouched: the phone tap floor still flips at 767px.
assert(/@media \(max-width: 767px\) \{\s*\n?\s*\/\*[^*]*\*\/\s*\n?\s*:root \{ --tap-min: 44px; \}/.test(css),
  "phone band (max-width: 767px) still sets --tap-min: 44px");
console.log("ok breakpoint ladder");
