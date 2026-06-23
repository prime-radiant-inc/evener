const fs = require("fs");
const { JSDOM } = require("jsdom");

function load(window, file) { window.eval(fs.readFileSync(file, "utf8")); }

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <main id="workspace"></main>
  <div id="pane-splitter" hidden></div>
  <aside id="side-panes" hidden></aside>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/parent" });
const { window } = dom;
load(window, "../assets/panes.js");

let allPass = true;
function check(ok, msg) { console.log((ok ? "PASS" : "FAIL") + " — " + msg); if (!ok) allPass = false; }

const pane = window.SerfPanes.open("/thread/local%3Achild", "child");
check(pane && pane.dataset.state === "loading", "pane starts loading");
const frame = pane.querySelector("iframe");
frame.dispatchEvent(new window.Event("load"));
check(pane.dataset.state === "ready", "pane becomes ready after iframe load");

const bad = window.SerfPanes.open("/thread/local%3Amissing", "missing");
window.SerfPanes.markError("/thread/local%3Amissing", "Thread failed to load");
check(bad.dataset.state === "error", "pane can enter error state");
check(!!bad.querySelector(".pane-error"), "error state renders pane error UI");
check(!!bad.querySelector("[data-pane-retry]"), "error state renders retry control");

if (!allPass) process.exit(1);
console.log("OK\ttest-panes-error.js");
