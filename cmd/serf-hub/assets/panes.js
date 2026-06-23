// panes.js — host-side multi-pane manager. Each side pane is an <iframe> loading a
// standalone thread document route, so pane chrome stays compact and isolated.
(function () {
  "use strict";
  var MAX_SIDE_PANES = 3;
  // Minimum width per pane. When multiple panes are open the side-region must
  // grow to honor each pane's readable minimum rather than compressing panes
  // to a fraction of a fixed 420px default.
  var PANE_MIN = 300;

  function region() { return document.getElementById("side-panes"); }
  function splitter() { return document.getElementById("pane-splitter"); }

  function threadHref(ref) {
    ref = String(ref || "").trim();
    return ref ? "/thread/" + encodeURIComponent(ref) : "";
  }

  function normalizePaneHref(href) {
    href = String(href || "").trim();
    if (!href) return "";
    try {
      var u = new URL(href, window.location.origin);
      if (u.origin !== window.location.origin) return href;
      if (u.pathname.indexOf("/thread/") === 0) return u.pathname + u.search + u.hash;
      if (u.pathname.indexOf("/s/") === 0) {
        var rest = u.pathname.slice(3);
        if (rest && rest.indexOf("/") === -1) {
          return threadHref(decodeURIComponent(rest));
        }
      }
    } catch (e) {
      if (href.indexOf("/thread/") === 0) return href;
      if (href.indexOf("/s/") === 0) {
        var id = href.slice(3);
        if (id && id.indexOf("/") === -1 && id.indexOf("?") === -1 && id.indexOf("#") === -1) {
          return threadHref(decodeURIComponent(id));
        }
      }
    }
    return href;
  }

  function paneFor(href) {
    var r = region();
    if (!r) return null;
    var frames = r.querySelectorAll(".pane-frame");
    for (var i = 0; i < frames.length; i++) {
      if (frames[i].getAttribute("src") === href) return frames[i].closest(".pane");
    }
    return null;
  }

  function clearPaneLoadTimer(pane) {
    if (pane && pane.__loadTimer) {
      window.clearTimeout(pane.__loadTimer);
      pane.__loadTimer = null;
    }
  }

  function markLoading(pane, href) {
    if (!pane) return;
    clearPaneLoadTimer(pane);
    pane.dataset.state = "loading";
    pane.__loadTimer = window.setTimeout(function () {
      markError(href, "Pane did not finish loading");
    }, 15000);
  }

  function markError(href, message) {
    href = normalizePaneHref(href);
    var pane = paneFor(href);
    if (!pane) return null;
    clearPaneLoadTimer(pane);
    pane.dataset.state = "error";
    var existing = pane.querySelector(".pane-error");
    if (existing) existing.remove();
    var err = document.createElement("div");
    err.className = "pane-error";
    var text = document.createElement("div");
    text.className = "pane-error-text";
    text.textContent = message || "Pane failed to load";
    var retry = document.createElement("button");
    retry.type = "button";
    retry.className = "btn btn-secondary";
    retry.dataset.paneRetry = "";
    retry.textContent = "retry";
    retry.addEventListener("click", function () {
      var frame = pane.querySelector(".pane-frame");
      if (frame) {
        err.remove();
        markLoading(pane, href);
        frame.setAttribute("src", href);
      }
    });
    var closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "btn btn-secondary";
    closeBtn.dataset.paneErrorClose = "";
    closeBtn.textContent = "close";
    closeBtn.addEventListener("click", function () { close(href); });
    err.appendChild(text);
    err.appendChild(retry);
    err.appendChild(closeBtn);
    pane.appendChild(err);
    return pane;
  }

  function openHrefs() {
    var r = region();
    if (!r) return [];
    return Array.prototype.map.call(r.querySelectorAll(".pane-frame"), function (f) {
      return normalizePaneHref(f.getAttribute("src"));
    });
  }

  function isKnownPaneSource(source) {
    var r = region();
    if (!r || !source) return false;
    var frames = r.querySelectorAll(".pane-frame");
    for (var i = 0; i < frames.length; i++) {
      if (frames[i].contentWindow === source) return true;
    }
    return false;
  }

  function isPaneSafeHref(href) {
    href = normalizePaneHref(href);
    if (!href) return false;
    try {
      var u = new URL(href, window.location.origin);
      if (u.origin !== window.location.origin) return false;
      return u.pathname.indexOf("/thread/") === 0 || u.pathname.indexOf("/doc/") === 0;
    } catch (e) {
      return href.indexOf("/thread/") === 0 || href.indexOf("/doc/") === 0;
    }
  }

  function openFromChild(source, href, title) {
    href = normalizePaneHref(href);
    if (!isKnownPaneSource(source)) return null;
    if (!isPaneSafeHref(href)) return null;
    return open(href, String(title || href));
  }

  function onMessage(e) {
    if (!e || e.origin !== window.location.origin) return;
    var data = e.data || {};
    if (data.type !== "serf:open-beside") return;
    openFromChild(e.source, data.href, data.title);
  }

  function showRegion(show) {
    var r = region(), s = splitter();
    if (r) r.hidden = !show;
    if (s) s.hidden = !show;
  }

  function open(href, title) {
    href = normalizePaneHref(href);
    if (!href) return null;
    var r = region();
    if (!r) {
      if (window.parent && window.parent !== window) {
        window.parent.postMessage({ type: "serf:open-beside", href: href, title: title || href }, window.location.origin);
      }
      return null;
    }
    // An explicit open of a previously-dismissed href clears its suppression:
    // the user asked for it back, so auto-open may bring it back too.
    unsuppress(href);
    var existing = paneFor(href);
    if (existing) { existing.querySelector(".pane-frame").focus(); return existing; }
    if (r.querySelectorAll(".pane").length >= MAX_SIDE_PANES) return null;

    var pane = document.createElement("section");
    pane.className = "pane";
    pane.dataset.state = "loading";
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
    frame.addEventListener("load", function () {
      clearPaneLoadTimer(pane);
      if (pane.dataset.state !== "error") pane.dataset.state = "ready";
    });
    frame.addEventListener("error", function () {
      markError(href, "Pane failed to load");
    });
    markLoading(pane, href);
    pane.appendChild(head); pane.appendChild(frame);
    r.appendChild(pane);
    showRegion(true);
    applyPaneMinWidth();
    persist();
    return pane;
  }

  function close(href) {
    href = normalizePaneHref(href);
    var pane = paneFor(href);
    if (pane) {
      clearPaneLoadTimer(pane);
      pane.remove();
    }
    if (region() && region().querySelectorAll(".pane").length === 0) showRegion(false);
    // Remember the user's dismissal so auto-open does not re-open this href on
    // the next init/navigation. A later explicit open() clears the suppression.
    suppress(href);
    applyPaneMinWidth();
    persist();
  }

  var STORE_KEY = "serf-hub.panes";
  var WIDTH_KEY = "serf-hub.panes.width";
  var CLOSED_KEY = "serf-hub.panes.closed";

  // Suppression memory: hrefs the user explicitly closed. Auto-open consults
  // isSuppressed() and skips these so a dismissed pane stays dismissed across
  // re-init and reload. Persisted as a JSON array of hrefs.
  function readClosed() {
    var raw;
    try { raw = window.localStorage.getItem(CLOSED_KEY); } catch (e) { return []; }
    if (!raw) return [];
    try { var a = JSON.parse(raw); return Array.isArray(a) ? a : []; } catch (e) { return []; }
  }

  function writeClosed(list) {
    try { window.localStorage.setItem(CLOSED_KEY, JSON.stringify(list)); } catch (e) { /* ignore */ }
  }

  function isSuppressed(href) {
    href = normalizePaneHref(href);
    return readClosed().indexOf(href) !== -1;
  }

  function suppress(href) {
    href = normalizePaneHref(href);
    if (!href) return;
    var list = readClosed();
    if (list.indexOf(href) === -1) { list.push(href); writeClosed(list); }
  }

  function unsuppress(href) {
    href = normalizePaneHref(href);
    if (!href) return;
    var list = readClosed();
    var i = list.indexOf(href);
    if (i !== -1) { list.splice(i, 1); writeClosed(list); }
  }

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

  // refreshURL encodes the current open pane hrefs as repeated pane= query
  // params on the current URL path, preserving any non-pane query params.
  // This is how open/close make the layout shareable via URL.
  // We always use window.location.pathname so this survives the renderer's
  // own history.replaceState calls (it writes /s/<id>; we read it back).
  function refreshURL() {
    try {
      var sp = new window.URLSearchParams(window.location.search);
      sp.delete("pane");
      var hrefs = openHrefs();
      for (var i = 0; i < hrefs.length; i++) {
        sp.append("pane", hrefs[i]);
      }
      var qs = sp.toString();
      var url = window.location.pathname + (qs ? "?" + qs : "");
      window.history.replaceState(null, "", url);
    } catch (e) { /* ignore in environments without history API */ }
  }

  function persist() {
    var r = region();
    if (!r) return;
    var data = Array.prototype.map.call(r.querySelectorAll(".pane"), function (p) {
      var f = p.querySelector(".pane-frame");
      var t = p.querySelector(".pane-title");
      return { href: normalizePaneHref(f.getAttribute("src")), title: t ? t.textContent : "" };
    });
    try { window.localStorage.setItem(STORE_KEY, JSON.stringify(data)); } catch (e) { /* ignore */ }
    refreshURL();
  }

  function restore() {
    // URL pane= params are the source of truth when present (shared link).
    // Fall back to localStorage when no pane= params exist in the URL.
    var urlPanes = [];
    try {
      var sp = new window.URLSearchParams(window.location.search);
      urlPanes = sp.getAll("pane");
    } catch (e) { /* ignore */ }

    if (urlPanes.length > 0) {
      // URL-specified panes: open each, bypassing suppression (an explicit
      // share link is a deliberate request so local dismissals are overridden).
      urlPanes.forEach(function (href) {
        href = normalizePaneHref(href);
        if (!href) return;
        // Clear any local suppression so the pane can open.
        unsuppress(href);
        open(href, href);
      });
      return;
    }

    // No URL panes — fall back to localStorage.
    var raw;
    try { raw = window.localStorage.getItem(STORE_KEY); } catch (e) { return; }
    if (!raw) return;
    var data;
    try { data = JSON.parse(raw); } catch (e) { return; }
    if (!Array.isArray(data)) return;
    data.forEach(function (p) { if (p && p.href) open(normalizePaneHref(p.href), p.title); });
  }

  window.SerfPanes = { open: open, close: close, openHrefs: openHrefs, restore: restore, isSuppressed: isSuppressed, setSidePanesWidth: setSidePanesWidth, threadHref: threadHref, normalizePaneHref: normalizePaneHref, openFromChild: openFromChild, isPaneSafeHref: isPaneSafeHref, markError: markError, MAX_SIDE_PANES: MAX_SIDE_PANES, PANE_MIN: PANE_MIN, _persist: persist };
  window.addEventListener("message", onMessage);

  function onLoad() { restore(); bindSplitter(); restoreWidth(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", onLoad);
  } else { onLoad(); }
})();
