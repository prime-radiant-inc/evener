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

  window.SerfSettingsDisplay = { readComposerPrefs, writeComposerPrefs, syncToggleState };

  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;
    if (target.matches('input[type=checkbox][data-composer="enterToSend"]')) {
      const prefs = readComposerPrefs();
      prefs.enterToSend = target.checked;
      writeComposerPrefs(prefs);
      syncToggleState(target);
      applyComposerKeybindHints();
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }
    if (target.matches('input[type=checkbox][data-composer="showCost"]')) {
      const prefs = readComposerPrefs();
      prefs.showCost = target.checked;
      writeComposerPrefs(prefs);
      syncToggleState(target);
      document.body.dataset.showCost = target.checked ? "true" : "false";
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }
  });

  // applyDisplayState reflects current composer prefs whenever a settings
  // pane is swapped in. htmx:afterSwap fires for the workspace swap; we
  // detect the panel's controls and set their checked/label state.
  function applyDisplayState() {
    const prefs = readComposerPrefs();
    const enterToSendBox = document.querySelector('input[type=checkbox][data-composer="enterToSend"]');
    if (enterToSendBox) {
      enterToSendBox.checked = prefs.enterToSend;
      syncToggleState(enterToSendBox);
    }
    const showCostBox = document.querySelector('input[type=checkbox][data-composer="showCost"]');
    if (showCostBox) {
      showCostBox.checked = prefs.showCost;
      syncToggleState(showCostBox);
    }
  }

  function applyComposerKeybindHints() {
    const sendKbd = document.querySelector(".send-btn kbd");
    const steerBtn = document.querySelector("[data-steer-trigger]");
    const steerKbd = steerBtn && steerBtn.querySelector("kbd");
    const on = readComposerPrefs().enterToSend;
    if (sendKbd) sendKbd.textContent = on ? "↵" : "⌘↵";
    if (steerKbd) steerKbd.textContent = on ? "" : "⇧↵";
  }

  document.addEventListener("DOMContentLoaded", applyDisplayState);
  document.body.addEventListener("htmx:afterSwap", applyDisplayState);
  document.addEventListener("DOMContentLoaded", applyComposerKeybindHints);
  document.body.addEventListener("htmx:afterSwap", applyComposerKeybindHints);

  // Show-cost applies to <body> on every page load so the CSS gate
  // (body[data-show-cost="false"]) is correct before any settings pane opens.
  (function () {
    document.body.dataset.showCost = readComposerPrefs().showCost ? "true" : "false";
  })();
})();
