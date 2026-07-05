// Settings-nav filter — delegated so it works after HTMX swaps the settings shell.
(function () {
  document.body.addEventListener("input", (e) => {
    const input = e.target && e.target.closest("[data-settings-nav-filter]");
    if (!input) return;
    const q = input.value.trim().toLowerCase();
    const nav = input.closest(".settings-nav");
    if (!nav) return;
    nav.querySelectorAll(".settings-nav-link").forEach(a => {
      const visible = !q || a.textContent.toLowerCase().includes(q);
      a.hidden = !visible;
    });
    // Hide section headers whose children are all hidden
    nav.querySelectorAll(".settings-nav-section").forEach(h => {
      let nxt = h.nextElementSibling;
      let anyVisible = false;
      while (nxt && !nxt.classList.contains("settings-nav-section")) {
        if (nxt.classList.contains("settings-nav-link") && !nxt.hidden) { anyVisible = true; break; }
        nxt = nxt.nextElementSibling;
      }
      h.hidden = !anyVisible;
    });
  });
})();

// Phone nav-as-page wiring — delegated so it works after HTMX swaps the settings shell.
(function () {
  const body = document.body;

  function syncPane() {
    // If we're in a settings route, default to content; if at /settings (root)
    // with no Active section, show nav. The Active section is rendered into
    // the title — use its presence as the signal.
    const title = document.querySelector(".workspace-title .title[data-settings-section]");
    const isContent = !!(title && title.textContent.trim());
    body.dataset.settingsPane = isContent ? "content" : "nav";

    // Toggle the back button's hidden attribute — CSS display cannot override
    // the HTML hidden attribute, so we must manage it explicitly.
    const back = document.querySelector(".settings-nav-back");
    if (back) {
      if (isContent) {
        back.removeAttribute("hidden");
      } else {
        back.setAttribute("hidden", "");
      }
    }
  }

  // Delegated click handler for the back button — survives DOM swaps.
  document.body.addEventListener("click", (e) => {
    if (!e.target || !e.target.closest) return;
    const btn = e.target.closest(".settings-nav-back");
    if (!btn) return;
    body.dataset.settingsPane = "nav";
    const back = document.querySelector(".settings-nav-back");
    if (back) back.setAttribute("hidden", "");
    // Navigate to /settings root via history; HTMX is not used here because
    // the visibility-only flip is local.
    if (window.history && history.pushState) history.pushState({}, "", "/settings");
  });

  // Run syncPane on initial load and after any HTMX swap that brings in the
  // settings shell (#workspace) or updates the active content (#settings-content).
  function onAfterSwap(ev) {
    if (!ev.detail || !ev.detail.target) return;
    const id = ev.detail.target.id;
    if (id === "workspace" || id === "settings-content") {
      syncPane();
    }
  }

  document.addEventListener("DOMContentLoaded", syncPane);
  document.body.addEventListener("htmx:afterSwap", onAfterSwap);
})();
