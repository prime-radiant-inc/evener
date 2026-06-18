// panes.js — host-side multi-pane manager. Each side pane is an <iframe> loading an
// existing /s/<id> route, so the single-instance renderer runs unchanged per pane.
(function () {
  "use strict";
  var MAX_SIDE_PANES = 3;
  // Minimum width per pane. When multiple panes are open the side-region must
  // grow to honor each pane's readable minimum rather than compressing panes
  // to a fraction of a fixed 420px default.
  var PANE_MIN = 300;

  function region() { return document.getElementById("side-panes"); }
  function splitter() { return document.getElementById("pane-splitter"); }

  function paneFor(href) {
    var r = region();
    if (!r) return null;
    var frames = r.querySelectorAll(".pane-frame");
    for (var i = 0; i < frames.length; i++) {
      if (frames[i].getAttribute("src") === href) return frames[i].closest(".pane");
    }
    return null;
  }

  function openHrefs() {
    var r = region();
    if (!r) return [];
    return Array.prototype.map.call(r.querySelectorAll(".pane-frame"), function (f) {
      return f.getAttribute("src");
    });
  }

  function showRegion(show) {
    var r = region(), s = splitter();
    if (r) r.hidden = !show;
    if (s) s.hidden = !show;
  }

  function open(href, title) {
    if (!href) return null;
    var r = region();
    if (!r) return null;
    var existing = paneFor(href);
    if (existing) { existing.querySelector(".pane-frame").focus(); return existing; }
    if (r.querySelectorAll(".pane").length >= MAX_SIDE_PANES) return null;

    var pane = document.createElement("section");
    pane.className = "pane";
    var head = document.createElement("header");
    head.className = "pane-header";
    var t = document.createElement("span");
    t.className = "pane-title";
    t.textContent = title || href;
    var x = document.createElement("button");
    x.type = "button";
    x.className = "pane-close";
    x.setAttribute("aria-label", "close pane");
    x.textContent = "✕";
    x.addEventListener("click", function () { close(href); });
    head.appendChild(t); head.appendChild(x);
    var frame = document.createElement("iframe");
    frame.className = "pane-frame";
    frame.setAttribute("src", href);
    frame.setAttribute("title", title || href);
    pane.appendChild(head); pane.appendChild(frame);
    r.appendChild(pane);
    showRegion(true);
    applyPaneMinWidth();
    persist();
    return pane;
  }

  function close(href) {
    var pane = paneFor(href);
    if (pane) pane.remove();
    if (region() && region().querySelectorAll(".pane").length === 0) showRegion(false);
    applyPaneMinWidth();
    persist();
  }

  var STORE_KEY = "serf-hub.panes";
  var WIDTH_KEY = "serf-hub.panes.width";

  function setSidePanesWidth(px) {
    var maxW = Math.min(1200, window.innerWidth - 360);
    var w = Math.max(280, Math.min(maxW, Math.round(px)));
    var r = region();
    if (r) r.style.setProperty("--side-panes-w", w + "px");
    try { window.localStorage.setItem(WIDTH_KEY, String(w)); } catch (e) { /* ignore */ }
    return w;
  }

  // applyPaneMinWidth ensures the side-region is wide enough that every open
  // pane has at least PANE_MIN pixels. When paneCount × PANE_MIN exceeds the
  // stored/current width, the region grows to the needed size. This is called
  // after open() and close() so the region tracks the pane count.
  function applyPaneMinWidth() {
    var r = region();
    if (!r) return;
    var paneCount = r.querySelectorAll(".pane").length;
    if (paneCount === 0) return;
    var needed = paneCount * PANE_MIN;
    var stored;
    try { stored = parseInt(window.localStorage.getItem(WIDTH_KEY), 10); } catch (e) { stored = 0; }
    var current = stored || 420; // default matches CSS --side-panes-w default
    setSidePanesWidth(Math.max(current, needed));
  }

  function restoreWidth() {
    var v; try { v = parseInt(window.localStorage.getItem(WIDTH_KEY), 10); } catch (e) { return; }
    if (v) setSidePanesWidth(v);
    // After restoring saved width, honour the per-pane minimum for the restored pane count.
    applyPaneMinWidth();
  }

  // Drag handler (verified manually; logic delegates to setSidePanesWidth).
  function bindSplitter() {
    var s = splitter(); if (!s || s.__bound) return; s.__bound = true;
    s.addEventListener("mousedown", function (e) {
      e.preventDefault();
      function move(ev) {
        // splitter sits between #workspace and #side-panes; panes grow as the
        // pointer moves left. Width = distance from pointer to right viewport edge.
        setSidePanesWidth(window.innerWidth - ev.clientX);
      }
      function up() {
        document.removeEventListener("mousemove", move);
        document.removeEventListener("mouseup", up);
      }
      document.addEventListener("mousemove", move);
      document.addEventListener("mouseup", up);
    });
  }

  function persist() {
    var r = region();
    if (!r) return;
    var data = Array.prototype.map.call(r.querySelectorAll(".pane"), function (p) {
      var f = p.querySelector(".pane-frame");
      var t = p.querySelector(".pane-title");
      return { href: f.getAttribute("src"), title: t ? t.textContent : "" };
    });
    try { window.localStorage.setItem(STORE_KEY, JSON.stringify(data)); } catch (e) { /* ignore */ }
  }

  function restore() {
    var raw;
    try { raw = window.localStorage.getItem(STORE_KEY); } catch (e) { return; }
    if (!raw) return;
    var data;
    try { data = JSON.parse(raw); } catch (e) { return; }
    if (!Array.isArray(data)) return;
    data.forEach(function (p) { if (p && p.href) open(p.href, p.title); });
  }

  window.SerfPanes = { open: open, close: close, openHrefs: openHrefs, restore: restore, setSidePanesWidth: setSidePanesWidth, MAX_SIDE_PANES: MAX_SIDE_PANES, PANE_MIN: PANE_MIN, _persist: persist };

  function onLoad() { restore(); bindSplitter(); restoreWidth(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", onLoad);
  } else { onLoad(); }
})();
