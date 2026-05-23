// Sidebar project collapse/expand. Projects default collapsed; explicit
// expansions persist under localStorage["serf-hub.sidebar.expanded.<key>"].
// The chevron glyph is the only click target on the header — count and
// rollup dot remain passive so they don't accidentally toggle when users
// glance at the row.
(function () {
  "use strict";

  var STORAGE_PREFIX = "serf-hub.sidebar.expanded.";

  function isCollapsed(key) {
    try {
      return window.localStorage.getItem(STORAGE_PREFIX + key) !== "true";
    } catch (e) {
      return true;
    }
  }

  function setCollapsed(key, collapsed) {
    try {
      if (collapsed) {
        window.localStorage.removeItem(STORAGE_PREFIX + key);
      } else {
        window.localStorage.setItem(STORAGE_PREFIX + key, "true");
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
      chevron.setAttribute("aria-expanded", collapsed ? "false" : "true");
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
    chevron.setAttribute("aria-expanded", nextCollapsed ? "false" : "true");
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

  // Mobile hamburger: toggle a body[data-sidebar-open] flag that the
  // mobile media query reads to slide the sidebar in. Tapping a sidebar
  // link also closes (via htmx:beforeRequest) so navigating to a session
  // doesn't leave the drawer hanging. The close-on-outside handler is
  // only armed while open — keeping it always-on caused double-fires
  // under mobile emulation where the synthetic click stack toggled
  // open then immediately closed.
  var sidebarTrapHandle = null;

  function setSidebarOpen(open) {
    if (open) {
      document.body.setAttribute("data-sidebar-open", "");
      document.addEventListener("click", onOutsideClick, true);
      // Only trap focus on phone — desktop sidebar isn't a drawer. Match
      // the design-language breakpoint.
      var isPhone = window.matchMedia && window.matchMedia("(max-width: 767px)").matches;
      if (isPhone && window.SerfFocusTrap) {
        var sidebar = document.getElementById("sidebar");
        var trigger = document.querySelector("[data-sidebar-toggle]");
        if (sidebar) {
          sidebarTrapHandle = window.SerfFocusTrap.activate(sidebar, trigger);
        }
      }
    } else {
      document.body.removeAttribute("data-sidebar-open");
      document.removeEventListener("click", onOutsideClick, true);
      if (sidebarTrapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(sidebarTrapHandle);
        sidebarTrapHandle = null;
      }
    }
  }

  function isSidebarOpen() {
    return document.body.hasAttribute("data-sidebar-open");
  }

  function onOutsideClick(e) {
    var t = e.target;
    if (!t) return;
    if (t.closest && (t.closest("#sidebar") || t.closest("[data-sidebar-toggle]"))) {
      return;
    }
    setSidebarOpen(false);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t) return;
    var trigger = t.closest && t.closest("[data-sidebar-toggle]");
    if (trigger) {
      e.preventDefault();
      e.stopPropagation();
      setSidebarOpen(!isSidebarOpen());
    }
  });

  document.addEventListener("htmx:beforeRequest", function (e) {
    var trigger = e.detail && e.detail.elt;
    if (trigger && trigger.closest && trigger.closest("#sidebar")) {
      setSidebarOpen(false);
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && isSidebarOpen()) {
      setSidebarOpen(false);
    }
  });

  // Sidebar rail mode — persisted to localStorage. The body[data-sidebar-rail]
  // attribute is the single source of truth that CSS reads; the helper
  // syncs that attribute to storage and back.
  var RAIL_KEY = "serf-hub.sidebar.rail";

  function isRailEnabled() {
    try {
      return window.localStorage.getItem(RAIL_KEY) === "true";
    } catch (e) {
      return false;
    }
  }

  function setRail(enabled) {
    if (enabled) {
      document.body.setAttribute("data-sidebar-rail", "");
    } else {
      document.body.removeAttribute("data-sidebar-rail");
    }
    try {
      if (enabled) {
        window.localStorage.setItem(RAIL_KEY, "true");
      } else {
        window.localStorage.removeItem(RAIL_KEY);
      }
    } catch (e) {
      // localStorage may be disabled; flip still works for this session.
    }
    syncRailToggleLabel();
  }

  function toggleRail() {
    setRail(!document.body.hasAttribute("data-sidebar-rail"));
  }

  // Apply persisted rail state ASAP — before first paint when possible.
  if (isRailEnabled()) {
    setRail(true);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var btn = t.closest("[data-sidebar-rail-toggle]");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    toggleRail();
  });

  // ⌘B / Ctrl+B — toggle rail mode. Skip when the focus is on an editable
  // surface (textarea, contenteditable, input) so the shortcut doesn't fire
  // while the user is typing browser-native chords. Mobile (no
  // matchMedia "(min-width: 768px)") ignores the shortcut because rail
  // mode is a desktop affordance.
  function isEditableTarget(el) {
    if (!el) return false;
    var tag = (el.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || tag === "select") return true;
    if (el.isContentEditable) return true;
    return false;
  }

  document.addEventListener("keydown", function (e) {
    if (e.key !== "b" && e.key !== "B") return;
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.altKey || e.shiftKey) return;
    if (isEditableTarget(e.target)) return;
    // Desktop only — match the design-language breakpoint.
    if (window.matchMedia && window.matchMedia("(max-width: 767px)").matches) return;
    e.preventDefault();
    toggleRail();
  });

  // Sync the rail-toggle button's aria-label so screen readers hear the
  // correct direction after each flip. Runs on init + after each htmx
  // swap (the sidebar partial re-renders frequently).
  function syncRailToggleLabel() {
    var btn = document.querySelector("[data-sidebar-rail-toggle]");
    if (!btn) return;
    var railed = document.body.hasAttribute("data-sidebar-rail");
    btn.setAttribute("aria-label", railed ? "expand sidebar" : "collapse sidebar");
    btn.setAttribute("title", railed ? "expand sidebar (⌘B)" : "collapse sidebar (⌘B)");
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", syncRailToggleLabel);
  } else {
    syncRailToggleLabel();
  }
  document.addEventListener("htmx:afterSwap", syncRailToggleLabel);

  // data-active marker — sync after every htmx swap into #workspace. The
  // marker is the row whose href matches the current pathname exactly (e.g.,
  // "/s/01ABC"). /new and /settings URLs don't match any sb-row, so all
  // rows clear.
  // Match by exact pathname: rows have href="/s/<id>" and window.location.pathname
  // is "/s/<id>" (with any trailing slash stripped below).
  function syncActiveRow() {
    var path = window.location.pathname || "";
    var rows = document.querySelectorAll(".sb-row");
    var matched = null;
    if (path && path.indexOf("/s/") === 0) {
      // Strip trailing slash for exact match.
      var clean = path.replace(/\/+$/, "");
      for (var i = 0; i < rows.length; i++) {
        var href = rows[i].getAttribute("href") || "";
        if (href === clean) { matched = rows[i]; break; }
      }
    }
    for (var j = 0; j < rows.length; j++) {
      if (rows[j] === matched) {
        rows[j].setAttribute("data-active", "");
      } else {
        rows[j].removeAttribute("data-active");
      }
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", syncActiveRow);
  } else {
    syncActiveRow();
  }
  document.addEventListener("htmx:afterSwap", function (e) {
    // Only resync when the swap was into #workspace OR the sidebar itself.
    // (Sidebar swaps re-render the rows; the marker has to be re-applied.)
    var target = e && e.target;
    if (!target) { syncActiveRow(); return; }
    if (target.id === "workspace" || target.id === "sidebar" || (target.closest && target.closest("#sidebar"))) {
      syncActiveRow();
    }
    // After a sidebar swap, update [data-pulse] on any status dots.
    if (target.id === "sidebar" || (target.closest && target.closest("#sidebar"))) {
      if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
        window.SerfRenderer.applyStatusDotPulse(document.getElementById("sidebar") || document);
      }
    }
  });
})();
