// Settings page interactivity. Uses event delegation on document.body so it
// works even when the settings partial is htmx-swapped in (inline scripts in
// swapped content don't reliably execute across all htmx versions).
(function () {
  "use strict";

  const transcriptStatusPrefsKey = "serf-hub.transcript.systemStatus";

  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;

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

  });

  // Reflect current notification prefs whenever a settings pane is swapped in.
  // htmx:afterSwap fires for the workspace swap; we detect the panel-specific
  // inputs and check the right boxes.
  function applySettingsState() {
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
