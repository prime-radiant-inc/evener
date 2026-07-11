// Sidebar polish (2026-07-11 annotated-screenshot pass):
//   • children disclosure ("› N") hidden on plain collapsed rows; revealed on
//     hover / focus-within / the active row / when already expanded
//   • session age is sans (variable-width) + smaller secondary treatment
//   • session rows carry no branch/meta column
//   • project headers use a stronger type treatment + inter-group whitespace
//   • rail toggle is a hamburger, not "⇤"
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
const js = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const appHtml = fs.readFileSync(__dirname + "/../templates/app.html", "utf8");

function ruleBody(sel) {
  const i = css.indexOf(sel);
  if (i === -1) return "";
  const open = css.indexOf("{", i);
  const close = css.indexOf("}", open);
  return css.slice(open + 1, close);
}

const fails = [];

// 1) Children toggle hidden by default on session rows...
const toggleRule = ruleBody(".subagent-toggle.sb-children-toggle {");
if (!/display:\s*none/.test(toggleRule)) fails.push("children toggle must be display:none by default (collapsed rows show no '› N')");
// ...revealed on hover, keyboard focus, the active row, and while expanded.
for (const sel of [
  ".sb-row:hover .sb-children-toggle",
  ".sb-row:focus-within .sb-children-toggle",
  ".sb-row[data-active] .sb-children-toggle",
  '.sb-children-toggle[aria-expanded="true"]',
]) {
  if (css.indexOf(sel) === -1) fails.push("missing reveal selector: " + sel);
}

// 2) Age: sans (variable-width) with tabular numerals, 2xs, dim.
const ageRule = ruleBody(".sb-row .age {");
if (/--font-mono/.test(ageRule)) fails.push("age must not be mono");
if (!/font-family:\s*var\(--font-sans\)/.test(ageRule)) fails.push("age must use --font-sans");
if (!/tabular-nums/.test(ageRule)) fails.push("age needs tabular-nums so widths stay stable");
if (!/font-size:\s*var\(--text-2xs\)/.test(ageRule)) fails.push("age must use the smaller --text-2xs step");

// 3) No branch/meta on session rows.
if (/n\.branch/.test(js)) fails.push("sidebar.js must not render n.branch");
if (/class="meta"/.test(js)) fails.push("session rows must not carry a .meta column");

// 4) Project header: stronger name + whitespace between groups.
const nameRule = ruleBody(".sb-tree .project-name {");
if (!/font-size:\s*var\(--text-base\)/.test(nameRule)) fails.push("project name must step up to --text-base");
if (!/font-weight:\s*6\d\d/.test(nameRule)) fails.push("project name must be semibold");
if (css.indexOf(".sb-tree .project-header:not(:first-child)") === -1 ||
    !/margin-top:\s*var\(--space-/.test(ruleBody(".sb-tree .project-header:not(:first-child)"))) {
  fails.push("project groups need whitespace between them (margin-top on non-first headers)");
}
// Row height must stay as-is: no vertical padding creep inside rows.
if (!/padding:\s*4px var\(--space-4\)/.test(ruleBody(".sb-row {"))) fails.push("row padding must stay 4px vertical");

// 5) Rail toggle is a hamburger.
if (/data-sidebar-rail-toggle[^>]*>⇤/.test(appHtml)) fails.push("rail toggle must not be the '⇤' glyph");
if (!/data-sidebar-rail-toggle[^>]*>☰/.test(appHtml)) fails.push("rail toggle must be a hamburger (☰)");

if (fails.length) { console.error("FAIL:\n  " + fails.join("\n  ")); process.exit(1); }
console.log("PASS: sidebar polish CSS/markup contract holds");
