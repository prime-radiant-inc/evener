const fs = require("fs");
const { JSDOM } = require("jsdom");

function loadScript(window, path) {
  window.eval(fs.readFileSync(path, "utf8"));
}

function newHostHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <main id="workspace"></main>
    <div id="pane-splitter" hidden></div>
    <aside id="side-panes" hidden></aside>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/s/parent" });
  const { window } = dom;
  loadScript(window, "../assets/panes.js");
  return window;
}

let allPass = true;
function pass(ok, msg) {
  console.log((ok ? "PASS" : "FAIL") + " — " + msg);
  if (!ok) allPass = false;
}

(function () {
  const host = newHostHarness();
  const pane = host.SerfPanes.open("/thread/local%3Achild", "child");
  const frame = pane && pane.querySelector("iframe");
  pass(!!frame, "host opens initial pane");

  const opened = host.SerfPanes.openFromChild(frame.contentWindow, "/thread/local%3Agrandchild", "grandchild");
  pass(!!opened, "known child frame can open a pane through host bridge");
  pass(host.SerfPanes.openHrefs().includes("/thread/local%3Agrandchild"), "host bridge opens requested thread href");

  const rejectedExternal = host.SerfPanes.openFromChild(frame.contentWindow, "https://example.com/thread/x", "bad");
  pass(!rejectedExternal, "host bridge rejects cross-origin hrefs");

  const unknown = { closed: false };
  const rejectedUnknown = host.SerfPanes.openFromChild(unknown, "/thread/local%3Aintruder", "intruder");
  pass(!rejectedUnknown, "host bridge rejects unknown source windows");

  if (!allPass) process.exit(1);
  console.log("OK\ttest-thread-document-bridge.js");
})();
