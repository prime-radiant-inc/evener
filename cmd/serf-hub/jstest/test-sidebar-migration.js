// Expansion state is keyed only by server project IDs. Old basename entries are
// ignored; the clean break does not migrate or synthesize project keys.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.localStorage.setItem("serf-hub.sidebar.expanded.foo", "true"); // obsolete basename key
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [
    { key: "foo-main-abcdefghij", name: "foo", working_dir: "/a/foo", default_expanded: false, sessions: [] },
    { key: "foo-clone-klmnopqrst", name: "foo", working_dir: "/b/foo", default_expanded: false, sessions: [] },
  ], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
setTimeout(() => {
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo") !== "true") throw new Error("obsolete basename preference must remain untouched");
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-main-abcdefghij") !== null) throw new Error("obsolete basename preference must not migrate");
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-clone-klmnopqrst") !== null) throw new Error("obsolete basename preference must not copy to co-basename projects");
  console.log("ok expansion keys use server project IDs without basename migration");
  process.exit(0);
}, 20);
