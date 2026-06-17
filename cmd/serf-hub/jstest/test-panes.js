// Sets up minimal DOM (#side-panes, #pane-splitter), loads panes.js, exercises open/close/cap.
const { JSDOM } = require("jsdom");
const fs = require("fs");
const path = require("path");

const dom = new JSDOM(`<!DOCTYPE html><body class="app">
  <main id="workspace"></main>
  <div id="pane-splitter" hidden></div>
  <aside id="side-panes" hidden></aside>
</body>`, { url: "http://localhost/" });
global.window = dom.window; global.document = dom.window.document;

eval(fs.readFileSync(path.join(__dirname, "..", "assets", "panes.js"), "utf8"));
const P = dom.window.SerfPanes;

// open one pane
const pane = P.open("/s/sub-1", "Subagent 1");
if (!pane) throw new Error("open returned null");
const frames = () => document.querySelectorAll("#side-panes .pane-frame");
if (frames().length !== 1) throw new Error("expected 1 frame, got " + frames().length);
if (frames()[0].getAttribute("src") !== "/s/sub-1") throw new Error("wrong iframe src");
if (document.getElementById("side-panes").hidden) throw new Error("region should be visible");

// opening same href again does NOT duplicate
P.open("/s/sub-1", "Subagent 1");
if (frames().length !== 1) throw new Error("duplicate href should not add a pane");

// cap at MAX_SIDE_PANES
P.open("/s/sub-2", "two"); P.open("/s/sub-3", "three"); P.open("/s/sub-4", "four");
if (frames().length !== P.MAX_SIDE_PANES) throw new Error("cap not enforced: " + frames().length);

// openHrefs reflects state
if (P.openHrefs().length !== P.MAX_SIDE_PANES) throw new Error("openHrefs wrong");

// close hides region when empty
P.openHrefs().slice().forEach(h => P.close(h));
if (frames().length !== 0) throw new Error("panes not closed");
if (!document.getElementById("side-panes").hidden) throw new Error("region should hide when empty");

console.log("test-panes: ok");
process.exit(0);
