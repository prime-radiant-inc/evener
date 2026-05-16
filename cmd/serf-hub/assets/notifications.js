// Browser notifications for serf-hub. Reads opt-in prefs from
// localStorage["serf-hub.notifications"] and drives four channels:
//   - title-bar count of awaiting sessions
//   - favicon dot for highest-attention state
//   - OS notification on idle->awaiting and processing->errored transitions
//   - short tone on the same transitions
//
// Polls /api/search?q= every 5s for live state. Settings pane dispatches
// the "serf-hub:notifications-changed" event when the user toggles a
// preference; this module re-reads prefs (and re-applies title/favicon)
// without a reload.
(function () {
  "use strict";

  const PREFS_KEY = "serf-hub.notifications";
  const POLL_MS = 5000;
  const PLAIN_FAVICON =
    "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237aa2f7'/></svg>";

  // Highest priority first. Transitions of interest below use these names.
  const STATE_PRIORITY = ["errored", "awaiting", "processing", "idle"];
  const STATE_COLORS = {
    errored: "#f7768e",
    awaiting: "#e0af68",
    processing: "#7aa2f7",
    idle: "#9ece6a",
  };

  // Per-session previous state, used to detect transitions.
  const prevState = new Map();
  let pollTimer = null;
  let initialized = false;

  function readPrefs() {
    try {
      return JSON.parse(localStorage.getItem(PREFS_KEY) || "{}") || {};
    } catch (e) {
      return {};
    }
  }

  function writePrefs(prefs) {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  }

  function activeTitle() {
    const header = document.querySelector(".workspace-header .workspace-title .title");
    if (header && header.textContent) return header.textContent.trim();
    return "serf";
  }

  function applyTitle(prefs, live) {
    const title = activeTitle();
    if (!prefs.title) {
      document.title = title === "serf" ? "serf" : title;
      return;
    }
    const awaitingCount = (live || []).filter((s) => s.state === "awaiting").length;
    if (awaitingCount > 0) {
      document.title = "(" + awaitingCount + ") serf — " + title;
    } else {
      document.title = "serf — " + title;
    }
  }

  // Pick the highest-priority state across live sessions.
  function topState(live) {
    const present = new Set((live || []).map((s) => s.state));
    for (const s of STATE_PRIORITY) {
      if (present.has(s)) return s;
    }
    return null;
  }

  function buildFaviconDataURI(dotColor) {
    // Base circle plus a small dot in the corner if a state is active.
    let svg =
      "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'>" +
      "<circle cx='50' cy='50' r='40' fill='#7aa2f7'/>";
    if (dotColor) {
      svg +=
        "<circle cx='78' cy='78' r='18' fill='" +
        dotColor +
        "' stroke='#1a1b26' stroke-width='4'/>";
    }
    svg += "</svg>";
    return "data:image/svg+xml;utf8," + svg.replace(/#/g, "%23");
  }

  function setFavicon(href) {
    let link = document.querySelector("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    link.href = href;
  }

  function applyFavicon(prefs, live) {
    if (!prefs.favicon) {
      setFavicon(PLAIN_FAVICON);
      return;
    }
    const top = topState(live);
    const color = top ? STATE_COLORS[top] : null;
    setFavicon(buildFaviconDataURI(color));
  }

  // Pull excerpt text from the most recent .assistant-message in the workspace.
  function latestAssistantExcerpt() {
    const nodes = document.querySelectorAll(".assistant-message");
    if (!nodes || nodes.length === 0) return "";
    const last = nodes[nodes.length - 1];
    const text = (last.textContent || "").trim().replace(/\s+/g, " ");
    if (text.length <= 120) return text;
    return text.slice(0, 117) + "…";
  }

  function fireOsNotification(session) {
    if (!("Notification" in window)) return;
    if (Notification.permission !== "granted") return;
    if (document.hasFocus && document.hasFocus()) return;
    const body = latestAssistantExcerpt();
    let n;
    try {
      n = new Notification("serf · " + (session.title || session.id), { body: body });
    } catch (e) {
      return;
    }
    if (n) {
      n.onclick = function () {
        try { window.focus(); } catch (e) {}
        window.location.href = "/s/" + encodeURIComponent(session.id);
      };
    }
  }

  function playTone() {
    if (document.hasFocus && document.hasFocus()) return;
    const Ctor = window.AudioContext || window.webkitAudioContext;
    if (!Ctor) return;
    let ctx;
    try {
      ctx = new Ctor();
    } catch (e) {
      return;
    }
    try {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain ? ctx.createGain() : null;
      osc.frequency.value = 800;
      if (gain) {
        gain.gain.value = 0.1;
        osc.connect(gain);
        gain.connect(ctx.destination);
      } else {
        osc.connect(ctx.destination);
      }
      osc.start();
      setTimeout(() => {
        try { osc.stop(); } catch (e) {}
        try { ctx.close && ctx.close(); } catch (e) {}
      }, 120);
    } catch (e) {}
  }

  // The two transitions that fire OS + sound notifications.
  function isAlertTransition(from, to) {
    if (from === "idle" && to === "awaiting") return true;
    if (from === "processing" && to === "errored") return true;
    return false;
  }

  function detectTransitions(prefs, live) {
    const seen = new Set();
    for (const s of live || []) {
      seen.add(s.id);
      const before = prevState.get(s.id);
      const after = s.state;
      if (before && before !== after && isAlertTransition(before, after)) {
        if (prefs.os) fireOsNotification(s);
        if (prefs.sound) playTone();
      }
      prevState.set(s.id, after);
    }
    // Forget sessions that are no longer live.
    for (const id of Array.from(prevState.keys())) {
      if (!seen.has(id)) prevState.delete(id);
    }
  }

  function applyAll(prefs, live) {
    applyTitle(prefs, live);
    applyFavicon(prefs, live);
  }

  function poll() {
    const searchPromise = window.SerfAppwire
      ? window.SerfAppwire.search("")
      : fetch("/api/search?q=").then((r) => r.json());
    return searchPromise
      .then((resp) => {
        const live = (resp && resp.live) || [];
        const prefs = readPrefs();
        applyAll(prefs, live);
        detectTransitions(prefs, live);
      })
      .catch(() => {});
  }

  function startPolling() {
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(poll, POLL_MS);
  }

  // Public init: read prefs, apply current state, kick off polling.
  function init() {
    initialized = true;
    const prefs = readPrefs();
    // Reset prev-state map on init so the first poll establishes a baseline.
    prevState.clear();
    const initialSearch = window.SerfAppwire
      ? window.SerfAppwire.search("")
      : fetch("/api/search?q=").then((r) => r.json());
    initialSearch
      .then((resp) => {
        const live = (resp && resp.live) || [];
        applyAll(prefs, live);
        for (const s of live) prevState.set(s.id, s.state);
      })
      .catch(() => {
        applyAll(prefs, []);
      });
    startPolling();
  }

  // Pref-change handler: re-read prefs and re-apply title + favicon now.
  // If the user just turned on OS notifications, request permission.
  function onPrefsChanged() {
    const prefs = readPrefs();
    if (prefs.os && "Notification" in window && Notification.permission === "default") {
      try {
        Notification.requestPermission().then((perm) => {
          if (perm !== "granted") {
            const cur = readPrefs();
            cur.os = false;
            writePrefs(cur);
            const box = document.querySelector('input[type=checkbox][data-notif="os"]');
            if (box) box.checked = false;
          }
        });
      } catch (e) {}
    }
    // Re-apply title/favicon immediately using a fresh poll.
    poll();
  }

  document.addEventListener("serf-hub:notifications-changed", onPrefsChanged);

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }

  // Expose a tiny test/control surface.
  window.serfHubNotifications = {
    init: init,
    poll: poll,
    _readPrefs: readPrefs,
    _prevState: prevState,
  };
})();

// Hub-wide launch-config and credentials notifications.
// Refresh visible panels when their underlying state changes.
(function () {
  "use strict";

  if (!window.SerfAppwire || typeof window.SerfAppwire.onNotification !== "function") {
    return;
  }

  SerfAppwire.onNotification(function (method) {
    if (method === "serf/auth/updated") {
      // Reload credentials panel if it is currently rendered.
      const credsRows = document.getElementById("credentials-rows");
      if (credsRows && window.launchconfig && typeof launchconfig.authList === "function") {
        launchconfig.authList().then(function (list) {
          credsRows.dispatchEvent(new CustomEvent("credentials-reload", { detail: list }));
        });
      }
      // Refresh providers settings tab if it is the active settings pane.
      if (
        window.location.pathname === "/settings/providers" &&
        window.htmx &&
        typeof htmx.ajax === "function"
      ) {
        htmx.ajax("GET", "/_partials/settings/providers", "#settings-content");
      }
    } else if (method === "serf/launch/updated") {
      // Reload whatever settings tab is open.
      const path = window.location.pathname;
      if (path.startsWith("/settings/") && window.htmx && typeof htmx.ajax === "function") {
        htmx.ajax("GET", "/_partials" + path, "#settings-content");
      }
    }
  });
})();
