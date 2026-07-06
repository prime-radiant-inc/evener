// Keyed reconcile: unchanged RowIDs keep DOM node identity across renders, and
// the same session in Needs-you + a project + Pinned yields three stable nodes.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(src);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function node(rowId, sid, state) {
  return { row_id: rowId, ref: "local:" + sid, session_id: sid, title: sid, state: state, kind: "session", tier: "current", live: state !== "ended" };
}

const w = boot();
const tree = emptyTree();
const sid = "01A";
tree.needs_you = [node("needsyou::local:" + sid, sid, "awaiting")];
tree.favorites = [node("pinned::local:" + sid, sid, "awaiting")];
tree.projects = [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true, sessions: [node("project:p1:local:" + sid, sid, "awaiting")] }];

w.SerfSidebar.renderTree(tree);
const rows1 = w.document.querySelectorAll(".sb-row");
if (rows1.length !== 3) throw new Error("same session across 3 tiers must yield 3 rows, got " + rows1.length);
// Tag the project row; a second identical render must keep the SAME node.
const projRow = w.document.querySelector('[data-row-id="project:p1:local:' + sid + '"]');
projRow.__probe = true;
w.SerfSidebar.renderTree(tree);
const projRow2 = w.document.querySelector('[data-row-id="project:p1:local:' + sid + '"]');
if (!projRow2 || projRow2.__probe !== true) throw new Error("unchanged RowID must keep DOM node identity");
console.log("ok reconcile keeps identity + cross-tier duplicates");
process.exit(0); // sidebar.js's 60s idle-resync interval (Task 20) keeps the event loop alive otherwise
