// Settings page interactivity — transcript system-status toggles. Uses
// event delegation on document.body so it works even when the settings
// partial is htmx-swapped in (inline scripts in swapped content don't
// reliably execute across all htmx versions).
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

  // Reflect current transcript-status prefs whenever a settings pane is
  // swapped in. htmx:afterSwap fires for the workspace swap; we detect the
  // panel's checkboxes and check the right boxes.
  function applyTranscriptState() {
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

  document.addEventListener("DOMContentLoaded", applyTranscriptState);
  document.body.addEventListener("htmx:afterSwap", applyTranscriptState);
})();
