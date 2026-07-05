// Settings page interactivity. Uses event delegation on document.body so it
// works even when the settings partial is htmx-swapped in (inline scripts in
// swapped content don't reliably execute across all htmx versions).
(function () {
  "use strict";

  const transcriptStatusPrefsKey = "serf-hub.transcript.systemStatus";

  // Theme picker — radio inputs with name="theme" inside .settings-form.
  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;

    if (target.matches('input[name="theme"]')) {
      const v = target.value;
      window.serfHub.setTheme(v === "system" ? null : v);
      if (window.SerfToast) window.SerfToast.show("Theme: " + v, "success");
      return;
    }

    if (target.matches('input[name="phone-density"]')) {
      const v = target.value;
      localStorage.setItem("serf-hub.phone-density", v);
      document.body.dataset.phoneDensity = v;
      return;
    }

    if (target.matches('input[name="sidebar-mode"]')) {
      const rail = target.value === "rail";
      localStorage.setItem("serf-hub.sidebar.rail", String(rail));
      if (rail) document.body.dataset.sidebarRail = "";
      else delete document.body.dataset.sidebarRail;
      return;
    }

    if (target.matches("input[type=checkbox][data-transcript-status]")) {
      const key = target.dataset.transcriptStatus;
      const cur = readTranscriptStatusPrefs();
      cur[key] = target.checked;
      writeTranscriptStatusPrefs(cur);
      syncToggleState(target);
      document.dispatchEvent(new CustomEvent("serf-hub:transcript-system-status-changed", {
        detail: { key, value: target.checked },
      }));
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }

    if (target.matches("input[type=checkbox][data-notif]")) {
      const key = target.dataset.notif;
      const desired = target.checked;

      // commit is the "yes the toggle stuck" finisher: persist prefs,
      // update the visible ON/OFF label, fire the change event, and toast.
      // It is split out so the OS-notification branch can defer it until
      // the browser permission prompt resolves (we don't want a success
      // toast or ON label for a setting the browser is about to deny).
      const commit = () => {
        const cur = readNotifPrefs();
        cur[key] = desired;
        writeNotifPrefs(cur);
        syncToggleState(target);
        document.dispatchEvent(new CustomEvent("serf-hub:notifications-changed", {
          detail: { key, value: desired },
        }));
        if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      };

      // revertToOff undoes a not-yet-committed OS toggle when the browser
      // denies the permission request. We use the same syncToggleState
      // path so the label stays in sync with the checkbox — the previous
      // code path left an "ON" label next to an unchecked box.
      const revertToOff = (reason) => {
        target.checked = false;
        const cur = readNotifPrefs();
        cur[key] = false;
        writeNotifPrefs(cur);
        syncToggleState(target);
        if (reason && window.SerfToast) window.SerfToast.show(reason, "warning");
      };

      if (key === "os" && desired && "Notification" in window && Notification.permission === "default") {
        Notification.requestPermission()
          .then((perm) => {
            if (perm === "granted") commit();
            else revertToOff("Browser denied notification permission");
          })
          .catch(() => revertToOff(""));
        return;
      }
      commit();
      return;
    }
  });

  // Reflect current theme + notification prefs whenever a settings pane is
  // swapped in. htmx:afterSwap fires for the workspace swap; we detect the
  // panel-specific inputs and check the right boxes.
  function applySettingsState() {
    const themeRadios = document.querySelectorAll('input[name="theme"]');
    if (themeRadios.length) {
      const current = localStorage.getItem("serf-hub.theme") || "system";
      themeRadios.forEach((r) => { r.checked = r.value === current; });
    }
    const phoneDensityRadios = document.querySelectorAll('input[name="phone-density"]');
    if (phoneDensityRadios.length) {
      const stored = localStorage.getItem("serf-hub.phone-density") || "compact";
      phoneDensityRadios.forEach((r) => { r.checked = r.value === stored; });
    }
    const sidebarModeRadios = document.querySelectorAll('input[name="sidebar-mode"]');
    if (sidebarModeRadios.length) {
      const stored = localStorage.getItem("serf-hub.sidebar.rail") === "true" ? "rail" : "pane";
      sidebarModeRadios.forEach((r) => { r.checked = r.value === stored; });
    }
    const notifBoxes = document.querySelectorAll("input[type=checkbox][data-notif]");
    if (notifBoxes.length) {
      const prefs = readNotifPrefs();
      notifBoxes.forEach((b) => { b.checked = !!prefs[b.dataset.notif]; syncToggleState(b); });
    }
    const transcriptBoxes = document.querySelectorAll("input[type=checkbox][data-transcript-status]");
    if (transcriptBoxes.length) {
      const prefs = readTranscriptStatusPrefs();
      transcriptBoxes.forEach((b) => { b.checked = prefs[b.dataset.transcriptStatus] === true; syncToggleState(b); });
    }
  }

  function syncToggleState(input) {
    const span = input.parentElement.querySelector(".state");
    if (span) span.textContent = input.checked ? "ON" : "OFF";
  }

  function readNotifPrefs() {
    try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
    catch (e) { return {}; }
  }
  function writeNotifPrefs(prefs) {
    localStorage.setItem("serf-hub.notifications", JSON.stringify(prefs));
  }
  function readTranscriptStatusPrefs() {
    try { return JSON.parse(localStorage.getItem(transcriptStatusPrefsKey) || "{}"); }
    catch (e) { return {}; }
  }
  function writeTranscriptStatusPrefs(prefs) {
    localStorage.setItem(transcriptStatusPrefsKey, JSON.stringify(prefs));
  }

  document.addEventListener("DOMContentLoaded", applySettingsState);
  document.body && document.body.addEventListener("htmx:afterSwap", applySettingsState);
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", applySettingsState);
  });
})();

// Phone density — apply stored value to body on every page load.
(function () {
  const KEY = "serf-hub.phone-density";
  const stored = localStorage.getItem(KEY) || "compact";
  document.body.dataset.phoneDensity = stored;
})();

// Sidebar mode (pane / rail) — apply stored value to body on every page load.
(function () {
  const KEY = "serf-hub.sidebar.rail";
  const rail = localStorage.getItem(KEY) === "true";
  if (rail) document.body.dataset.sidebarRail = "";
  else delete document.body.dataset.sidebarRail;
})();

// Workspace model chip abbreviation — shorten server-rendered full model IDs
// (e.g. "anthropic/claude-haiku-4-5-20251001") to compact display names
// (e.g. "claude-haiku-4-5") while preserving the full ID in title for tooltip.
// Uses data-full-model to anchor abbreviation to the original server value so
// repeated swaps do not re-abbreviate an already-shortened string.
(function () {
  var modelAbbrevHandlerInstalled = false;

  function applyModelAbbreviations() {
    if (!window.SerfSpawn || !window.SerfSpawn.abbreviateModel) return;
    document.querySelectorAll("[data-model-display]").forEach(function (el) {
      // Populate the stable anchor attribute from the server-rendered value on
      // first encounter, before any abbreviation has been applied.
      if (!el.dataset.fullModel) {
        el.dataset.fullModel = el.textContent || "";
      }
      var full = el.dataset.fullModel;
      var abbr = window.SerfSpawn.abbreviateModel(full);
      if (abbr !== (el.textContent || "")) el.textContent = abbr;
    });
  }

  function installModelAbbrevHandler() {
    if (modelAbbrevHandlerInstalled) return;
    modelAbbrevHandlerInstalled = true;
    document.body.addEventListener("htmx:afterSwap", applyModelAbbreviations);
  }

  document.addEventListener("DOMContentLoaded", function () {
    applyModelAbbreviations();
    installModelAbbrevHandler();
  });
  // Guard for scripts that run after DOMContentLoaded has already fired.
  if (document.readyState !== "loading") {
    applyModelAbbreviations();
    installModelAbbrevHandler();
  }
})();
