// Sidebar tri-state: auto|rail|pane persisted under the legacy key, binary
// prefs migrate (true→rail, false→pane, absent→auto), one effective-state
// helper drives body[data-sidebar-rail], ⌘B cycles rail→pane→auto.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function makeWindow(stored, desktopMatches) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const w = dom.window;
  if (stored !== null) w.localStorage.setItem("serf-hub.sidebar.rail", stored);
  w.matchMedia = (q) => ({ matches: q === "(min-width: 1200px)" ? desktopMatches : false, addEventListener() {}, addListener() {} });
  w.fetch = () => new Promise(() => {});
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

// Migration: binary "true" → rail, "false" → pane, absent → auto.
let w = makeWindow("true", true);
assert(w.SerfSidebar.readSidebarMode() === "rail", '"true" migrates to rail');
w = makeWindow("false", true);
assert(w.SerfSidebar.readSidebarMode() === "pane", '"false" migrates to pane');
w = makeWindow(null, true);
assert(w.SerfSidebar.readSidebarMode() === "auto", "absent pref defaults to auto");

// Effective state: auto follows the 1200px query; explicit modes pin.
w = makeWindow(null, true);
assert(!w.document.body.hasAttribute("data-sidebar-rail"), "auto on desktop ≥1200px → pane (no rail attr)");
assert(w.document.body.getAttribute("data-sidebar-mode") === "auto", "data-sidebar-mode records the setting");
w = makeWindow(null, false);
assert(w.document.body.hasAttribute("data-sidebar-rail"), "auto below 1200px → rail");
w = makeWindow("rail", true);
assert(w.document.body.hasAttribute("data-sidebar-rail"), "explicit rail pins rail even on desktop");

// Cycle: rail → pane → auto → rail, persisted.
w = makeWindow("rail", true);
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "pane", "rail cycles to pane");
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "auto", "pane cycles to auto");
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "rail", "auto cycles to rail");
assert(w.localStorage.getItem("serf-hub.sidebar.rail") === "rail", "cycle persists to storage");

console.log("ok sidebar tri-state");
process.exit(0);
