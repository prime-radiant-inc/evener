// A collapsed project (default_expanded omitted by Go's `omitempty` when
// false, and not present in model.expanded) must render aria-expanded="false"
// — not the literal string "undefined" from `String(undefined || undefined)`.
// Covers both render paths: buildProjectHeader (first render) and
// patchProjectHeader (reconcile of an existing header on a later render).
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}

const w = boot();
const tree = emptyTree();
// default_expanded is OMITTED (as Go's omitempty does for false), and "p1" is
// not in model.expanded — this is a genuinely collapsed project.
tree.projects = [{ key: "p1", name: "p", working_dir: "/w/p", sessions: [] }];

// First render: hits buildProjectHeader (container starts empty).
w.SerfSidebar.renderTree(tree);
const header1 = w.document.querySelector('[data-row-id="header:p1"]');
if (!header1) throw new Error("project header not rendered");
if (header1.getAttribute("aria-expanded") !== "false") {
  throw new Error('collapsed project header must have aria-expanded="false", got ' + JSON.stringify(header1.getAttribute("aria-expanded")));
}

// Second render of the same tree: header already exists, so this hits
// patchProjectHeader instead of buildProjectHeader.
w.SerfSidebar.renderTree(tree);
const header2 = w.document.querySelector('[data-row-id="header:p1"]');
if (header2.getAttribute("aria-expanded") !== "false") {
  throw new Error('patched collapsed project header must have aria-expanded="false", got ' + JSON.stringify(header2.getAttribute("aria-expanded")));
}

console.log("ok collapsed project header aria-expanded=false on build + patch");
process.exit(0); // sidebar.js's 60s idle-resync interval (Task 20) keeps the event loop alive otherwise
