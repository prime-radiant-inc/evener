// Sidebar sections: a keyed "Archived (N)" divider, collapsed by default,
// that reuses pushProject/buildProjectHeader/buildRow for its content once
// expanded, with an Unarchive (not Archive) row-menu action for the projects
// it contains.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

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
  w.eval(iconsSrc);
  w.eval(src);
  return { w, posts };
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function treeWithArchived() {
  var t = emptyTree();
  // default_expanded: true on the archived project itself — its own
  // expansion is independent of the section's (like an active project), so
  // this fixture proves both levels: expanding the SECTION reveals the
  // project header, whose own (already-expanded) state then shows its rows.
  t.archived_projects = [{
    key: "apk1", name: "arch", working_dir: "/a/arch", default_expanded: true,
    sessions: [{ row_id: "project:apk1:local:01Z", ref: "local:01Z", session_id: "01Z", title: "old session", state: "ended", kind: "session", tier: "archived" }],
  }];
  return t;
}

const { w, posts } = boot();
const tree = treeWithArchived();

// 1. Collapsed by default: the section header exists with the right label;
// no archived project header or session rows are in the DOM.
w.SerfSidebar.renderTree(tree);
const header = w.document.querySelector('[data-row-id="section:archived"]');
if (!header) throw new Error("archived section header must render when archived_projects is non-empty");
if (!/Archived \(1\)/.test(header.textContent)) {
  throw new Error('section header must show label "Archived (N)", got ' + JSON.stringify(header.textContent));
}
if (header.getAttribute("aria-expanded") !== "false") {
  throw new Error('collapsed section header must have aria-expanded="false", got ' + JSON.stringify(header.getAttribute("aria-expanded")));
}
if (w.document.querySelector('[data-row-id="header:apk1"]')) {
  throw new Error("archived project header must NOT render while the section is collapsed");
}
if (w.document.querySelector('[data-row-id="project:apk1:local:01Z"]')) {
  throw new Error("archived session row must NOT render while the section is collapsed");
}

// 2. Click the header -> expands; archived project header appears;
// localStorage records the section expansion key; aria-expanded flips
// "false" -> "true" (exact strings).
header.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const header2 = w.document.querySelector('[data-row-id="section:archived"]');
if (header2.getAttribute("aria-expanded") !== "true") {
  throw new Error('expanded section header must have aria-expanded="true", got ' + JSON.stringify(header2.getAttribute("aria-expanded")));
}
if (w.localStorage.getItem("serf-hub.sidebar.expanded.section:archived") !== "true") {
  throw new Error("section expansion must persist to localStorage under the section key");
}
const projHeader = w.document.querySelector('[data-row-id="header:apk1"]');
if (!projHeader) throw new Error("archived project header must render once the section is expanded (reused buildProjectHeader)");
if (!w.document.querySelector('[data-row-id="project:apk1:local:01Z"]')) {
  throw new Error("archived session row must render once its project is expanded too (reused buildRow)");
}

// 3. Identity probe: a second identical renderTree call keeps the SAME
// section-header DOM node (like test-sidebar-reconcile.js's probe).
header2.__probe = true;
w.SerfSidebar.renderTree(tree);
const header3 = w.document.querySelector('[data-row-id="section:archived"]');
if (!header3 || header3.__probe !== true) throw new Error("section header must keep DOM node identity across re-renders");

// 4. Row menu on an archived project offers Unarchive (not Archive);
// clicking it POSTs /api/archive with {kind:"project", id:working_dir, archived:false}.
const menuBtn = projHeader.querySelector(".sb-menu-btn");
if (!menuBtn) throw new Error("archived project header must carry a ⋯ menu button");
menuBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const menu = w.document.querySelector(".sb-menu");
if (!menu) throw new Error("clicking ⋯ must open a menu");
const items = [].map.call(menu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
if (items.some((t) => /^Archive$/.test(t))) {
  throw new Error("an archived project's menu must not offer plain Archive, got " + JSON.stringify(items));
}
const unarchiveItem = [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => /Unarchive/.test(e.textContent));
if (!unarchiveItem) throw new Error("an archived project's menu must offer Unarchive, got " + JSON.stringify(items));
unarchiveItem.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!posts.length) throw new Error("Unarchive must POST /api/archive");
const last = posts[posts.length - 1];
if (last.url !== "/api/archive") throw new Error("Unarchive must POST to /api/archive, got " + last.url);
if (last.body.kind !== "project" || last.body.id !== "/a/arch" || last.body.archived !== false) {
  throw new Error("Unarchive POST body wrong: " + JSON.stringify(last.body));
}

console.log("ok sidebar sections: archived (N) collapsed-by-default + identity + unarchive menu");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise
