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
if (frames()[0].getAttribute("src") !== "/thread/sub-1") throw new Error("wrong iframe src");
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

// persistence: opening writes localStorage; a fresh load restores
P.open("/s/keep-1", "Keep 1");
const stored = JSON.parse(dom.window.localStorage.getItem("serf-hub.panes") || "[]");
if (!stored.some(p => p.href === "/thread/keep-1")) throw new Error("open not persisted");

// simulate reload: clear DOM panes, call restore()
document.querySelectorAll("#side-panes .pane").forEach(n => n.remove());
document.getElementById("side-panes").hidden = true;
P.restore();
if (!P.openHrefs().includes("/thread/keep-1")) throw new Error("restore did not reopen pane");
console.log("test-panes persistence: ok");

// max = Math.min(1200, window.innerWidth - 360); jsdom innerWidth = 1024 → max = 664
var expectedMax = Math.min(1200, dom.window.innerWidth - 360);
P.setSidePanesWidth(10000);
if (parseInt(dom.window.localStorage.getItem("serf-hub.panes.width"), 10) !== expectedMax)
  throw new Error("width not clamped to max (expected " + expectedMax + ")");
P.setSidePanesWidth(10);
if (parseInt(dom.window.localStorage.getItem("serf-hub.panes.width"), 10) !== 280)
  throw new Error("width not clamped to min");
console.log("test-panes width: ok");

// ---- PANE_MIN enforcement -------------------------------------------------
// Fresh DOM to isolate from the earlier open/close session (no stored width).
(function () {
  var dom2 = new JSDOM(`<!DOCTYPE html><body class="app">
    <main id="workspace"></main>
    <div id="pane-splitter" hidden></div>
    <aside id="side-panes" hidden></aside>
  </body>`, { url: "http://localhost/" });
  global.window = dom2.window; global.document = dom2.window.document;
  eval(fs.readFileSync(path.join(__dirname, "..", "assets", "panes.js"), "utf8"));
  var P2 = dom2.window.SerfPanes;
  var PANE_MIN = P2.PANE_MIN;

  // 1 pane: region width should be at least PANE_MIN.
  P2.open("/s/a", "A");
  var w1 = parseInt(dom2.window.localStorage.getItem("serf-hub.panes.width"), 10);
  if (isNaN(w1) || w1 < PANE_MIN)
    throw new Error("1 pane: side-region width " + w1 + " < PANE_MIN " + PANE_MIN);
  console.log("test-panes PANE_MIN 1-pane: ok (width=" + w1 + ")");

  // 2 panes: region width should be at least 2 × PANE_MIN.
  P2.open("/s/b", "B");
  var w2 = parseInt(dom2.window.localStorage.getItem("serf-hub.panes.width"), 10);
  if (isNaN(w2) || w2 < 2 * PANE_MIN)
    throw new Error("2 panes: side-region width " + w2 + " < 2×PANE_MIN " + (2 * PANE_MIN));
  console.log("test-panes PANE_MIN 2-pane: ok (width=" + w2 + ")");

  // 3 panes: region width should be at least 3 × PANE_MIN (capped by viewport).
  P2.open("/s/c", "C");
  var w3 = parseInt(dom2.window.localStorage.getItem("serf-hub.panes.width"), 10);
  var needed3 = 3 * PANE_MIN;
  var hardMax = Math.min(1200, dom2.window.innerWidth - 360);
  var expected3 = Math.min(needed3, hardMax); // capped by viewport when needed > max
  if (isNaN(w3) || w3 < expected3)
    throw new Error("3 panes: side-region width " + w3 + " < expected " + expected3);
  console.log("test-panes PANE_MIN 3-pane: ok (width=" + w3 + " expected≥" + expected3 + ")");

  // Splitter drag to a small value does NOT drop below PANE_MIN per pane when
  // 2 panes are open (user drag goes through setSidePanesWidth directly, so
  // it CAN go below; applyPaneMinWidth is only called on open/close). Verify
  // that restore() after a narrow drag re-applies the pane-count minimum.
  P2.close("/s/c"); // now 2 panes open
  // Force a narrow stored width (simulates splitter drag below the minimum).
  dom2.window.localStorage.setItem("serf-hub.panes.width", "200");
  // close+reopen pane triggers applyPaneMinWidth which should bump it up.
  P2.close("/s/b");
  P2.open("/s/b", "B"); // back to 2 panes; applyPaneMinWidth runs
  var wRestored = parseInt(dom2.window.localStorage.getItem("serf-hub.panes.width"), 10);
  if (isNaN(wRestored) || wRestored < 2 * PANE_MIN)
    throw new Error("after reopen with narrow stored width, expected ≥" + (2 * PANE_MIN) + ", got " + wRestored);
  console.log("test-panes PANE_MIN reopen bumps narrow width: ok (width=" + wRestored + ")");

  // restore() also enforces the minimum after reload.
  dom2.window.localStorage.setItem("serf-hub.panes.width", "100");
  // Simulate reload: tear down panes, restore.
  dom2.window.document.querySelectorAll("#side-panes .pane").forEach(function(n) { n.remove(); });
  dom2.window.document.getElementById("side-panes").hidden = true;
  P2.restore();
  // restore() reopens the persisted panes (a, b) and calls applyPaneMinWidth.
  var openCount = P2.openHrefs().length;
  var wAfterRestore = parseInt(dom2.window.localStorage.getItem("serf-hub.panes.width"), 10);
  if (isNaN(wAfterRestore) || wAfterRestore < openCount * PANE_MIN)
    throw new Error("restore(): width after restore " + wAfterRestore + " < " + openCount + "×PANE_MIN=" + (openCount * PANE_MIN));
  console.log("test-panes PANE_MIN restore enforces minimum: ok (width=" + wAfterRestore + " panes=" + openCount + ")");
}());


// ---- Splitter drag lifecycle regression ------------------------------------
(function () {
  var dom3 = new JSDOM(`<!DOCTYPE html><body class="app">
    <main id="workspace"></main>
    <div id="pane-splitter" hidden></div>
    <aside id="side-panes" hidden></aside>
  </body>`, { url: "http://localhost/" });
  global.window = dom3.window; global.document = dom3.window.document;
  Object.defineProperty(dom3.window, "innerWidth", { configurable: true, value: 1200 });
  eval(fs.readFileSync(path.join(__dirname, "..", "assets", "panes.js"), "utf8"));
  dom3.window.document.dispatchEvent(new dom3.window.Event("DOMContentLoaded", { bubbles: true }));
  var P3 = dom3.window.SerfPanes;
  P3.open("/s/drag-a", "Drag A");
  var split = dom3.window.document.getElementById("pane-splitter");
  var storedWidth = function () { return parseInt(dom3.window.localStorage.getItem("serf-hub.panes.width"), 10); };
  var mouse = function (type, clientX, buttons) {
    return new dom3.window.MouseEvent(type, { bubbles: true, cancelable: true, clientX: clientX, buttons: buttons });
  };

  split.dispatchEvent(mouse("mousedown", 700, 1));
  if (dom3.window.document.body.dataset.paneDragging !== "true") {
    throw new Error("splitter drag should mark body[data-pane-dragging=true] so iframes stop stealing shrink mousemove events");
  }
  dom3.window.document.dispatchEvent(mouse("mousemove", 500, 1));
  var widened = storedWidth();
  if (widened !== 700) throw new Error("active splitter drag left should widen side panes to 700, got " + widened);

  dom3.window.document.dispatchEvent(mouse("mousemove", 900, 1));
  var shrunk = storedWidth();
  if (shrunk >= widened) throw new Error("active splitter drag right should shrink side panes / widen main pane, got " + shrunk + " after " + widened);

  dom3.window.document.dispatchEvent(mouse("mousemove", 450, 0));
  var afterReleasedMove = storedWidth();
  if (afterReleasedMove !== shrunk) throw new Error("mousemove with no pressed button must not keep resizing after drag release; before=" + shrunk + " after=" + afterReleasedMove);

  dom3.window.document.dispatchEvent(mouse("mouseup", 450, 0));
  if (dom3.window.document.body.dataset.paneDragging) {
    throw new Error("splitter drag should clear body[data-pane-dragging] on mouseup");
  }
  dom3.window.document.dispatchEvent(mouse("mousemove", 520, 0));
  var afterMouseupHover = storedWidth();
  if (afterMouseupHover !== shrunk) throw new Error("hover/contact after mouseup must not resize; before=" + shrunk + " after=" + afterMouseupHover);
  console.log("test-panes splitter drag lifecycle: ok");
}());


// ---- Width model contract ---------------------------------------------------
(function () {
  var css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");
  if (!/\.side-panes\s*\{[^}]*flex:\s*0\s+0\s+var\(--side-panes-w,\s*420px\)/m.test(css)) {
    throw new Error("side-panes must use a non-shrinking/non-growing flex basis tied to --side-panes-w so splitter drags visibly resize it");
  }
  if (!/\.pane\s*\{[^}]*min-width:\s*var\(--pane-min,/m.test(css)) {
    throw new Error("pane min-width must be driven by the shared --pane-min CSS variable");
  }
  if (!/body\.app\[data-pane-dragging="true"\]\s+\.pane-frame\s*\{[^}]*pointer-events:\s*none/m.test(css)) {
    throw new Error("pane iframes must ignore pointer events while the host splitter is dragging");
  }
  console.log("test-panes width model CSS contract: ok");
}());

console.log("test-panes: ok");
process.exit(0);
