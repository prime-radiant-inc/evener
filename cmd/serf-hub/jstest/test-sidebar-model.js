// First paint renders skeleton immediately; /api/tree resolves into rows;
// client-built rows carry the htmx workspace-swap attributes and get processed.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside><main id="workspace"></main></body></html>`, {
  runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/s/01A",
});
const w = dom.window;
let processed = 0;
w.htmx = { process() { processed++; } };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
    sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "hi", state: "idle", kind: "session", tier: "current", live: false }] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 } };
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree) });
w.eval(src);

// Skeleton painted synchronously on eval, before fetch resolves.
if (!w.document.querySelector("#sidebar .sb-skeleton")) throw new Error("skeleton must paint on first eval");

setTimeout(() => {
  const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row) throw new Error("row not rendered after fetch");
  if (row.getAttribute("hx-get") !== "/_partials/s/01A/workspace") throw new Error("row missing hx-get workspace swap");
  if (row.getAttribute("href") !== "/s/01A") throw new Error("row missing href");
  if (row.getAttribute("hx-push-url") !== "/s/01A") throw new Error("row missing hx-push-url");
  if (processed < 1) throw new Error("htmx.process must run on created rows");
  if (!row.hasAttribute("data-active")) throw new Error("row matching /s/01A pathname must be active");
  console.log("ok model+skeleton+htmx.process+active-row");
  process.exit(0);
}, 20);
