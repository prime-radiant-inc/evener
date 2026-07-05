// Browser notifications for serf-hub. Reads opt-in prefs from
// localStorage["serf-hub.notifications"] and drives four channels:
//   - title-bar count of sessions needing attention (needs_you + error)
//   - favicon dot for the highest-priority attention level
//   - OS notification on transitions into needs_you/error
//   - short tone on the same transitions
//
// Event-driven (spec v5): the hub broadcasts "serf/attention/changed" over
// AppWire whenever a live session's attention level transitions, carrying
// both the changed entries and the authoritative summary counts. A baseline
// is fetched from /api/tree (attentionSummary) on init and on reconnect;
// no OS/sound fires until that baseline lands, so reloading the hub never
// re-alerts on attention that was already true before the page opened.
// Only the Web Locks leader tab (or every tab, if that API is unavailable)
// fires OS/sound so a multi-tab session doesn't double-alert.
//
// Settings pane dispatches the "serf-hub:notifications-changed" event when
// the user toggles a preference; this module re-reads prefs (and
// re-applies title/favicon) without a reload. Prefs are versioned
// (serf-hub.notifications.v) so the title/favicon-on-by-default migration
// (v2) runs once and never overrides a preference the user already set.
(function () {
  "use strict";

  const PREFS_KEY = "serf-hub.notifications";
  const PREFS_VERSION_KEY = "serf-hub.notifications.v";
  const DEFAULT_PREFS = { title: true, favicon: true, os: false, sound: false };
  const PLAIN_FAVICON =
    "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237aa2f7'/></svg>";

  // Dot color by attention level. No "idle" entry: idle never sets a dot.
  const STATE_COLORS = {
    error: "#f7768e",
    needs_you: "#e0af68",
    working: "#7aa2f7",
  };

  // The authoritative badge summary, or null until the /api/tree baseline
  // fetch resolves. No edge-firing (OS/sound) before it (spec v5): without
  // it, a session already needs_you at page-load would look like a fresh
  // transition on the first "serf/attention/changed" broadcast.
  let summary = null;
  let initialized = false;
  let leader = false;

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

  // Versioned migration to the v2 defaults (title+favicon ON, os+sound
  // opt-in). A fresh install gets the new defaults outright. An existing
  // blob had every never-touched key implicitly OFF under the old
  // all-off defaults; backfill that explicitly so the new ON defaults
  // don't silently flip behavior the user never chose (round-4 A4).
  function migratePrefs() {
    if (localStorage.getItem(PREFS_VERSION_KEY) === "2") return;
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) {
      writePrefs(Object.assign({}, DEFAULT_PREFS));
    } else {
      let cur = {};
      try { cur = JSON.parse(raw) || {}; } catch (e) { cur = {}; }
      for (const k of ["title", "favicon", "os", "sound"]) {
        if (typeof cur[k] !== "boolean") cur[k] = false;
      }
      writePrefs(cur);
    }
    localStorage.setItem(PREFS_VERSION_KEY, "2");
  }

  // Shared section-label map. Exposed as window.SerfSectionLabels so
  // renderer.js (which loads later) can read the same source rather
  // than maintaining a parallel copy.
  const SECTION_LABELS = {
    "general": "general", "theme": "theme", "notifications": "notifications",
    "providers": "providers", "agents": "agents",
    "launch-serf": "serf launch", "launch-codex": "codex launch",
    "inrepo": "in-repo config",
    "plugins": "plugins", "skills": "skills", "mcp": "mcp servers",
    "hub": "hub", "storage": "storage", "project": "project",
  };
  window.SerfSectionLabels = SECTION_LABELS;

  function activeSection() {
    // Within settings, prefer the URL after htmx pushes new section URL.
    if (document.querySelector(".workspace-header .workspace-title .title[data-settings-section]")) {
      const urlMatch = location.pathname.match(/^\/settings\/([^/?]+)/);
      if (urlMatch) return SECTION_LABELS[urlMatch[1]] || urlMatch[1];
    }
    const header = document.querySelector(".workspace-header .workspace-title .title");
    if (header && header.textContent) return header.textContent.trim();
    return "";
  }

  function applyTitle(prefs, summary) {
    const section = activeSection();
    const base = section ? section + " \xb7 serf hub" : "serf hub";
    if (!prefs.title) {
      document.title = base;
      return;
    }
    const count = summary ? summary.needsYou + summary.error : 0;
    document.title = count > 0 ? "(" + count + ") " + base : base;
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

  function applyFavicon(prefs, summary) {
    if (!prefs.favicon) {
      setFavicon(PLAIN_FAVICON);
      return;
    }
    const topLevel = summary
      ? (summary.error > 0 ? "error" : summary.needsYou > 0 ? "needs_you" : summary.working > 0 ? "working" : null)
      : null;
    const color = topLevel ? STATE_COLORS[topLevel] : null;
    setFavicon(buildFaviconDataURI(color));
  }

  // Apply both title and favicon from the current prefs + summary.
  function applyCounts() {
    const prefs = readPrefs();
    applyTitle(prefs, summary);
    applyFavicon(prefs, summary);
  }

  function fireOsNotification(entry) {
    if (!("Notification" in window)) return;
    if (Notification.permission !== "granted") return;
    if (document.hasFocus && document.hasFocus()) return;
    let n;
    try {
      n = new Notification("serf · " + (entry.title || entry.threadId));
    } catch (e) {
      return;
    }
    if (n) {
      n.onclick = function () {
        try { window.focus(); } catch (e) {}
        window.location.href = "/s/" + encodeURIComponent(entry.threadId);
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

  // Baseline fetch: the authoritative /api/tree summary. Called on init and
  // on AppWire reconnect (a dropped connection can miss broadcasts, so the
  // reconnect handler re-syncs rather than trusting the gap stayed empty).
  function fetchBaseline() {
    return fetch("/api/tree").then((r) => r.json()).then((resp) => {
      summary = (resp && resp.attentionSummary) || { needsYou: 0, error: 0, working: 0 };
      applyCounts();
    }).catch(() => {});
  }

  // Elect one tab to fire OS/sound. Uses a held Web Lock as the election:
  // the first tab to acquire it holds it for its lifetime (the callback
  // never resolves its promise), so every later tab's ifAvailable request
  // fails immediately and knows it isn't the leader. Environments without
  // the Web Locks API (or that reject the request) fire from every tab —
  // a duplicate alert is a smaller problem than a silent one.
  function isLeaderTab(cb) {
    if (navigator.locks && navigator.locks.request) {
      navigator.locks.request("serf-hub-os-leader", { ifAvailable: true }, (lock) => {
        if (lock) { cb(true); return new Promise(() => {}); }
        cb(false);
        return Promise.resolve();
      }).catch(() => cb(true));
      return;
    }
    cb(true);
  }
  isLeaderTab((v) => { leader = v; });

  // Handles "serf/attention/changed": update the summary-driven counts
  // unconditionally, then fire OS/sound only for entries that just
  // transitioned into needs_you/error, only once a baseline exists, only
  // when this tab is unfocused, and only on the leader tab.
  function onAttentionChanged(params) {
    const prefs = readPrefs();
    const hadBaseline = summary !== null;
    if (params && params.summary) summary = params.summary;
    applyCounts();
    if (!hadBaseline) return;
    if (document.hasFocus && document.hasFocus()) return;
    if (!leader) return;
    for (const ch of (params && params.changed) || []) {
      const into = ch.level === "needs_you" || ch.level === "error";
      const was = ch.prevLevel === "needs_you" || ch.prevLevel === "error";
      if (into && !was) {
        if (prefs.os) fireOsNotification(ch);
        if (prefs.sound) playTone();
      }
    }
  }

  // Public init: migrate prefs, fetch the baseline, and subscribe to
  // attention broadcasts plus reconnect/own-thread reconcile triggers.
  function init() {
    initialized = true;
    migratePrefs();
    fetchBaseline();
    if (window.SerfAppwire && typeof window.SerfAppwire.onNotification === "function") {
      window.SerfAppwire.onNotification(function (method, params) {
        if (method === "serf/attention/changed") onAttentionChanged(params);
      });
    }
    if (window.SerfAppwire && typeof window.SerfAppwire.onConnectionRestored === "function") {
      window.SerfAppwire.onConnectionRestored(fetchBaseline);
    }
    // Own-thread instant reconcile: renderer.js dispatches this when a live
    // THREAD_STATUS_CHANGED frame updates the open thread's status pill, so
    // the badge doesn't wait for the next hub-side attention tick.
    document.addEventListener("serf-hub:thread-status", fetchBaseline);
  }

  // Pref-change handler: re-read prefs and re-apply title + favicon now.
  // If the user just turned on OS notifications, request permission. This
  // is defensive: settings.js now also gates the toggle commit on the
  // permission request, so by the time this handler fires permission is
  // typically already granted (and the guard below short-circuits). The
  // remaining edge cases (something else dispatches the event without
  // going through settings.js) still need the revert path here.
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
            if (box) {
              box.checked = false;
              // Keep the visible ON/OFF label in step with the checkbox;
              // otherwise the box looks unchecked while the label still
              // claims ON for a setting the browser just refused.
              const span = box.parentElement && box.parentElement.querySelector(".state");
              if (span) span.textContent = "OFF";
            }
          }
        });
      } catch (e) {}
    }
    // Re-apply title/favicon immediately using the current summary.
    applyCounts();
  }

  document.addEventListener("serf-hub:notifications-changed", onPrefsChanged);

  // Re-apply title/favicon immediately after HTMX swaps the workspace
  // (e.g., navigating between settings sections). Otherwise the title
  // stays stale until the next attention broadcast. Also sync the settings
  // header's visible .title span to the new section so the workspace
  // header doesn't read the previous tab name.
  document.body.addEventListener("htmx:afterSettle", () => {
    if (!initialized) return;
    syncSettingsHeader();
    applyCounts();
  });

  function syncSettingsHeader() {
    const headerTitle = document.querySelector(".workspace-header .workspace-title .title[data-settings-section]");
    if (!headerTitle) return;
    const urlMatch = location.pathname.match(/^\/settings\/([^/?]+)/);
    if (!urlMatch) return;
    const section = urlMatch[1];
    if (headerTitle.dataset.settingsSection === section) return;
    headerTitle.dataset.settingsSection = section;
    headerTitle.textContent = SECTION_LABELS[section] || section;
    // Also fix the active link highlight so the nav reflects the URL.
    document.querySelectorAll(".settings-nav-link").forEach((a) => {
      const href = a.getAttribute("href") || "";
      a.classList.toggle("active", href === "/settings/" + section);
    });
  }

  // Init runs immediately rather than waiting for DOMContentLoaded: nothing
  // here touches DOM content synchronously (activeSection/setFavicon run
  // later, inside fetchBaseline's async callback, by which point the page
  // has parsed regardless) and subscribing to AppWire notifications early
  // means no attention broadcast during page load is missed.
  init();

  // Expose a tiny test/control surface.
  window.serfHubNotifications = {
    init: init,
    _readPrefs: readPrefs,
    _onAttentionChanged: onAttentionChanged,
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
      // Reload instances panel if it is currently rendered.
      const instancesRoot = document.getElementById("instances-root");
      if (instancesRoot && instancesRoot.dataset.loaded === "true" && window.launchconfig && typeof launchconfig.instanceList === "function") {
        launchconfig.instanceList().then(function (data) {
          instancesRoot.dispatchEvent(new CustomEvent("credentials-reload", { bubbles: true, detail: data }));
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
      // Reload whatever settings tab is open, preserving query params (e.g. ?cwd= on project page).
      const path = window.location.pathname;
      if (path.startsWith("/settings/") && window.htmx && typeof htmx.ajax === "function") {
        htmx.ajax("GET", "/_partials" + path + window.location.search, "#settings-content");
      }
    } else if (method === "serf/marketplace/updated" || method === "serf/plugin/updated") {
      // Refresh the plugins-manager pane if it is the active settings pane, so
      // a mutation from another tab (or another client entirely) doesn't
      // leave this tab's marketplace/installed lists stale. That staleness is
      // not just cosmetic: Browse's "Install" button is only hidden for
      // already-installed plugins when the local installed list is current,
      // and Manager.Install unconditionally resets AutoUpgrade to false on
      // reinstall, so a stale tab could silently clobber another tab's
      // auto-upgrade setting.
      if (
        window.location.pathname === "/settings/plugins-manager" &&
        window.htmx &&
        typeof htmx.ajax === "function"
      ) {
        htmx.ajax("GET", "/_partials/settings/plugins-manager", "#settings-content");
      }
    }
  });
})();
