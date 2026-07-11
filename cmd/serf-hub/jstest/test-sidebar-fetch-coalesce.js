// Concurrent /api/tree consumers share one in-flight transfer: the startup
// fetchTree and an immediately-scheduled resync (e.g. connection-restored or
// an early notification) must not each download the tree — on large hubs the
// payload is megabytes, and the seq guard only discarded the stale RESULT,
// not the duplicate transfer.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside><main id="workspace"></main></body></html>`, {
  runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
});
const w = dom.window;
w.htmx = { process() {} };
let notifHandler = null;
w.SerfAppwire = { onNotification(h) { notifHandler = h; }, onConnectionRestored() {} };
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
    sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "hi", state: "idle", kind: "session", tier: "current", live: false }] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 } };
let treeFetches = 0;
let resolvers = [];
w.fetch = (url) => {
  if (String(url).indexOf("/api/tree/project") === 0) return Promise.resolve({ ok: true, json: () => Promise.resolve(null) });
  treeFetches++;
  return new Promise((resolve) => { resolvers.push(() => resolve({ ok: true, json: () => Promise.resolve(tree) })); });
};
w.eval(iconsSrc);
w.eval(src); // startup fetchTree() issues transfer #1 (unresolved)

// While transfer #1 is in flight, a qualifying notification schedules an
// immediate resync (lastResync=0 -> zero wait). It must reuse the in-flight
// transfer instead of opening a second one.
notifHandler("thread/started", {});

setTimeout(() => {
  if (treeFetches !== 1) throw new Error("expected 1 shared /api/tree transfer, got " + treeFetches);
  resolvers.forEach((r) => r());
  setTimeout(() => {
    const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
    if (!row) throw new Error("shared transfer must still render the tree");
    // A later resync, after the first settled, opens a fresh transfer.
    notifHandler("thread/started", {});
    setTimeout(() => {
      if (treeFetches !== 2) throw new Error("post-settle resync must issue a new transfer, got " + treeFetches);
      console.log("ok coalesced in-flight /api/tree");
      process.exit(0);
    }, 2200);
  }, 20);
}, 50);
