// Composer layout: the dock spans the window (design-system §6), the input
// column centers at the measure, hit targets and ceilings hold.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const capGroup = css.match(/\.workspace-header,\s*\n\.conversation,\s*\n?[^{]*\{/);
assert(!capGroup || !capGroup[0].includes(".workspace-input"),
  ".workspace-input removed from the width-capped group (dock spans the window)");
assert(/\.workspace-input\s*\{[^}]*background:\s*var\(--bg-raised\)/.test(css),
  "dock carries the --bg-raised background step");
assert(/\.workspace-input > \[data-composer-surface\] \{[^}]*width:\s*min\(100%, var\(--measure\)\)[^}]*margin-inline:\s*auto/.test(css),
  "composer content column centers at the measure");
assert(/button\.composer-model-value \{[^}]*min-height:\s*30px/.test(css),
  "desktop model pill hit target ≥30px");
assert(/\.message-input \{[^}]*max-height:\s*min\(50vh, 264px\)/.test(css),
  "textarea has a px ceiling");
assert(/@media \(min-width: 768px\) and \(max-height: 639px\)/.test(css),
  "short-desktop band exists");
const phoneIdx = css.indexOf("@media (max-width: 767px) {\n  /* Touch target floor");
const shortIdx = css.indexOf("@media (max-width: 900px) and (max-height: 560px)");
const phoneBand = css.slice(phoneIdx, shortIdx);
assert(/\.controls-center \{[^}]*margin-left:\s*auto/.test(phoneBand),
  "phone control row rebalanced (model chip clusters right with the actions)");
console.log("ok composer layout");
