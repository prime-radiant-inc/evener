// Project fold: a server-default-expanded project (default_expanded: true —
// hubcore sets it whenever the project has live/attention sessions) must
// still be collapsible by the user. The old logic OR'd model.expanded with
// default_expanded, so a click could never fold an active project back up.
// The user's explicit collapse is persisted ("false" in localStorage) and
// survives re-renders, resyncs, and reloads.
const fs = require("fs");
const assert = require("assert");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function makeTree() {
  return {
    needs_you: [], favorites: [],
    projects: [{
      key: "p1", name: "serf", working_dir: "/w/p1", default_expanded: true,
      sessions: [
        { row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "one", state: "active", kind: "session", tier: "current" },
      ],
    }],
    archived_projects: [], test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

function boot(storage) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  Object.keys(storage || {}).forEach((k) => w.localStorage.setItem(k, storage[k]));
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(makeTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(src);
  return w;
}

const tick = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  // 1) Default-expanded project renders its sessions.
  const w = boot();
  await tick(20);
  const header = w.document.querySelector('[data-row-id="header:p1"]');
  assert.ok(header, "project header renders");
  assert.strictEqual(header.getAttribute("aria-expanded"), "true", "default-expanded project starts expanded");
  assert.ok(w.document.querySelector('[data-row-id="project:p1:local:01A"]'), "sessions visible while expanded");

  // 2) Clicking the header folds it — THE bug: default_expanded must not win
  //    over an explicit user collapse.
  header.click();
  assert.strictEqual(header.getAttribute("aria-expanded"), "false", "clicking header collapses a default-expanded project");
  assert.ok(!w.document.querySelector('[data-row-id="project:p1:local:01A"]'), "sessions hidden after collapse");

  // 3) The collapse survives a re-render of the same tree (resync).
  w.SerfSidebar.renderTree(makeTree());
  assert.strictEqual(
    w.document.querySelector('[data-row-id="header:p1"]').getAttribute("aria-expanded"), "false",
    "collapse survives resync re-render");

  // 4) The explicit collapse persists to localStorage...
  assert.strictEqual(w.localStorage.getItem("serf-hub.sidebar.expanded.p1"), "false",
    "explicit collapse persisted as \"false\"");

  // 5) ...and a fresh boot restores it.
  const w2 = boot({ "serf-hub.sidebar.expanded.p1": "false" });
  await tick(20);
  assert.strictEqual(
    w2.document.querySelector('[data-row-id="header:p1"]').getAttribute("aria-expanded"), "false",
    "persisted collapse wins over default_expanded on a fresh load");
  assert.ok(!w2.document.querySelector('[data-row-id="project:p1:local:01A"]'), "sessions stay hidden on fresh load");

  // 6) Clicking again re-expands, and the preference flips back.
  const h2 = w2.document.querySelector('[data-row-id="header:p1"]');
  h2.click();
  assert.strictEqual(h2.getAttribute("aria-expanded"), "true", "click re-expands a user-collapsed project");
  assert.ok(w2.document.querySelector('[data-row-id="project:p1:local:01A"]'), "sessions visible again");

  console.log("PASS: default-expanded projects fold and the preference persists");
  process.exit(0); // sidebar.js arms a 60s resync interval; don't wait it out
})().catch((e) => { console.error(e); process.exit(1); });
