// Sidebar density/contrast floor: row meta uses >=11px and not --text-dim;
// menu/reveal controls are >=24px.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function ruleBody(selector) {
  const i = css.indexOf(selector);
  if (i < 0) return "";
  return css.slice(i, css.indexOf("}", i));
}
const fails = [];
const metaRule = ruleBody(".sb-row .meta");
if (!/font-size:\s*var\(--text-xs\)|font-size:\s*11px/.test(metaRule)) fails.push("row meta must be 11px (--text-xs)");
if (/--text-dim/.test(metaRule)) fails.push("row meta must not use --text-dim");
const menuBtn = ruleBody(".sb-menu-btn");
if (!/min-height:\s*24px/.test(menuBtn) || !/min-width:\s*24px/.test(menuBtn)) fails.push("reveal button must be >=24px");
if (fails.length) { fails.forEach((f) => console.log("FAIL: " + f)); process.exit(1); }
console.log("ok sidebar density/contrast floor");
process.exit(0);
