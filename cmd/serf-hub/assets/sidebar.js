// Sidebar project collapse/expand. Per-project state is persisted under
// localStorage["serf-hub.sidebar.collapsed.<key>"] = "true" | (absent).
// The chevron glyph is the only click target on the header — count and
// rollup dot remain passive so they don't accidentally toggle when users
// glance at the row.
(function () {
  "use strict";

  var STORAGE_PREFIX = "serf-hub.sidebar.collapsed.";

  function isCollapsed(key) {
    try {
      return window.localStorage.getItem(STORAGE_PREFIX + key) === "true";
    } catch (e) {
      return false;
    }
  }

  function setCollapsed(key, collapsed) {
    try {
      if (collapsed) {
        window.localStorage.setItem(STORAGE_PREFIX + key, "true");
      } else {
        window.localStorage.removeItem(STORAGE_PREFIX + key);
      }
    } catch (e) {
      // localStorage may be disabled; collapse still works for this session.
    }
  }

  function applyCollapseState(section) {
    var key = section.getAttribute("data-project-key");
    if (!key) return;
    var collapsed = isCollapsed(key);
    section.classList.toggle("collapsed", collapsed);
    var chevron = section.querySelector(".project-chevron");
    if (chevron) {
      chevron.textContent = collapsed ? "▸" : "▾";
    }
  }

  function applyAll(root) {
    var sections = (root || document).querySelectorAll("[data-project-key]");
    for (var i = 0; i < sections.length; i++) {
      applyCollapseState(sections[i]);
    }
  }

  function onChevronClick(e) {
    var chevron = e.target.closest(".project-chevron");
    if (!chevron) return;
    var section = chevron.closest("[data-project-key]");
    if (!section) return;
    e.preventDefault();
    e.stopPropagation();
    var key = section.getAttribute("data-project-key");
    var nextCollapsed = !section.classList.contains("collapsed");
    section.classList.toggle("collapsed", nextCollapsed);
    chevron.textContent = nextCollapsed ? "▸" : "▾";
    setCollapsed(key, nextCollapsed);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { applyAll(document); });
  } else {
    applyAll(document);
  }

  document.addEventListener("htmx:afterSwap", function (e) {
    applyAll(e.target || document);
  });

  document.addEventListener("click", onChevronClick);
})();
