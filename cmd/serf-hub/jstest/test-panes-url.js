// test-panes-url.js — tests for shareable URL encoding of side pane layouts.
//
// Each test block uses a fresh JSDOM with a custom URL so we can verify that:
//   - open() adds pane= params to the URL (path preserved)
//   - close() removes the corresponding pane= param
//   - restore() prefers URL pane= params over localStorage
//   - a URL-specified pane opens even if it's in the suppressed set
//   - existing non-pane query params are preserved
//   - MAX_SIDE_PANES cap is respected on URL restore
"use strict";

const { JSDOM } = require("jsdom");
const fs = require("fs");
const path = require("path");

const PANES_SRC = fs.readFileSync(
  path.join(__dirname, "..", "assets", "panes.js"),
  "utf8"
);

// makeDOM creates a fresh JSDOM and loads panes.js into it.
// Sets global.window/document so that eval(PANES_SRC) works (panes.js refers
// to `window` as a global, matching how it runs in the browser).
function makeDOM(url) {
  url = url || "http://localhost/";
  const dom = new JSDOM(
    `<!DOCTYPE html><body class="app">
      <main id="workspace"></main>
      <div id="pane-splitter" hidden></div>
      <aside id="side-panes" hidden></aside>
    </body>`,
    { url: url }
  );
  global.window = dom.window;
  global.document = dom.window.document;
  eval(PANES_SRC); // eslint-disable-line no-eval
  return dom;
}

function searchParams(dom) {
  return new dom.window.URLSearchParams(dom.window.location.search);
}

function currentPath(dom) {
  return dom.window.location.pathname;
}

// ---- Test: open() adds pane= params; path is preserved ----------------------
(function testOpenAddsPaneParam() {
  const dom = makeDOM("http://localhost/s/main-session");
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");
  var sp = searchParams(dom);
  var panes = sp.getAll("pane");
  if (panes.length !== 1)
    throw new Error(
      "open: expected 1 pane= param, got " + panes.length
    );
  if (panes[0] !== "/thread/sub-1")
    throw new Error("open: wrong pane= value: " + panes[0]);
  if (currentPath(dom) !== "/s/main-session")
    throw new Error(
      "open: path changed; expected /s/main-session, got " + currentPath(dom)
    );
  console.log("test-panes-url open adds pane param: ok");
}());

// ---- Test: opening a second pane adds a second pane= param -----------------
(function testOpenTwoPanes() {
  const dom = makeDOM("http://localhost/s/main-session");
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");
  P.open("/s/sub-2", "Sub 2");
  var panes = searchParams(dom).getAll("pane");
  if (panes.length !== 2)
    throw new Error("two opens: expected 2 pane= params, got " + panes.length);
  if (panes[0] !== "/thread/sub-1" || panes[1] !== "/thread/sub-2")
    throw new Error("two opens: wrong pane= values: " + panes.join(","));
  console.log("test-panes-url open two panes adds two params: ok");
}());

// ---- Test: close() removes the pane= param ----------------------------------
(function testCloseRemovesPaneParam() {
  const dom = makeDOM("http://localhost/s/main-session");
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");
  P.open("/s/sub-2", "Sub 2");
  P.close("/s/sub-1");
  var panes = searchParams(dom).getAll("pane");
  if (panes.length !== 1)
    throw new Error("close: expected 1 pane= param, got " + panes.length);
  if (panes[0] !== "/thread/sub-2")
    throw new Error("close: wrong remaining pane: " + panes[0]);
  if (currentPath(dom) !== "/s/main-session")
    throw new Error("close: path changed");
  console.log("test-panes-url close removes pane param: ok");
}());

// ---- Test: closing all panes leaves no pane= params in URL ------------------
(function testCloseAllRemovesParams() {
  const dom = makeDOM("http://localhost/s/main-session");
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");
  P.close("/s/sub-1");
  var panes = searchParams(dom).getAll("pane");
  if (panes.length !== 0)
    throw new Error(
      "close all: expected 0 pane= params, got " + panes.length
    );
  console.log("test-panes-url close all leaves no pane params: ok");
}());

// ---- Test: non-pane query params are preserved on open/close ----------------
(function testNonPaneParamsPreserved() {
  const dom = makeDOM(
    "http://localhost/s/main-session?foo=bar&baz=qux"
  );
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");
  var sp = searchParams(dom);
  if (sp.get("foo") !== "bar")
    throw new Error("non-pane: foo param lost after open");
  if (sp.get("baz") !== "qux")
    throw new Error("non-pane: baz param lost after open");
  if (sp.getAll("pane").length !== 1)
    throw new Error("non-pane: pane param not added");

  P.close("/s/sub-1");
  var sp2 = searchParams(dom);
  if (sp2.get("foo") !== "bar")
    throw new Error("non-pane: foo param lost after close");
  if (sp2.get("baz") !== "qux")
    throw new Error("non-pane: baz param lost after close");
  if (sp2.getAll("pane").length !== 0)
    throw new Error("non-pane: pane param not removed on close");
  console.log("test-panes-url non-pane params preserved: ok");
}());

// ---- Test: restore() reads pane= params from URL when present ---------------
(function testRestoreFromURL() {
  // Encode href in URL manually as a fresh "incoming share link" scenario.
  var href1 = "/s/shared-sub-1";
  var href2 = "/s/shared-sub-2";
  var url =
    "http://localhost/s/main-session?pane=" +
    encodeURIComponent(href1) +
    "&pane=" +
    encodeURIComponent(href2);
  const dom = makeDOM(url);
  const P = dom.window.SerfPanes;

  // Ensure localStorage has NO stored panes — only URL should be consulted.
  dom.window.localStorage.removeItem("serf-hub.panes");

  // Call restore() the way onLoad does after a fresh page load.
  P.restore();

  var open = P.openHrefs();
  if (open.indexOf("/thread/shared-sub-1") === -1)
    throw new Error("URL restore: href1 not opened");
  if (open.indexOf("/thread/shared-sub-2") === -1)
    throw new Error("URL restore: href2 not opened");
  console.log("test-panes-url restore from URL: ok");
}());

// ---- Test: restore normalizes legacy /s session pane hrefs to thread documents --
(function testRestoreNormalizesLegacySessionPaneHref() {
  const dom = makeDOM("http://localhost/s/parent?pane=%2Fs%2Flocal%253Achild-A");
  const P = dom.window.SerfPanes;

  P.restore();

  var hrefs = P.openHrefs();
  if (hrefs.length !== 1)
    throw new Error("restore normalize: expected one restored pane, got " + hrefs.length);
  if (hrefs[0] !== "/thread/local%3Achild-A")
    throw new Error("restore normalize: wrong restored href: " + hrefs[0]);
  console.log("test-panes-url restore normalizes legacy /s pane hrefs: ok");
}());

// ---- Test: URL pane= takes precedence over localStorage on restore ----------
(function testURLTakesPrecedenceOverStorage() {
  var urlHref = "/s/url-pane";
  var lsHref = "/s/ls-only-pane";
  var url =
    "http://localhost/s/session?pane=" + encodeURIComponent(urlHref);
  const dom = makeDOM(url);
  const P = dom.window.SerfPanes;

  // Pre-populate localStorage with a different pane.
  dom.window.localStorage.setItem(
    "serf-hub.panes",
    JSON.stringify([{ href: lsHref, title: "LS Only" }])
  );

  P.restore();

  var open = P.openHrefs();
  if (open.indexOf("/thread/url-pane") === -1)
    throw new Error("URL precedence: URL pane not opened");
  // localStorage pane should NOT be opened when URL has pane= params.
  if (open.indexOf("/thread/ls-only-pane") !== -1)
    throw new Error(
      "URL precedence: localStorage pane was opened despite URL having pane= params"
    );
  console.log("test-panes-url URL takes precedence over localStorage: ok");
}());

// ---- Test: no pane= params → falls back to localStorage restore -------------
(function testFallbackToLocalStorage() {
  var lsHref = "/s/from-storage";
  const dom = makeDOM("http://localhost/s/session");
  const P = dom.window.SerfPanes;

  dom.window.localStorage.setItem(
    "serf-hub.panes",
    JSON.stringify([{ href: lsHref, title: "From Storage" }])
  );

  P.restore();

  var open = P.openHrefs();
  if (open.indexOf("/thread/from-storage") === -1)
    throw new Error("localStorage fallback: stored pane not restored");
  console.log("test-panes-url falls back to localStorage when no URL params: ok");
}());

// ---- Test: URL-specified suppressed pane opens and suppression is cleared ---
(function testURLPaneOverridesSuppression() {
  var href = "/s/previously-closed";
  // Simulate a URL share of a pane the local user had previously closed.
  var url =
    "http://localhost/s/session?pane=" + encodeURIComponent(href);
  const dom = makeDOM(url);
  const P = dom.window.SerfPanes;

  // Plant a suppression for this href.
  dom.window.localStorage.setItem(
    "serf-hub.panes.closed",
    JSON.stringify([href])
  );

  P.restore();

  var open = P.openHrefs();
  if (open.indexOf("/thread/previously-closed") === -1)
    throw new Error("URL suppression override: suppressed pane not opened");
  // Suppression should now be cleared so future auto-open can work normally.
  if (P.isSuppressed("/thread/previously-closed"))
    throw new Error("URL suppression override: isSuppressed still true after URL restore");
  console.log("test-panes-url URL-specified pane overrides suppression: ok");
}());

// ---- Test: URL restore respects MAX_SIDE_PANES cap --------------------------
(function testURLRestoreRespectsMax() {
  var hrefs = [
    "/s/p1", "/s/p2", "/s/p3", "/s/p4", "/s/p5",
  ];
  var params = hrefs.map(function (h) {
    return "pane=" + encodeURIComponent(h);
  });
  var url = "http://localhost/s/session?" + params.join("&");
  const dom = makeDOM(url);
  const P = dom.window.SerfPanes;

  P.restore();

  var open = P.openHrefs();
  if (open.length > P.MAX_SIDE_PANES)
    throw new Error(
      "URL restore cap: opened " +
        open.length +
        " panes, expected max " +
        P.MAX_SIDE_PANES
    );
  console.log(
    "test-panes-url URL restore respects MAX_SIDE_PANES: ok (opened " +
      open.length +
      "/" +
      hrefs.length +
      ")"
  );
}());

// ---- Test: URL encode/decode round-trip for hrefs with query strings --------
(function testURLEncodeDecodeQueryHref() {
  // Doc panes have hrefs like /doc/file?session=...&path=...
  var docHref = "/doc/file?session=01TEST&path=/work/repo/foo.go";
  const dom = makeDOM("http://localhost/s/session");
  const P = dom.window.SerfPanes;

  P.open(docHref, "foo.go");

  // The pane= value in the URL must encode the href correctly so it round-trips.
  var panes = searchParams(dom).getAll("pane");
  if (panes.length !== 1)
    throw new Error("doc href encode: expected 1 pane= param");
  if (panes[0] !== docHref)
    throw new Error(
      "doc href encode: round-trip failed; got " + panes[0]
    );
  console.log("test-panes-url doc href with query string round-trips: ok");
}());

// ---- Test: pane= params preserved in localStorage alongside URL encode ------
(function testPersistStillWorksWithURL() {
  const dom = makeDOM("http://localhost/s/session");
  const P = dom.window.SerfPanes;

  P.open("/s/sub-1", "Sub 1");

  // localStorage must still be updated (belt-and-suspenders: URL + storage).
  var stored = dom.window.localStorage.getItem("serf-hub.panes");
  if (!stored) throw new Error("persist: localStorage not written");
  var data = JSON.parse(stored);
  if (!data.some(function (p) { return p.href === "/thread/sub-1"; }))
    throw new Error("persist: /thread/sub-1 not found in stored data");
  console.log("test-panes-url localStorage persist still works alongside URL: ok");
}());

console.log("test-panes-url: all tests passed");
process.exit(0);
