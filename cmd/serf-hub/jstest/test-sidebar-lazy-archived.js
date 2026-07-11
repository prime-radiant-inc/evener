// Archived projects arrive as session-less stubs ({sessions: null,
// session_count: N}); the sidebar renders them collapsed with a count and
// lazy-loads their sessions from /api/tree/project?key= on expand — including
// projects the user had expanded in a previous visit (localStorage restore).
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot(preExpanded) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside><main id="workspace"></main></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  (preExpanded || []).forEach((k) => w.localStorage.setItem("serf-hub.sidebar.expanded." + k, "true"));
  const stub = { key: "arch1", name: "old-proj", working_dir: "/w/old", is_archived: true, session_count: 2, sessions: null };
  const full = { key: "arch1", name: "old-proj", working_dir: "/w/old", is_archived: true,
    sessions: [
      { row_id: "project:arch1:local:01X", ref: "local:01X", session_id: "01X", title: "one", state: "ended", kind: "session", tier: "archived", live: false },
      { row_id: "project:arch1:local:01Y", ref: "local:01Y", session_id: "01Y", title: "two", state: "ended", kind: "session", tier: "archived", live: false },
    ] };
  const tree = { needs_you: [], favorites: [], projects: [], archived_projects: [stub], test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
  const projectFetches = [];
  w.fetch = (url) => {
    const u = String(url);
    if (u.indexOf("/api/tree/project") === 0) {
      projectFetches.push(u);
      return Promise.resolve({ ok: true, json: () => Promise.resolve(JSON.parse(JSON.stringify(full))) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(JSON.parse(JSON.stringify(tree))) });
  };
  w.eval(iconsSrc);
  w.eval(src);
  return { w, projectFetches };
}

const tick = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  // 1) Fresh boot: stub renders collapsed under the Archived section with a
  //    count, no lazy fetch yet.
  const a = boot([]);
  await tick(20);
  let section = a.w.document.querySelector('[data-row-id="section:archived"]');
  if (!section) throw new Error("archived section header missing");
  a.w.document.querySelector('[data-row-id="section:archived"]').click();
  await tick(20);
  const header = a.w.document.querySelector('[data-row-id="header:arch1"]');
  if (!header) throw new Error("archived stub header missing after section expand");
  if ((header.textContent || "").indexOf("2") === -1) throw new Error("stub header must show the session count, got: " + header.textContent);
  if (a.w.document.querySelector('[data-row-id="project:arch1:local:01X"]')) throw new Error("stub must render collapsed without sessions");
  if (a.projectFetches.length !== 0) throw new Error("no lazy fetch before expand, got " + a.projectFetches);

  // 2) Expanding the stub lazy-fetches its sessions and renders them.
  header.click();
  await tick(20);
  if (a.projectFetches.length !== 1 || a.projectFetches[0].indexOf("key=arch1") === -1) {
    throw new Error("expand must lazy-fetch the project, got " + JSON.stringify(a.projectFetches));
  }
  if (!a.w.document.querySelector('[data-row-id="project:arch1:local:01X"]')) throw new Error("lazy-loaded sessions must render");

  // 3) Restore: a previously-expanded archived project hydrates on initial load.
  const b = boot(["section:archived", "arch1"]);
  await tick(30);
  if (b.projectFetches.length !== 1) throw new Error("restored expansion must auto-hydrate, got " + JSON.stringify(b.projectFetches));
  if (!b.w.document.querySelector('[data-row-id="project:arch1:local:01Y"]')) throw new Error("restored expansion must render lazy sessions");

  console.log("ok lazy archived stubs");
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
