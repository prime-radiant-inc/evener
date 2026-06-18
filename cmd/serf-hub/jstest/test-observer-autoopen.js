// Observer auto-open: when #conversation carries data-observers="<ref>", the
// renderer's init auto-opens each LIVE observer beside the worker via
// SerfPanes.open("/s/<ref>"). A user-closed observer pane stays closed across
// re-init (suppression memory in panes.js), and an explicit manual open clears
// that suppression.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

// ---- Part 1: renderer auto-open reads data-observers and calls SerfPanes.open

function newHarness(observers, opts) {
  opts = opts || {};
  const attr = observers ? ` data-observers="${observers}"` : "";
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01HOST"></header>
    <div id="conversation" data-session-id="01HOST" data-state="active"${attr}></div>
    <form data-input-form data-session-id="01HOST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  const opened = [];
  if (opts.withPanes !== false) {
    window.SerfPanes = {
      open: (href, title) => { opened.push({ href, title }); return {}; },
      isSuppressed: opts.isSuppressed || (() => false),
    };
  }
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv, opened };
}

let allPass = true;
function check(name, cond, detail) {
  console.log((cond ? "PASS" : "FAIL") + " — " + name);
  if (!cond) { allPass = false; if (detail) console.log("  detail: " + detail); }
}

// auto-open fires for each observer ref
(function () {
  const { opened } = newHarness("OBS1");
  check("auto-open calls SerfPanes.open for the observer",
    opened.length === 1 && opened[0].href === "/s/OBS1",
    JSON.stringify(opened));
})();

// multiple observers each open
(function () {
  const { opened } = newHarness("OBS1 OBS2");
  const hrefs = opened.map(o => o.href);
  check("auto-open opens each of multiple observers",
    hrefs.includes("/s/OBS1") && hrefs.includes("/s/OBS2") && opened.length === 2,
    JSON.stringify(hrefs));
})();

// no data-observers -> no auto-open
(function () {
  const { opened } = newHarness(null);
  check("no data-observers means no auto-open", opened.length === 0, JSON.stringify(opened));
})();

// suppressed observer is NOT re-opened
(function () {
  const { opened } = newHarness("OBS1", { isSuppressed: (href) => href === "/s/OBS1" });
  check("a suppressed (user-closed) observer is not re-opened", opened.length === 0, JSON.stringify(opened));
})();

// guard: no SerfPanes (inside a pane iframe) -> no auto-open, no throw
(function () {
  const { opened } = newHarness("OBS1", { withPanes: false });
  check("absent SerfPanes (pane iframe) skips auto-open without error", opened.length === 0, JSON.stringify(opened));
})();

// ---- Part 2: panes.js suppression memory (close records; open clears)

(function () {
  const dom = new JSDOM(`<!DOCTYPE html><body class="app">
    <main id="workspace"></main>
    <div id="pane-splitter" hidden></div>
    <aside id="side-panes" hidden></aside>
  </body>`, { url: "http://localhost/" });
  global.window = dom.window; global.document = dom.window.document;
  eval(fs.readFileSync(path.join(__dirname, "..", "assets", "panes.js"), "utf8"));
  const P = dom.window.SerfPanes;

  // open then close -> href becomes suppressed
  P.open("/s/OBS1", "Observer 1");
  check("freshly-opened observer is not suppressed", P.isSuppressed("/s/OBS1") === false);
  P.close("/s/OBS1");
  check("closing an observer records suppression", P.isSuppressed("/s/OBS1") === true);

  // suppression persists in localStorage under the documented key
  const raw = dom.window.localStorage.getItem("serf-hub.panes.closed");
  check("suppression persists to serf-hub.panes.closed", !!raw && raw.indexOf("/s/OBS1") !== -1, String(raw));

  // explicit manual open clears suppression
  P.open("/s/OBS1", "Observer 1");
  check("manual open clears suppression", P.isSuppressed("/s/OBS1") === false);
})();

if (!allPass) { console.error("FAIL: observer auto-open tests failed"); process.exit(1); }
console.log("OK\ttest-observer-autoopen.js");
process.exit(0);
