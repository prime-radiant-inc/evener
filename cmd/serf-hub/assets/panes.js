// panes.js — host-side multi-pane manager. Each side pane is an <iframe> loading an
// existing /s/<id> route, so the single-instance renderer runs unchanged per pane.
(function () {
  "use strict";
  var MAX_SIDE_PANES = 3;

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
    persist();
    return pane;
  }

  function close(href) {
    var pane = paneFor(href);
    if (pane) pane.remove();
    if (region() && region().querySelectorAll(".pane").length === 0) showRegion(false);
    persist();
  }

  var STORE_KEY = "serf-hub.panes";

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

  window.SerfPanes = { open: open, close: close, openHrefs: openHrefs, restore: restore, MAX_SIDE_PANES: MAX_SIDE_PANES, _persist: persist };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", restore);
  } else { restore(); }
})();
