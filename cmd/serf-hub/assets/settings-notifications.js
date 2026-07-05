// Settings page interactivity — notification toggles (title bar count,
// favicon dot, OS notification, sound). Uses event delegation on
// document.body so it works even when the settings partial is
// htmx-swapped in (inline scripts in swapped content don't reliably
// execute across all htmx versions).
//
// Not to be confused with assets/notifications.js, which drives the
// runtime title/favicon/OS/sound alerting itself and reads the same
// localStorage prefs this file writes.
(function () {
  "use strict";

  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;

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

  // Reflect current notification prefs whenever a settings pane is swapped
  // in. htmx:afterSwap fires for the workspace swap; we detect the
  // panel's checkboxes and check the right boxes.
  function applyNotifState() {
    const notifBoxes = document.querySelectorAll("input[type=checkbox][data-notif]");
    if (notifBoxes.length) {
      const prefs = readNotifPrefs();
      notifBoxes.forEach((b) => { b.checked = !!prefs[b.dataset.notif]; syncToggleState(b); });
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

  document.addEventListener("DOMContentLoaded", applyNotifState);
  document.body.addEventListener("htmx:afterSwap", applyNotifState);
})();
