// Sidebar test-runs section: a keyed "Test runs (N)" divider, collapsed by
// default, rendered BELOW Archived, reusing pushProject/buildProjectHeader/
// buildRow for its content once expanded (same section machinery as
// test-sidebar-sections.js's Archived case). Precedence — which bucket a
// project that is both test-run and archived lands in — is decided
// server-side (web_api_tree.go, round-2 B6: TestRuns wins). The client just
// renders tree.test_runs and tree.archived_projects as two independent
// buckets and must never re-derive or merge them itself. Unlike Archived, a
// test-runs project keeps the plain "Archive" menu item (not "Unarchive")
// since it was never placed in the archived bucket.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  const posts = [];
  w.fetch = (url, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  };
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.confirm = () => true; // confirmDeleteProject's window.confirm gate
  w.eval(src);
  return { w, posts };
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function treeWithBoth() {
  var t = emptyTree();
  // default_expanded: true on each project itself proves both levels (like
  // test-sidebar-sections.js): expanding the SECTION reveals the project
  // header, whose own (already-expanded) state then shows its rows.
  t.archived_projects = [{
    key: "apk1", name: "arch", working_dir: "/a/arch", default_expanded: true,
    sessions: [{ row_id: "project:apk1:local:01Z", ref: "local:01Z", session_id: "01Z", title: "old session", state: "ended", kind: "session", tier: "archived" }],
  }];
  t.test_runs = [{
    key: "tpk1", name: "serf-e2e-foo", working_dir: "/t/serf-e2e-foo", default_expanded: true,
    sessions: [{ row_id: "project:tpk1:local:02Z", ref: "local:02Z", session_id: "02Z", title: "e2e run", state: "ended", kind: "session", tier: "archived" }],
  }];
  return t;
}
function rowOrder(w) {
  return [].map.call(w.document.querySelectorAll("#sidebar [data-row-id]"), function (e) { return e.getAttribute("data-row-id"); });
}

const { w, posts } = boot();
const tree = treeWithBoth();

// 1. Collapsed by default: the test-runs section header exists with the
// right label; no test-runs project header or session rows are in the DOM.
w.SerfSidebar.renderTree(tree);
const header = w.document.querySelector('[data-row-id="section:test-runs"]');
if (!header) throw new Error("test-runs section header must render when test_runs is non-empty");
if (!/Test runs \(1\)/.test(header.textContent)) {
  throw new Error('section header must show label "Test runs (N)", got ' + JSON.stringify(header.textContent));
}
if (header.getAttribute("aria-expanded") !== "false") {
  throw new Error('collapsed section header must have aria-expanded="false", got ' + JSON.stringify(header.getAttribute("aria-expanded")));
}
if (w.document.querySelector('[data-row-id="header:tpk1"]')) {
  throw new Error("test-runs project header must NOT render while the section is collapsed");
}
if (w.document.querySelector('[data-row-id="project:tpk1:local:02Z"]')) {
  throw new Error("test-runs session row must NOT render while the section is collapsed");
}

// 2. Ordering: the Test runs section header comes AFTER the Archived section
// header in DOM order (both non-empty) — true even fully collapsed, since
// section headers render any time their bucket is non-empty.
const order1 = rowOrder(w);
const archivedIdx1 = order1.indexOf("section:archived");
const testRunsIdx1 = order1.indexOf("section:test-runs");
if (archivedIdx1 === -1) throw new Error("archived section header must also render in this fixture (both non-empty), got order " + JSON.stringify(order1));
if (!(archivedIdx1 < testRunsIdx1)) {
  throw new Error("section:test-runs must come AFTER section:archived in DOM order, got order " + JSON.stringify(order1));
}

// 3. Precedence contract: expanding ONLY the Archived section must reveal
// apk1's project header but never tpk1's — a project fed in tree.test_runs
// must not render inside the Archived section (that would mean the client
// re-bucketed, which is the server's job alone).
const archivedHeader = w.document.querySelector('[data-row-id="section:archived"]');
archivedHeader.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.querySelector('[data-row-id="header:apk1"]')) {
  throw new Error("expanding Archived must reveal its own project (apk1)");
}
if (w.document.querySelector('[data-row-id="header:tpk1"]')) {
  throw new Error("a tree.test_runs project (tpk1) must NOT render inside the Archived section");
}
if (w.document.querySelector('[data-row-id="section:test-runs"]').getAttribute("aria-expanded") !== "false") {
  throw new Error("expanding Archived must not also expand the independent Test runs section");
}

// 4. Expand the Test runs section itself -> project header + session row
// appear; localStorage records the section's OWN expansion key;
// aria-expanded flips "false" -> "true" (exact strings).
header.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const header2 = w.document.querySelector('[data-row-id="section:test-runs"]');
if (header2.getAttribute("aria-expanded") !== "true") {
  throw new Error('expanded section header must have aria-expanded="true", got ' + JSON.stringify(header2.getAttribute("aria-expanded")));
}
if (w.localStorage.getItem("serf-hub.sidebar.expanded.section:test-runs") !== "true") {
  throw new Error("section expansion must persist to localStorage under the section key");
}
const projHeader = w.document.querySelector('[data-row-id="header:tpk1"]');
if (!projHeader) throw new Error("test-runs project header must render once the section is expanded (reused buildProjectHeader)");
if (!w.document.querySelector('[data-row-id="project:tpk1:local:02Z"]')) {
  throw new Error("test-runs session row must render once its project is expanded too (reused buildRow)");
}

// 5. Full-order sanity check with both sections expanded: proves reconcile
// ordering (header -> its own session rows, section -> section) holds up
// end to end, not just for the two section headers in isolation.
const order2 = rowOrder(w);
const expected = ["section:archived", "header:apk1", "project:apk1:local:01Z", "section:test-runs", "header:tpk1", "project:tpk1:local:02Z"];
if (JSON.stringify(order2) !== JSON.stringify(expected)) {
  throw new Error("expected full row order " + JSON.stringify(expected) + ", got " + JSON.stringify(order2));
}

// 6. Menu: the expanded test-runs project's menu offers Delete… and Archive
// (never Unarchive — it isn't in the archived bucket), and Delete… actually
// works (posts /api/project/delete with the wire key/working_dir) — this
// bucket exists specifically so the serf-e2e-* sprawl can be bulk-deleted.
const menuBtn = projHeader.querySelector(".sb-menu-btn");
if (!menuBtn) throw new Error("test-runs project header must carry a ⋯ menu button");
menuBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const menu = w.document.querySelector(".sb-menu");
if (!menu) throw new Error("clicking ⋯ must open a menu");
const items = [].map.call(menu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
if (!items.some((t) => /^Archive$/.test(t))) {
  throw new Error("a test-runs project's menu must offer plain Archive, got " + JSON.stringify(items));
}
if (items.some((t) => /Unarchive/.test(t))) {
  throw new Error("a test-runs project's menu must NOT offer Unarchive, got " + JSON.stringify(items));
}
const deleteItem = [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => /^Delete/.test(e.textContent));
if (!deleteItem) throw new Error("a test-runs project's menu must offer Delete…, got " + JSON.stringify(items));
deleteItem.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!posts.length) throw new Error("Delete… must POST /api/project/delete");
const last = posts[posts.length - 1];
if (last.url !== "/api/project/delete") throw new Error("Delete… must POST to /api/project/delete, got " + last.url);
if (last.body.key !== "tpk1" || last.body.working_dir !== "/t/serf-e2e-foo") {
  throw new Error("Delete… POST body wrong: " + JSON.stringify(last.body));
}

console.log("ok sidebar test-runs section: collapsed-by-default + ordering + precedence + delete-capable menu");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise
