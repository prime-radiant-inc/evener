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
