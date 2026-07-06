// buildRow must render an icon (svg) inside .status-icon and set a title
// attribute carrying the unified word as the hover tooltip. The row no
// longer also renders a redundant .status-dot (sweep/sidebar-polish v2).
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

const node = { row_id: "project:x:local:01A", ref: "local:01A", state: "active", title: "t", session_id: "01A" };
const row = w.SerfSidebarInternal.buildRow(node); // exposed for tests, see step 3
const iconWrap = row.querySelector(".status-icon");
assert.ok(iconWrap, "row must render a .status-icon element");
assert.ok(iconWrap.querySelector("svg"), "status-icon must contain an svg icon");
assert.strictEqual(iconWrap.getAttribute("title"), "Working", "status-icon title must be the unified word (hover tooltip)");
assert.ok(!row.querySelector(".status-dot"), "row must not also render a redundant .status-dot");

const askNode = Object.assign({}, node, { state: "awaiting", ask_pending: true });
const askRow = w.SerfSidebarInternal.buildRow(askNode);
assert.strictEqual(askRow.querySelector(".status-icon").getAttribute("title"), "Question waiting");

const moveNode = Object.assign({}, node, { state: "awaiting", ask_pending: false });
const moveRow = w.SerfSidebarInternal.buildRow(moveNode);
assert.strictEqual(moveRow.querySelector(".status-icon").getAttribute("title"), "Your move");

const badge = w.SerfSidebarInternal.buildRollupBadge("rollup-live", "working", 3);
assert.ok(badge.querySelector("svg"), "rollup badge must render an svg icon, not a text glyph");
assert.strictEqual(badge.textContent.trim(), "3", "rollup badge count text must still read as a plain number");

const askOnRow = w.SerfSidebarInternal.buildRow({ row_id: "x", ref: "local:01A", state: "awaiting", ask_pending: true, title: "t", session_id: "01A" });
assert.strictEqual(askOnRow.getAttribute("data-ask"), "true", "an ask-pending row must carry data-ask=true");

const askOffRow = w.SerfSidebarInternal.buildRow({ row_id: "y", ref: "local:01B", state: "awaiting", ask_pending: false, title: "t", session_id: "01B" });
assert.ok(!askOffRow.hasAttribute("data-ask"), "a your-move row must not carry data-ask");

console.log("test-sidebar-icons.js: OK");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise
