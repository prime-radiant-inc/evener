/* settings-pickers.js — lightweight model autocomplete for settings pages.
   Works by fetching /api/models and populating a <datalist> and a custom
   inline picker attached to any element with [data-settings-model-picker].
   Also wires shared directory picker behavior to settings dir controls. */
(function () {
  "use strict";

  // ---------- model picker ----------

  let _modelsCache = null;

  function fetchModels() {
    if (_modelsCache) return _modelsCache;
    _modelsCache = fetch("/api/models?diagnostics=1", { credentials: "same-origin" })
      .then(r => r.ok ? r.json() : { models: [], diagnostics: [], recent: [] })
      .catch(() => ({ models: [], diagnostics: [], recent: [] }));
    return _modelsCache;
  }

  function buildModelPicker(anchorBtn, hiddenInput, displayEl) {
    const existing = document.querySelector(".sp-picker");
    if (existing) { existing.remove(); return; }

    fetchModels().then(result => {
      const models = Array.isArray(result && result.models) ? result.models : [];
      const recent = Array.isArray(result && result.recent) ? result.recent : [];

      // Group by provider; "Recent" is a pinned-first pseudo-provider.
      const byProvider = {};
      if (recent.length > 0) byProvider["Recent"] = recent;
      models.forEach(m => {
        if (!byProvider[m.provider]) byProvider[m.provider] = [];
        byProvider[m.provider].push(m);
      });
      const providers = Object.keys(byProvider).filter(p => p !== "Recent").sort();
      if (byProvider["Recent"]) providers.unshift("Recent");

      const picker = document.createElement("div");
      picker.className = "sp-picker chip-picker chip-picker-wide";
      picker.style.position = "absolute";
      picker.style.zIndex = "50";

      const search = document.createElement("input");
      search.className = "chip-picker-search";
      search.placeholder = "search models…";
      picker.appendChild(search);

      const body = document.createElement("div");
      body.className = "chip-picker-body";

      const providerCol = document.createElement("div");
      providerCol.className = "chip-picker-providers";
      const modelCol = document.createElement("div");
      modelCol.className = "chip-picker-models";
      body.appendChild(providerCol);
      body.appendChild(modelCol);
      picker.appendChild(body);

      let activeProvider = providers[0] || "";

      function formatCtx(n) {
        if (n >= 1000000) return (n / 1000000).toFixed(1).replace(".0", "") + "M";
        if (n >= 1000) return (n / 1000).toFixed(0) + "K";
        return String(n);
      }

      function modelBadges(m) {
        const badges = [];
        if (m.supports_tools) badges.push("tools");
        if (m.supports_vision) badges.push("vision");
        if (m.supports_reasoning) {
          const levels = m.reasoning_effort_levels;
          badges.push(Array.isArray(levels) && levels.length ? "reasoning (" + levels.join("/") + ")" : "reasoning");
        }
        if (m.supports_web_search) badges.push("web search");
        return badges;
      }

      function renderProviders(filter) {
        providerCol.innerHTML = "";
        providers.forEach(p => {
          if (filter) {
            const hasMatch = byProvider[p].some(m =>
              (m.model + " " + (m.display_name || "")).toLowerCase().includes(filter)
            );
            if (!hasMatch) return;
          }
          const el = document.createElement("div");
          el.className = "chip-picker-provider" + (p === activeProvider ? " active" : "");
          el.textContent = p;
          el.addEventListener("click", () => {
            activeProvider = p;
            renderProviders(filter);
            renderModels(filter);
          });
          providerCol.appendChild(el);
        });
      }

      function renderModels(filter) {
        modelCol.innerHTML = "";
        const list = byProvider[activeProvider] || [];
        list.forEach(m => {
          if (filter) {
            const hay = (m.model + " " + (m.display_name || "")).toLowerCase();
            if (!hay.includes(filter)) return;
          }
          const el = document.createElement("div");
          el.className = "chip-picker-model";
          const name = document.createElement("div");
          name.className = "chip-picker-model-name";
          name.textContent = m.display_name || m.model;
          el.appendChild(name);
          const badges = modelBadges(m);
          if (badges.length) {
            const badgeRow = document.createElement("div");
            badgeRow.className = "chip-picker-model-badges";
            badges.forEach(b => {
              const span = document.createElement("span");
              span.className = "chip-picker-badge";
              span.textContent = b;
              badgeRow.appendChild(span);
            });
            el.appendChild(badgeRow);
          }
          const meta = document.createElement("div");
          meta.className = "chip-picker-model-meta";
          const parts = [];
          if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
          if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
          meta.textContent = parts.join(" · ");
          el.appendChild(meta);
          el.addEventListener("click", () => {
            const val = m.provider + "/" + m.model;
            hiddenInput.value = val;
            hiddenInput.dispatchEvent(new Event("change", { bubbles: true }));
            if (displayEl) {
              displayEl.textContent = (window.SerfSpawn && window.SerfSpawn.abbreviateModel)
                ? window.SerfSpawn.abbreviateModel(val)
                : val;
            }
            picker.remove();
          });
          modelCol.appendChild(el);
        });
      }

      search.addEventListener("input", () => {
        const q = search.value.toLowerCase().trim();
        if (q && byProvider[activeProvider] && !byProvider[activeProvider].some(m =>
            (m.model + " " + (m.display_name || "")).toLowerCase().includes(q))) {
          for (const p of providers) {
            if (byProvider[p].some(m => (m.model + " " + (m.display_name || "")).toLowerCase().includes(q))) {
              activeProvider = p;
              break;
            }
          }
        }
        renderProviders(q);
        renderModels(q);
      });

      renderProviders("");
      renderModels("");

      // Position below the anchor button
      anchorBtn.parentNode.style.position = "relative";
      anchorBtn.parentNode.appendChild(picker);
      picker.style.top = (anchorBtn.offsetTop + anchorBtn.offsetHeight + 4) + "px";
      picker.style.left = anchorBtn.offsetLeft + "px";
      search.focus();

      attachDismiss(picker);
    });
  }

  // attachDismiss adds click-outside and Escape handlers that close the
  // given picker element. composedPath handles clicks inside elements
  // that get re-rendered out from under the event (e.g. provider tabs).
  function attachDismiss(picker) {
    function dismiss() {
      picker.remove();
      document.removeEventListener("click", offClick);
      document.removeEventListener("keydown", onKey);
    }
    function offClick(e) {
      const path = (e.composedPath && e.composedPath()) || [];
      if (!picker.isConnected || !path.includes(picker)) dismiss();
    }
    function onKey(e) {
      if (e.key === "Escape") {
        e.preventDefault();
        dismiss();
      }
    }
    setTimeout(() => {
      document.addEventListener("click", offClick);
      document.addEventListener("keydown", onKey);
    }, 0);
  }

  // ---------- dir picker ----------

  function writeDirInput(input, value) {
    input.value = value;
    input.__spDirSuppressNextInput = true;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function openSharedDirPicker(anchor, input, opts) {
    if (!anchor || !input) return;
    if (!window.SerfDirPicker || typeof window.SerfDirPicker.open !== "function") return;
    opts = opts || {};
    window.SerfDirPicker.open({
      anchor,
      currentValue: input.value || "",
      placeholder: input.placeholder || "/path/to/repo",
      minWidth: "360px",
      inlineInput: opts.inline ? input : null,
      onAccept(value) { writeDirInput(input, value); },
    });
  }

  function wireDirInput(input) {
    if (input.__spDirInit) return;
    input.__spDirInit = true;

    input.addEventListener("input", () => {
      if (input.__spDirSuppressNextInput) {
        input.__spDirSuppressNextInput = false;
        return;
      }
      if (!input.value) return;
      openSharedDirPicker(input, input, { inline: true });
    });
    input.addEventListener("keydown", (e) => {
      if (e.key !== "ArrowDown") return;
      if (document.querySelector(".chip-picker-dir")) return;
      e.preventDefault();
      openSharedDirPicker(input, input, { inline: true });
    });
  }

  // ---------- init ----------

  function initSettingsPickers(root) {
    root = root || document;

    // Model pickers: button[data-settings-model-picker] toggles a picker;
    // it must be adjacent to a hidden input[name] and a .sp-model-display span.
    root.querySelectorAll("button[data-settings-model-picker]").forEach(btn => {
      if (btn.__spInit) return;
      btn.__spInit = true;
      const container = btn.closest(".sp-model-wrap");
      if (!container) return;
      const hidden = container.querySelector("input[type=hidden]");
      const display = container.querySelector(".sp-model-display");
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        buildModelPicker(btn, hidden, display);
      });
    });

    // Dir pickers: button[data-settings-dir-picker] toggles a dir picker for
    // the sibling input[type=text].
    root.querySelectorAll("button[data-settings-dir-picker]").forEach(btn => {
      if (btn.__spInit) return;
      btn.__spInit = true;
      const container = btn.closest(".sp-dir-wrap");
      if (!container) return;
      const input = container.querySelector("input[type=text]");
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        openSharedDirPicker(btn, input);
      });
    });

    // Inline dir autocomplete: input[data-settings-dir-input] opens the
    // shared directory picker, no separate picker button needed.
    root.querySelectorAll("input[data-settings-dir-input]").forEach(input => {
      wireDirInput(input);
    });
  }

  // Prefetch model list so the picker opens instantly
  fetchModels();

  window.SettingsPickers = { init: initSettingsPickers };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => initSettingsPickers());
  } else {
    initSettingsPickers();
  }
  // Re-init when HTMX swaps in new content
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", (e) => initSettingsPickers(e.target));
  });
  if (document.body) {
    document.body.addEventListener("htmx:afterSwap", (e) => initSettingsPickers(e.target));
  }
})();
