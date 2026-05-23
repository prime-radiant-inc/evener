// Settings page interactivity. Uses event delegation on document.body so it
// works even when the settings partial is htmx-swapped in (inline scripts in
// swapped content don't reliably execute across all htmx versions).
(function () {
  "use strict";

  // Theme picker — radio inputs with name="theme" inside .settings-form.
  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;

    if (target.matches('input[name="theme"]')) {
      const v = target.value;
      window.serfHub.setTheme(v === "system" ? null : v);
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

    if (target.matches("input[type=checkbox][data-notif]")) {
      const key = target.dataset.notif;
      const prefs = readNotifPrefs();
      prefs[key] = target.checked;
      writeNotifPrefs(prefs);
      if (key === "os" && target.checked && "Notification" in window) {
        Notification.requestPermission().catch(() => {});
      }
      document.dispatchEvent(new CustomEvent("serf-hub:notifications-changed", {
        detail: { key, value: target.checked },
      }));
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
      notifBoxes.forEach((b) => { b.checked = !!prefs[b.dataset.notif]; });
    }
  }

  function readNotifPrefs() {
    try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
    catch (e) { return {}; }
  }
  function writeNotifPrefs(prefs) {
    localStorage.setItem("serf-hub.notifications", JSON.stringify(prefs));
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
