// Sidebar project collapse/expand. Each project's collapsed state persists
// under localStorage["serf-hub.sidebar.expanded.<key>"] as an explicit
// "true"/"false". With no stored value a project falls back to its tier
// default: active-tier projects (data-default-expanded) start expanded, all
// others start collapsed — so live work is visible without burying the user
// under every past project. An explicit user toggle always wins over the
// default. The chevron glyph is the only click target on the header — count,
// age, and rollup dot remain passive so they don't accidentally toggle when
// users glance at the row.
(function () {
  "use strict";

  var STORAGE_PREFIX = "serf-hub.sidebar.expanded.";

  // defaultExpanded reads the tier hint baked into the section by the server.
  function defaultExpanded(section) {
    return section.getAttribute("data-default-expanded") === "true";
  }

  // isCollapsed resolves a project's collapsed state: an explicit stored
  // value wins; otherwise the tier default decides.
  function isCollapsed(key, section) {
    var stored = null;
    try {
      stored = window.localStorage.getItem(STORAGE_PREFIX + key);
    } catch (e) {
      stored = null;
    }
    if (stored === "true") return false; // explicitly expanded
    if (stored === "false") return true; // explicitly collapsed
    return !defaultExpanded(section); // no explicit value → tier default
  }

  // setCollapsed persists the explicit state, but prunes the entry when the
  // state matches the section's tier default — so storage only carries
  // deviations from the default and a default-collapsed project the user
  // collapses leaves no residue.
  function setCollapsed(key, collapsed, section) {
    var matchesDefault = collapsed === !defaultExpanded(section);
    try {
      if (matchesDefault) {
        window.localStorage.removeItem(STORAGE_PREFIX + key);
      } else {
        window.localStorage.setItem(STORAGE_PREFIX + key, collapsed ? "false" : "true");
      }
    } catch (e) {
      // localStorage may be disabled; collapse still works for this session.
    }
  }

  function applyCollapseState(section) {
    var key = section.getAttribute("data-project-key");
    if (!key) return;
    var collapsed = isCollapsed(key, section);
    section.classList.toggle("collapsed", collapsed);
    var chevron = section.querySelector(".project-chevron");
    if (chevron) {
      chevron.textContent = collapsed ? "▸" : "▾";
      chevron.setAttribute("aria-expanded", collapsed ? "false" : "true");
    }
  }

  function applyAll(root) {
    var scope = root || document;
    var sections = scope.querySelectorAll("[data-project-key]");
    for (var i = 0; i < sections.length; i++) {
      applyCollapseState(sections[i]);
    }
    // After applying localStorage-based state, expand the project containing
    // the active session row so navigating to a session always reveals it.
    var activeRow = scope.querySelector(".sb-row[data-active]");
    if (!activeRow) {
      // Fallback: match by current pathname if syncActiveRow hasn't run yet.
      var path = (window.location && window.location.pathname) || "";
      if (path && path.indexOf("/s/") === 0) {
        var clean = path.replace(/\/+$/, "");
        var rows = scope.querySelectorAll(".sb-row");
        for (var j = 0; j < rows.length; j++) {
          if ((rows[j].getAttribute("href") || "") === clean) {
            activeRow = rows[j];
            break;
          }
        }
      }
    }
    if (activeRow) {
      var activeSection = activeRow.closest("[data-project-key]");
      if (activeSection && activeSection.classList.contains("collapsed")) {
        var key = activeSection.getAttribute("data-project-key");
        activeSection.classList.remove("collapsed");
        var chevron = activeSection.querySelector(".project-chevron");
        if (chevron) {
          chevron.textContent = "▾";
          chevron.setAttribute("aria-expanded", "true");
        }
        // Persist the expansion so it survives sidebar re-renders.
        setCollapsed(key, false, activeSection);
      }
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
    setCollapsed(key, nextCollapsed, section);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { applyAll(document); });
  } else {
    applyAll(document);
  }

  document.addEventListener("htmx:afterSwap", function (e) {
    applyAll(e.target || document);
  });

  // First-paint stagger on the Live section. Sidebar.html re-renders every
  // 5s (hx-trigger="every 5s"), but only the very first paint earns the
  // choreography. After that, .stagger is removed and individual rows
  // appear instantly.
  var staggerApplied = false;
  function applyLiveStagger(scope) {
    if (staggerApplied) return;
    var live = (scope || document).querySelector(".sidebar-live-section");
    if (!live) return;
    var rows = live.querySelectorAll(".sb-row");
    if (!rows.length) return;
    live.classList.add("stagger");
    for (var i = 0; i < rows.length && i < 10; i++) {
      rows[i].style.setProperty("--i", String(i));
    }
    staggerApplied = true;
    // Strip the class after the longest animation finishes (10 rows × 30ms
    // delay + 160ms duration = 460ms; round to 600ms for safety).
    setTimeout(function () {
      live.classList.remove("stagger");
      for (var j = 0; j < rows.length; j++) {
        rows[j].style.removeProperty("--i");
      }
    }, 600);
  }

  document.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "sidebar") applyLiveStagger(e.target);
  });
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { applyLiveStagger(document); });
  } else {
    applyLiveStagger(document);
  }

  var sidebarRefreshTimer = null;
  function scheduleSidebarRefresh() {
    if (!window.htmx || !document.body) return;
    if (sidebarRefreshTimer) clearTimeout(sidebarRefreshTimer);
    sidebarRefreshTimer = setTimeout(function () {
      sidebarRefreshTimer = null;
      window.htmx.trigger(document.body, "sidebar:refresh");
    }, 50);
  }

  function notificationAffectsSidebar(method) {
    switch (method) {
      case "thread/started":
      case "thread/closed":
      case "thread/status/changed":
      case "thread/queueChanged":
      case "turn/started":
      case "turn/completed":
      case "item/started":
      case "item/completed":
      case "serf/job/started":
      case "serf/job/finished":
        return true;
      default:
        return false;
    }
  }

  if (window.SerfAppwire && typeof window.SerfAppwire.onNotification === "function") {
    window.SerfAppwire.onNotification(function (method) {
      if (notificationAffectsSidebar(method)) scheduleSidebarRefresh();
    });
  }
  if (window.SerfAppwire && typeof window.SerfAppwire.onConnectionRestored === "function") {
    window.SerfAppwire.onConnectionRestored(scheduleSidebarRefresh);
  }

  document.addEventListener("click", onChevronClick);

  // "+N subagents" fold toggle — reveals/hides the overflow subagent rows
  // (those past the first 3) within a project. The toggle flips
  // data-subagents-expanded on the .project-children container (CSS reveals
  // .subagent-overflow rows) and swaps its own label between "+N subagents"
  // and "− hide". Not persisted: subagent rows are ephemeral and the parent
  // project's collapse state already governs visibility across re-renders.
  function onSubagentToggle(e) {
    var toggle = e.target.closest(".subagent-toggle");
    if (!toggle) return;
    e.preventDefault();
    e.stopPropagation();
    var children = toggle.closest(".project-children");
    if (!children) return;
    var expanded = children.hasAttribute("data-subagents-expanded");
    if (expanded) {
      children.removeAttribute("data-subagents-expanded");
      toggle.setAttribute("aria-expanded", "false");
      if (toggle.dataset.collapsedLabel) toggle.textContent = toggle.dataset.collapsedLabel;
    } else {
      // Remember the "+N subagents" label so we can restore it on collapse.
      toggle.dataset.collapsedLabel = toggle.textContent;
      children.setAttribute("data-subagents-expanded", "");
      toggle.setAttribute("aria-expanded", "true");
      toggle.textContent = "− hide";
    }
  }
  document.addEventListener("click", onSubagentToggle);

  // Repeated-title cluster fold toggle (mockup #10/#C) — the cluster header
  // flips data-cluster-expanded on its .session-cluster (CSS reveals the member
  // runs), swaps the chevron glyph, and updates aria-expanded. Not persisted:
  // clusters are recomputed each render and the parent project's collapse state
  // already governs visibility across re-renders.
  function onClusterToggle(e) {
    var header = e.target.closest(".cluster-header");
    if (!header) return;
    e.preventDefault();
    e.stopPropagation();
    var cluster = header.closest(".session-cluster");
    if (!cluster) return;
    var expanded = cluster.hasAttribute("data-cluster-expanded");
    var chevron = header.querySelector(".cluster-chevron");
    if (expanded) {
      cluster.removeAttribute("data-cluster-expanded");
      header.setAttribute("aria-expanded", "false");
      if (chevron) chevron.textContent = "▸";
    } else {
      cluster.setAttribute("data-cluster-expanded", "");
      header.setAttribute("aria-expanded", "true");
      if (chevron) chevron.textContent = "▾";
    }
  }
  document.addEventListener("click", onClusterToggle);

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
      // Do not close the mobile sidebar when the event originated inside the search dialog.
      if (e.target && e.target.closest && e.target.closest("#search-dialog")) return;
      var dlg = document.getElementById("search-dialog");
      if (dlg && dlg.open) return;
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

  // Expose close so other modules (e.g. search) can fully close the mobile
  // sidebar drawer — deactivating its focus trap — before opening their own
  // overlay. Using the internal setSidebarOpen(false) ensures the trap is
  // properly torn down and the click-outside listener is removed.
  window.SerfSidebar = { close: function () { setSidebarOpen(false); } };
})();
