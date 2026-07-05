// Workspace model chip abbreviation — shorten server-rendered full model IDs
// (e.g. "anthropic/claude-haiku-4-5-20251001") to compact display names
// (e.g. "claude-haiku-4-5") while preserving the full ID in title for tooltip.
// Uses data-full-model to anchor abbreviation to the original server value so
// repeated swaps do not re-abbreviate an already-shortened string.
//
// Not settings-pane logic — it targets [data-model-display] chips in the
// workspace header/composer (workspace.html), and lived in settings.js only
// by historical accident. Moved here so settings.js could be split cleanly
// into per-section files (2026-07 consistency sweep, Track 0).
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
