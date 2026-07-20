// Sidebar density/contrast floor: menu/reveal controls are >=24px. (The row
// meta/branch column was removed 2026-07-11 — its floor assertions went with
// it; the age's treatment is covered by test-sidebar-polish-css.js.)
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function ruleBody(selector) {
  const i = css.indexOf(selector);
  if (i < 0) return "";
  return css.slice(i, css.indexOf("}", i));
}
const fails = [];
const menuBtn = ruleBody(".sb-menu-btn");
if (!/min-height:\s*24px/.test(menuBtn) || !/min-width:\s*24px/.test(menuBtn)) fails.push("reveal button must be >=24px");
// Collapsed mode (data-sidebar-rail) hides the ENTIRE sidebar (issue #33 —
// the 56px icon rail was retired), so no per-element rail rules (e.g. hiding
// .sb-section to stop its "Archived (N)" label bleeding out of the strip) may
// survive: the whole-hide contract lives in test-sidebar-collapsed-css.js.
if (/body\.app\[data-sidebar-rail\][^{}]*\.sb-section/.test(css)) fails.push("collapsed mode must not carry per-element .sb-section strip styling (the whole sidebar is hidden)");
if (fails.length) { fails.forEach((f) => console.log("FAIL: " + f)); process.exit(1); }
console.log("ok sidebar density/contrast floor");
process.exit(0);
