// SerfFocusTrap — minimal focus management for slide-over panels, modal
// dialogs, and the mobile sidebar drawer. The contract:
//
//   const handle = SerfFocusTrap.activate(panelEl, triggerEl);
//   // ...later...
//   SerfFocusTrap.deactivate(handle);
//
// On activate, the helper:
//   1. Captures the current activeElement (or the explicit triggerEl arg) as
//      the restore target.
//   2. Applies `inert` to every root-level sibling of panelEl so screen
//      readers and tab traversal skip them.
//   3. Binds a Tab/Shift+Tab handler that cycles focus inside panelEl.
//   4. Focuses the first focusable child of panelEl.
//
// On deactivate, the helper:
//   1. Removes `inert` from the siblings it applied it to.
//   2. Unbinds the Tab handler.
//   3. Returns focus to the captured restore target (if still in the DOM).
//
// The handle is opaque; callers pass it back to deactivate. Each activate
// produces a fresh handle, so multiple traps can be active concurrently and
// torn down in any order — though in practice serf-hub opens one at a time.
(function () {
  "use strict";

  // Standard focusable selectors. Tabbable subset = these minus [tabindex="-1"].
  var FOCUSABLE = [
    "a[href]",
    "button:not([disabled])",
    "input:not([disabled]):not([type=hidden])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    "[tabindex]:not([tabindex='-1'])",
    "summary",
  ].join(",");

  function tabbable(root) {
    var nodes = root.querySelectorAll(FOCUSABLE);
    var out = [];
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      if (n.hasAttribute("disabled")) continue;
      if (n.getAttribute("tabindex") === "-1") continue;
      // Skip hidden elements — offsetParent is null for display:none subtrees
      // in normal layout; JSDOM returns null too. inert subtrees are also
      // skipped because their items report tabindex=-1.
      out.push(n);
    }
    return out;
  }

  function activate(panel, returnFocusTo) {
    if (!panel) return null;
    var restoreTarget = returnFocusTo || document.activeElement;
    var siblings = [];
    var parent = panel.parentNode;
    if (parent) {
      for (var i = 0; i < parent.children.length; i++) {
        var sib = parent.children[i];
        if (sib === panel) continue;
        if (sib.hasAttribute("inert")) continue; // don't double-toggle
        sib.setAttribute("inert", "");
        siblings.push(sib);
      }
    }

    function onKeyDown(e) {
      if (e.key !== "Tab") return;
      var list = tabbable(panel);
      if (list.length === 0) {
        e.preventDefault();
        return;
      }
      var first = list[0];
      var last = list[list.length - 1];
      var active = document.activeElement;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
      // Otherwise let the browser do its natural Tab.
    }

    panel.addEventListener("keydown", onKeyDown);

    // Initial focus: first tabbable inside the panel, else the panel itself.
    var initial = tabbable(panel)[0];
    if (initial) {
      initial.focus();
    } else if (panel.tabIndex < 0) {
      panel.setAttribute("tabindex", "-1");
      panel.focus();
    }

    return { panel: panel, siblings: siblings, restoreTarget: restoreTarget, onKeyDown: onKeyDown };
  }

  function deactivate(handle) {
    if (!handle) return;
    if (handle.panel && handle.onKeyDown) {
      handle.panel.removeEventListener("keydown", handle.onKeyDown);
    }
    for (var i = 0; i < handle.siblings.length; i++) {
      handle.siblings[i].removeAttribute("inert");
    }
    var t = handle.restoreTarget;
    if (t && typeof t.focus === "function" && document.contains(t)) {
      t.focus();
    }
  }

  window.SerfFocusTrap = { activate: activate, deactivate: deactivate };
})();
