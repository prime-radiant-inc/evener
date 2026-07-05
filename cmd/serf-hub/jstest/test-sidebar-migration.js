// Old basename-keyed expansion entries migrate to the new path-slug keys after
// the first render; a co-basename collision copies the old value to all matching new keys.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.localStorage.setItem("serf-hub.sidebar.expanded.foo", "true"); // legacy basename key
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [
    { key: "foo-aaaa1111", name: "foo", working_dir: "/a/foo", default_expanded: false, sessions: [] },
    { key: "foo-bbbb2222", name: "foo", working_dir: "/b/foo", default_expanded: false, sessions: [] },
  ], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
setTimeout(() => {
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-aaaa1111") !== "true") throw new Error("migration must copy to first co-basename key");
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-bbbb2222") !== "true") throw new Error("migration must copy to all co-basename keys");
  console.log("ok expansion-key migration post-first-render + copy-to-all");
  process.exit(0);
}, 20);
