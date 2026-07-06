// Overlay: a favorite (mutation-type) op stays applied across resyncs until a
// resync reflects the field, and does NOT roll back when no qualifying event
// fires (post-POST resync confirms it). An archive (disappearance-type) op
// completes on POST-2xx. 30s eviction is a safety net only.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot(trees) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  let i = 0;
  const posts = [];
  w.fetch = (url, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(trees[Math.min(i++, trees.length - 1)]) });
  };
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(src);
  return { w, posts };
}
function tree(fav) {
  return { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
    projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
      sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "s", state: "idle", kind: "session", tier: "current", favorite: fav }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}

(async () => {
  // Baseline: not favorited. Optimistic favorite. Next resync (post-POST) shows
  // favorite=true → op completes, no rollback.
  const { w, posts } = boot([tree(false), tree(true)]);
  await new Promise(r => setTimeout(r, 20));
  w.SerfSidebar.favorite("local:01A", true);
  // Optimistic: the row shows the star immediately, before any resync.
  let row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row.hasAttribute("data-favorite")) throw new Error("optimistic favorite must apply immediately");
  if (!posts.some(p => p.url === "/api/favorite" && p.body.favorited === true)) throw new Error("favorite must POST /api/favorite");
  await new Promise(r => setTimeout(r, 20)); // post-POST resync resolves to tree(true)
  row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row.hasAttribute("data-favorite")) throw new Error("confirmed favorite must persist after resync (no false rollback)");
  console.log("ok overlay favorite confirms via post-POST resync");
  process.exit(0);
})();
