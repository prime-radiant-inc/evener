// Archived sessions (#44): archived-tier sessions no longer render inline
// under their active project. They fold away behind the top-level
// "Archived sessions" disclosure (the renamed section:archived), and inside
// it they group under per-project sub-headings showing the project name.
// Active sessions must render exactly as before.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot(url) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: url || "http://localhost/",
  });
  const w = dom.window;
  const posts = [];
  w.fetch = (u, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url: u, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
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
function treeWithArchivedSessions() {
  var t = emptyTree();
  t.projects = [{
    key: "p1", name: "proj-one", working_dir: "/w/p1", default_expanded: true,
    sessions: [
      { row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "active one", state: "active", kind: "session", tier: "current" },
      { row_id: "project:p1:local:01B", ref: "local:01B", session_id: "01B", title: "recent one", state: "ended", kind: "session", tier: "recent" },
      { row_id: "project:p1:local:01C", ref: "local:01C", session_id: "01C", title: "old one", state: "ended", kind: "session", tier: "archived" },
      { row_id: "project:p1:local:01D", ref: "local:01D", session_id: "01D", title: "old two", state: "ended", kind: "session", tier: "archived" },
    ],
  }];
  // An archived project stub (session-less; lazy-hydrates on expand) — the
  // pre-existing section content, unchanged by #44.
  t.archived_projects = [{ key: "ap1", name: "old-proj", working_dir: "/w/old", is_archived: true, session_count: 1, sessions: null }];
  return t;
}

const { w, posts } = boot();
const tree = treeWithArchivedSessions();
w.SerfSidebar.renderTree(tree);

// 1. The top-level disclosure exists, labeled "Archived sessions (N)" where
//    N counts the archived sessions inside it (2 from the active project + 1
//    from the archived stub), collapsed by default.
const section = w.document.querySelector('[data-row-id="section:archived"]');
if (!section) throw new Error("archived section header must render when archived sessions exist");
if (!/Archived sessions \(3\)/.test(section.textContent)) {
  throw new Error('section header must show label "Archived sessions (N)", got ' + JSON.stringify(section.textContent));
}
if (section.getAttribute("aria-expanded") !== "false") {
  throw new Error('archived section must be collapsed by default, got aria-expanded=' + JSON.stringify(section.getAttribute("aria-expanded")));
}

// 2. The active project renders its non-archived sessions inline exactly as
//    before (default_expanded), but its archived-tier sessions do NOT render
//    inline — they live behind the section.
if (!w.document.querySelector('[data-row-id="project:p1:local:01A"]')) throw new Error("current-tier session must render inline under its active project");
if (!w.document.querySelector('[data-row-id="project:p1:local:01B"]')) throw new Error("recent-tier session must render inline under its active project");
if (w.document.querySelector('[data-row-id="project:p1:local:01C"]')) throw new Error("archived session must NOT render inline under its active project");
if (w.document.querySelector('[data-row-id="project:p1:local:01D"]')) throw new Error("archived session must NOT render inline under its active project");

// 3. Expanding the section reveals per-project sub-headings: the archived
//    stub (existing behavior) and a group header for the active project's
//    archived sessions showing the project name — collapsed by default.
section.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.querySelector('[data-row-id="header:ap1"]')) throw new Error("archived project stub header must render inside the expanded section");
const group = w.document.querySelector('[data-row-id="header:archived:p1"]');
if (!group) throw new Error("active project's archived sessions must group under a per-project sub-heading inside the section");
if ((group.textContent || "").indexOf("proj-one") === -1) throw new Error("the archived-sessions sub-heading must show the project name, got " + JSON.stringify(group.textContent));
if (group.getAttribute("aria-expanded") !== "false") {
  throw new Error("the per-project archived group must be collapsed by default, got aria-expanded=" + JSON.stringify(group.getAttribute("aria-expanded")));
}
if (w.document.querySelector('[data-row-id="project:p1:local:01C"]')) throw new Error("archived sessions must stay hidden until their project group is expanded");

// 4. Expanding the group renders ONLY the archived sessions; active sessions
//    remain exactly where they were. Expansion persists under the group key.
group.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.querySelector('[data-row-id="project:p1:local:01C"]')) throw new Error("archived session must render once its project group is expanded");
if (!w.document.querySelector('[data-row-id="project:p1:local:01D"]')) throw new Error("archived session must render once its project group is expanded");
if (!w.document.querySelector('[data-row-id="project:p1:local:01A"]')) throw new Error("active sessions must remain rendered under the active project");
if (w.localStorage.getItem("serf-hub.sidebar.expanded.archived:p1") !== "true") {
  throw new Error("archived group expansion must persist under its own key");
}

// 5. DOM identity: the section header node survives a re-render (keyed
//    reconcile), like every other keyed element.
const section2 = w.document.querySelector('[data-row-id="section:archived"]');
section2.__probe = true;
w.SerfSidebar.renderTree(tree);
const section3 = w.document.querySelector('[data-row-id="section:archived"]');
if (!section3 || section3.__probe !== true) throw new Error("section header must keep DOM node identity across re-renders");

// 6. An archived session's row menu offers Unarchive (not Archive), and
//    clicking it POSTs /api/archive with archived:false.
const archivedRow = w.document.querySelector('[data-row-id="project:p1:local:01C"]');
const menuBtn = archivedRow.querySelector(".sb-menu-btn");
if (!menuBtn) throw new Error("archived session row must carry a ⋯ menu button");
menuBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const menu = w.document.querySelector(".sb-menu");
if (!menu) throw new Error("clicking ⋯ must open a menu");
const items = [].map.call(menu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
if (items.some((t) => /^Archive$/.test(t))) throw new Error("an archived session's menu must not offer plain Archive, got " + JSON.stringify(items));
const unarchive = [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => /Unarchive/.test(e.textContent));
if (!unarchive) throw new Error("an archived session's menu must offer Unarchive, got " + JSON.stringify(items));
unarchive.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const last = posts[posts.length - 1];
if (!last || last.url !== "/api/archive" || last.body.kind !== "session" || last.body.archived !== false) {
  throw new Error("Unarchive must POST /api/archive {kind:session, archived:false}, got " + JSON.stringify(last));
}

// 7. Empty archive: no archived projects and no archived-tier sessions in any
//    active project -> no section chrome at all.
const empty = boot();
const activeOnly = emptyTree();
activeOnly.projects = [{
  key: "p2", name: "proj-two", working_dir: "/w/p2", default_expanded: true,
  sessions: [{ row_id: "project:p2:local:02A", ref: "local:02A", session_id: "02A", title: "live", state: "active", kind: "session", tier: "current" }],
}];
empty.w.SerfSidebar.renderTree(activeOnly);
if (empty.w.document.querySelector('[data-row-id="section:archived"]')) {
  throw new Error("no Archived sessions section may render when nothing is archived");
}

// 8. Deep-link auto-reveal: a URL targeting an archived session of an active
//    project expands the section + the project group and marks the row
//    data-active.
const deep = boot("http://localhost/s/01D");
deep.w.SerfSidebar.renderTree(treeWithArchivedSessions());
const revealed = deep.w.document.querySelector('[data-row-id="project:p1:local:01D"]');
if (!revealed) throw new Error("deep-link to an archived session must auto-reveal it through the section + project group");
if (!revealed.hasAttribute("data-active")) throw new Error("the auto-revealed archived session must be marked data-active");
if (deep.w.localStorage.getItem("serf-hub.sidebar.expanded.section:archived") !== "true") {
  throw new Error("auto-reveal must persist the section expansion key");
}
if (deep.w.localStorage.getItem("serf-hub.sidebar.expanded.archived:p1") !== "true") {
  throw new Error("auto-reveal must persist the archived project-group expansion key");
}

console.log("ok sidebar archived sessions: folded behind top-level disclosure, grouped by project, active list untouched");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise
