// Compact single-line session rows (sweep/sidebar-polish, v3 2026-07-11):
// title and row-end are direct grid children of .sb-row (no .text-col
// wrapper), there is NO meta/branch column at all, the title carries the
// full name as a hover tooltip, the age is pinned flush to the row's right
// edge inside row-end, and rail-collapsed mode hides everything but the
// status icon.
"use strict";
const fs = require("fs");
const { JSDOM } = require("jsdom");
const assert = require("assert");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(fs.readFileSync(__dirname + "/../assets/icons.js", "utf8"));
  w.eval(fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8"));
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}

const w = boot();
const longTitle = "refactor auth middleware to use the new session token store";
const node = { row_id: "project:x:local:01A", ref: "local:01A", state: "active", title: longTitle, branch: "main", updated_at: new Date().toISOString(), session_id: "01A" };
const row = w.SerfSidebarInternal.buildRow(node);

// title/row-end are direct children of .sb-row — no .text-col wrapper, and
// no meta/branch column (branch is workspace detail, not navigation signal).
assert.ok(!row.querySelector(".text-col"), "session row must not wrap title in .text-col");
const title = row.querySelector(":scope > .title");
assert.ok(title, ".title must be a direct child of .sb-row");
assert.ok(!row.querySelector(".meta"), "session row must not render a .meta/branch column");
const rowEnd = row.querySelector(":scope > .row-end");
assert.ok(rowEnd, ".row-end must be a direct child of .sb-row");

// The ⋯ menu and the age live together in row-end's age-slot (same cell,
// overlap via CSS grid-area), not as their own top-level column.
const menuBtn = rowEnd.querySelector(".age-slot > .sb-menu-btn");
assert.ok(menuBtn, ".sb-menu-btn must live inside row-end's age-slot");
const ageEl = rowEnd.querySelector(".age-slot > .age");
assert.ok(ageEl, ".age must live inside row-end's age-slot, alongside the menu button");

// Full title is preserved as a hover tooltip since the CSS clips it visually.
assert.strictEqual(title.getAttribute("title"), longTitle, "title element must carry the full name as a tooltip");
assert.strictEqual(title.textContent, longTitle);

assert.ok(ageEl.textContent.length > 0, "age-slot's age span must carry the rendered age text");

console.log("test-sidebar-row-layout.js: markup OK");

// --- CSS assertions -------------------------------------------------------
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function ruleBody(selector) {
  const i = css.indexOf(selector);
  if (i < 0) return null;
  return css.slice(i, css.indexOf("}", i));
}
const fails = [];

const sbRowRule = ruleBody(".sb-row {");
if (!sbRowRule) fails.push("no .sb-row base rule found");
else if (!/grid-template-columns:\s*auto minmax\(0,\s*1fr\)\s*auto/.test(sbRowRule)) {
  fails.push(".sb-row must use a 3-track grid (icon, title-grows, row-end), got: " + sbRowRule);
}

const titleRule = ruleBody(".sb-row .title {");
if (!titleRule) fails.push("no .sb-row .title rule found");
else {
  if (/-webkit-line-clamp/.test(titleRule)) fails.push(".sb-row .title must not clamp to two lines anymore");
  if (!/white-space:\s*nowrap/.test(titleRule)) fails.push(".sb-row .title must be single-line (white-space: nowrap)");
  if (!/text-overflow:\s*ellipsis/.test(titleRule)) fails.push(".sb-row .title must ellipsize overflow");
}

if (ruleBody(".sb-row .meta {")) fails.push(".sb-row .meta rule must be gone along with the branch column");

// row-end pins content to the row's right edge (no trailing column after it)
// and age-slot stacks age + ⋯ in one cell so they can swap without shifting.
const rowEndRule = ruleBody(".sb-row .row-end {");
if (!rowEndRule) fails.push("no .sb-row .row-end rule found");
else if (!/justify-content:\s*flex-end/.test(rowEndRule)) fails.push(".sb-row .row-end must right-align its content (flush right)");

if (!/\.sb-row \.age-slot > \* \{\s*grid-area:\s*1 \/ 1;\s*\}/.test(css)) {
  fails.push("age-slot must stack its children (age, ⋯ menu) in the same grid cell via grid-area overlap");
}
if (!/\.sb-row:hover \.age,\s*\.sb-row:focus-within \.age\s*\{[^}]*opacity:\s*0/.test(css)) {
  fails.push("hovering/focusing the row must fade the age out");
}
if (!/\.sb-row:hover \.sb-menu-btn,\s*\.sb-row:focus-within \.sb-menu-btn\s*\{[^}]*opacity:\s*1/.test(css)) {
  fails.push("hovering/focusing the row must fade the ⋯ menu in");
}

// Collapsed mode (data-sidebar-rail) hides the whole sidebar (issue #33 — the
// 56px icon rail that used to hide title/row-end inside a narrow strip is
// retired), so no rail-scoped .sb-row styling may survive.
if (/body\.app\[data-sidebar-rail\]\s+\.sb-row/.test(css)) {
  fails.push("collapsed mode must not restyle .sb-row (the whole sidebar is hidden)");
}

if (fails.length) {
  fails.forEach((f) => console.log("FAIL: " + f));
  process.exit(1);
}
console.log("PASS: test-sidebar-row-layout.js");
process.exit(0);
