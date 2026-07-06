// Compact single-line session rows (sweep/sidebar-polish): title, meta, and
// the ⋯ menu are direct grid children of .sb-row (no .text-col wrapper
// stacking title above meta on a second line), the title carries the full
// name as a hover tooltip, and rail-collapsed mode hides everything but the
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

// title/meta/menu are direct children of .sb-row — no .text-col wrapper.
assert.ok(!row.querySelector(".text-col"), "session row must not wrap title/meta in .text-col");
const title = row.querySelector(":scope > .title");
assert.ok(title, ".title must be a direct child of .sb-row");
const meta = row.querySelector(":scope > .meta");
assert.ok(meta, ".meta must be a direct child of .sb-row");
const menuBtn = row.querySelector(":scope > .sb-menu-btn");
assert.ok(menuBtn, ".sb-menu-btn must be a direct child of .sb-row");

// Full title is preserved as a hover tooltip since the CSS clips it visually.
assert.strictEqual(title.getAttribute("title"), longTitle, "title element must carry the full name as a tooltip");
assert.strictEqual(title.textContent, longTitle);

// meta keeps branch + age, in order.
assert.ok(meta.textContent.indexOf("main") !== -1, "meta must include the branch");

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
else if (!/grid-template-columns:\s*auto minmax\(0,\s*1fr\)\s*minmax\(0,\s*\d+px\)\s*auto/.test(sbRowRule)) {
  fails.push(".sb-row must use a 4-track grid (icon, title-grows, meta-capped, actions), got: " + sbRowRule);
}

const titleRule = ruleBody(".sb-row .title {");
if (!titleRule) fails.push("no .sb-row .title rule found");
else {
  if (/-webkit-line-clamp/.test(titleRule)) fails.push(".sb-row .title must not clamp to two lines anymore");
  if (!/white-space:\s*nowrap/.test(titleRule)) fails.push(".sb-row .title must be single-line (white-space: nowrap)");
  if (!/text-overflow:\s*ellipsis/.test(titleRule)) fails.push(".sb-row .title must ellipsize overflow");
}

const metaRule = ruleBody(".sb-row .meta {");
if (!metaRule) fails.push("no .sb-row .meta rule found");
else if (/margin-top/.test(metaRule)) fails.push(".sb-row .meta must not be pushed onto its own line with margin-top");

// Rail-collapsed mode (56px icon rail) must hide title/meta/actions, leaving
// only the status icon visible.
const railBlock = css.slice(css.indexOf('body.app[data-sidebar-rail] .sb-row {'));
const railSnippet = railBlock.slice(0, railBlock.indexOf('@container'));
if (!/\.sb-row \.title[^;{}]*\{[^}]*display:\s*none/.test(railSnippet) &&
    !/\.title[\s\S]{0,80}display:\s*none/.test(railSnippet)) {
  fails.push("rail mode must hide .sb-row .title");
}
if (!/\.meta[\s\S]{0,120}display:\s*none/.test(railSnippet)) {
  fails.push("rail mode must hide .sb-row .meta");
}

if (fails.length) {
  fails.forEach((f) => console.log("FAIL: " + f));
  process.exit(1);
}
console.log("PASS: test-sidebar-row-layout.js");
process.exit(0);
