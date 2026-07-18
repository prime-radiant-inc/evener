// Sidebar disclosure keys and project keys share one expansion set. A resync
// must refresh only actual projects; disclosure keys are not valid
// /api/tree/project identifiers.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const sidebarSrc = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside><main id="workspace"></main></body></html>`, {
  runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
});
const w = dom.window;
w.htmx = { process() {} };
let connectionRestored = null;
w.SerfAppwire = {
  onNotification() {},
  onConnectionRestored(handler) { connectionRestored = handler; },
};

const parent = {
  row_id: "project:p1:local:MAIN", ref: "local:MAIN", session_id: "MAIN",
  title: "main", state: "idle", kind: "session", tier: "current", live: false,
  children: [{
    row_id: "project:p1:local:CHILD", ref: "local:CHILD", session_id: "CHILD",
    title: "child", state: "ended", kind: "session", tier: "current", live: false,
  }],
};
const tree = {
  needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p1", name: "project", working_dir: "/w/project", sessions: [parent] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

w.localStorage.setItem("serf-hub.sidebar.expanded.p1", "true");
w.localStorage.setItem("serf-hub.sidebar.expanded.inactive:project:p1:local:MAIN", "true");
const projectFetches = [];
w.fetch = (url) => {
  const value = String(url);
  if (value.indexOf("/api/tree/project") === 0) {
    projectFetches.push(value);
    return Promise.resolve({ ok: true, json: () => Promise.resolve(null) });
  }
  return Promise.resolve({ ok: true, json: () => Promise.resolve(JSON.parse(JSON.stringify(tree))) });
};

w.eval(iconsSrc);
w.eval(sidebarSrc);

const tick = () => new Promise((resolve) => setTimeout(resolve, 20));

(async () => {
  await tick();
  if (!connectionRestored) throw new Error("sidebar did not register the resync handler");
  connectionRestored();
  await tick();

  if (projectFetches.length !== 1 || projectFetches[0] !== "/api/tree/project?key=p1") {
    throw new Error("resync must request only expanded project keys, got " + JSON.stringify(projectFetches));
  }

  console.log("PASS: sidebar resync fetches only expanded projects");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
