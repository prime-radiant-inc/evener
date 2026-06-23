const fs = require("fs");
const { JSDOM } = require("jsdom");

function loadScript(window, path) {
  window.eval(fs.readFileSync(path, "utf8"));
}

function dispatchChildMessage(host, child, data) {
  host.dispatchEvent(new host.MessageEvent("message", {
    data,
    origin: child.location.origin,
    source: child,
  }));
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

function installRendererHarness(child) {
  child.document.open();
  child.document.write(`<!DOCTYPE html><html><body>
    <a class="subagent-parent-up" href="/thread/local%3Aparent" data-open-parent-beside="/thread/local%3Aparent">↑ Parent</a>
    <div id="conversation" data-session-id="local:child" data-state="active"></div>
    <form data-input-form data-session-id="local:child"><textarea class="message-input"></textarea></form>
  </body></html>`);
  child.document.close();
  child.marked = { parse: t => String(t || "") };
  child.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(child);
  child.SerfRenderer.init(child.document.getElementById("conversation"));
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

  const child = frame.contentWindow;
  const originalPostMessage = host.postMessage;
  host.postMessage = function (data) { dispatchChildMessage(host, child, data); };
  loadScript(child, "../assets/panes.js");
  const localPane = child.SerfPanes.open("/thread/local%3Abridged", "bridged");
  host.postMessage = originalPostMessage;
  pass(!localPane, "framed child with no local pane host returns no local pane");
  pass(host.SerfPanes.openHrefs().includes("/thread/local%3Abridged"), "framed child SerfPanes.open posts bridge request to host");

  const childDom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/thread/local%3Achild" });
  const breadcrumbChild = childDom.window;
  let posted = null;
  breadcrumbChild.parent = { postMessage: (msg, origin) => { posted = { msg, origin }; } };
  installRendererHarness(breadcrumbChild);
  breadcrumbChild.SerfRenderer.isInPane = () => true;
  const parentLink = breadcrumbChild.document.querySelector("[data-open-parent-beside]");
  parentLink.dispatchEvent(new breadcrumbChild.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(!!posted && posted.origin === "http://127.0.0.1:9180" && posted.msg.type === "serf:open-beside" && posted.msg.href === "/thread/local%3Aparent", "framed thread breadcrumb posts parent open request to host bridge");

  if (!allPass) process.exit(1);
  console.log("OK\ttest-thread-document-bridge.js");
  process.exit(0);
})();
