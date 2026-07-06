// Settings page interactivity — Display section: Enter-to-send and Show-cost
// toggles. Uses event delegation on document.body so it works even when the
// settings partial is htmx-swapped in (inline scripts in swapped content
// don't reliably execute across all htmx versions). Mirrors the
// settings-appearance.js / settings-notifications.js shape (2026-07
// consistency sweep, Track C).
(function () {
  "use strict";

  function readComposerPrefs() {
    let parsed = {};
    try { parsed = JSON.parse(localStorage.getItem("serf-hub.composer") || "{}") || {}; }
    catch (e) { parsed = {}; }
    // showCost defaults ON; enterToSend defaults OFF.
    return {
      enterToSend: parsed.enterToSend === true,
      showCost: parsed.showCost !== false,
    };
  }
  function writeComposerPrefs(prefs) {
    localStorage.setItem("serf-hub.composer", JSON.stringify(prefs));
  }
  function syncToggleState(input) {
    const span = input.parentElement.querySelector(".state");
    if (span) span.textContent = input.checked ? "ON" : "OFF";
  }

  // W2/W3 add: the change-listener branches for data-composer="enterToSend"
  // and data-composer="showCost", the applyDisplayState() restore function,
  // and the composer keybind-hint sync. Expose the pref helpers so those
  // additions and the page-load IIFEs below share one source of truth.
  window.SerfSettingsDisplay = { readComposerPrefs, writeComposerPrefs, syncToggleState };

  // Show-cost applies to <body> on every page load so the CSS gate
  // (body[data-show-cost="false"]) is correct before any settings pane opens.
  (function () {
    document.body.dataset.showCost = readComposerPrefs().showCost ? "true" : "false";
  })();
})();
