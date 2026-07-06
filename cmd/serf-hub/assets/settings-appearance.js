// Settings page interactivity — appearance: theme, phone density, and
// sidebar mode. Uses event delegation on document.body so it works even
// when the settings partial is htmx-swapped in (inline scripts in swapped
// content don't reliably execute across all htmx versions).
(function () {
  "use strict";

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

    if (target.matches('input[name="font-size"]')) {
      const v = target.value;
      localStorage.setItem("serf-hub.appearance.fontSize", v);
      document.body.dataset.fontSize = v;
      return;
    }
  });

  // Reflect current theme/density/sidebar-mode prefs whenever a settings
  // pane is swapped in. htmx:afterSwap fires for the workspace swap; we
  // detect the panel's radios and check the right one.
  function applyAppearanceState() {
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
    const fontSizeRadios = document.querySelectorAll('input[name="font-size"]');
    if (fontSizeRadios.length) {
      const stored = localStorage.getItem("serf-hub.appearance.fontSize") || "m";
      fontSizeRadios.forEach((r) => { r.checked = r.value === stored; });
    }
  }

  document.addEventListener("DOMContentLoaded", applyAppearanceState);
  document.body.addEventListener("htmx:afterSwap", applyAppearanceState);
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

// Font size — apply stored value to body on every page load.
(function () {
  const KEY = "serf-hub.appearance.fontSize";
  document.body.dataset.fontSize = localStorage.getItem(KEY) || "m";
})();
