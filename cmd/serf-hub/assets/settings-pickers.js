/* settings-pickers.js — lightweight model autocomplete for settings pages.
   Works by fetching /api/models and populating a <datalist> and a custom
   inline picker attached to any element with [data-settings-model-picker].
   Also wires /api/dirs autocomplete to inputs with [data-settings-dir-picker]. */
(function () {
  "use strict";

  // ---------- model picker ----------

  let _modelsCache = null;

  function fetchModels() {
    if (_modelsCache) return _modelsCache;
    _modelsCache = fetch("/api/models", { credentials: "same-origin" })
      .then(r => r.ok ? r.json() : [])
      .catch(() => []);
    return _modelsCache;
  }

  function buildModelPicker(anchorBtn, hiddenInput, displayEl) {
    const existing = document.querySelector(".sp-picker");
    if (existing) { existing.remove(); return; }

    fetchModels().then(models => {
      if (!Array.isArray(models)) models = [];

      // Group by provider
      const byProvider = {};
      models.forEach(m => {
        if (!byProvider[m.provider]) byProvider[m.provider] = [];
        byProvider[m.provider].push(m);
      });
      const providers = Object.keys(byProvider).sort();

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
          name.textContent = m.model;
          const meta = document.createElement("div");
          meta.className = "chip-picker-model-meta";
          const parts = [];
          if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
          if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
          meta.textContent = parts.join(" · ");
          el.appendChild(name);
          el.appendChild(meta);
          el.addEventListener("click", () => {
            const val = m.provider + "/" + m.model;
            hiddenInput.value = val;
            if (displayEl) displayEl.textContent = val;
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

      setTimeout(() => {
        const offClick = (e) => {
          const path = (e.composedPath && e.composedPath()) || [];
          if (!path.includes(picker)) {
            picker.remove();
            document.removeEventListener("click", offClick);
          }
        };
        document.addEventListener("click", offClick);
      }, 0);
    });
  }

  // ---------- dir picker ----------

  function buildDirPicker(anchorBtn, input) {
    const existing = document.querySelector(".sp-picker");
    if (existing) { existing.remove(); return; }

    const picker = document.createElement("div");
    picker.className = "sp-picker chip-picker chip-picker-dir";
    picker.style.position = "absolute";
    picker.style.zIndex = "50";
    picker.style.minWidth = "360px";

    const search = document.createElement("input");
    search.className = "chip-picker-search";
    search.placeholder = "/path/to/dir";
    search.value = input.value || "";
    picker.appendChild(search);

    const results = document.createElement("div");
    results.className = "chip-picker-results";
    picker.appendChild(results);

    let timer = null;

    function fetchDirs(prefix) {
      const p = window.SerfAppwire
        ? window.SerfAppwire.completeDirs(prefix)
        : fetch("/api/dirs?prefix=" + encodeURIComponent(prefix), { credentials: "same-origin" }).then(r => r.json());
      p.then(data => {
        results.innerHTML = "";
        const list = (data && data.results) || [];
        if (list.length === 0) {
          const empty = document.createElement("div");
          empty.className = "chip-picker-empty";
          empty.textContent = "no matching directories";
          empty.style.cssText = "padding:8px 12px;color:var(--text-muted);font-size:12px;";
          results.appendChild(empty);
          return;
        }
        list.forEach(r => {
          const el = document.createElement("div");
          el.className = "chip-picker-dir-row";
          const path = document.createElement("span");
          path.className = "chip-picker-dir-path";
          path.textContent = r.path;
          el.appendChild(path);
          if (r.is_git) {
            const tag = document.createElement("span");
            tag.className = "chip-picker-dir-tag";
            tag.textContent = "git";
            el.appendChild(tag);
          }
          el.addEventListener("click", () => {
            input.value = r.path;
            search.value = r.path;
            picker.remove();
          });
          results.appendChild(el);
        });
      }).catch(() => {});
    }

    search.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => fetchDirs(search.value), 150);
    });

    search.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-row");
        if (first) first.click();
        else if (search.value) {
          input.value = search.value;
          picker.remove();
        }
      } else if (e.key === "Tab") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-path");
        if (first) search.value = first.textContent + "/";
      }
    });

    anchorBtn.parentNode.style.position = "relative";
    anchorBtn.parentNode.appendChild(picker);
    picker.style.top = (anchorBtn.offsetTop + anchorBtn.offsetHeight + 4) + "px";
    picker.style.left = anchorBtn.offsetLeft + "px";
    search.focus();
    fetchDirs(search.value);

    setTimeout(() => {
      const offClick = (e) => {
        if (!picker.contains(e.target)) {
          picker.remove();
          document.removeEventListener("click", offClick);
        }
      };
      document.addEventListener("click", offClick);
    }, 0);
  }

  // ---------- inline dir autocomplete ----------

  function wireDirInput(input) {
    if (input.__spDirInit) return;
    input.__spDirInit = true;

    // Create a datalist for suggestions
    const listId = "sp-dir-list-" + Math.random().toString(36).slice(2);
    const dl = document.createElement("datalist");
    dl.id = listId;
    input.setAttribute("list", listId);
    input.parentNode.insertBefore(dl, input.nextSibling);

    let timer = null;
    input.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      const prefix = input.value;
      if (!prefix) { dl.innerHTML = ""; return; }
      timer = setTimeout(() => {
        const p = window.SerfAppwire
          ? window.SerfAppwire.completeDirs(prefix)
          : fetch("/api/dirs?prefix=" + encodeURIComponent(prefix), { credentials: "same-origin" }).then(r => r.json());
        p.then(data => {
          const list = (data && data.results) || [];
          dl.innerHTML = "";
          list.forEach(r => {
            const opt = document.createElement("option");
            opt.value = r.path;
            dl.appendChild(opt);
          });
        }).catch(() => {});
      }, 150);
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
        buildDirPicker(btn, input);
      });
    });

    // Inline dir autocomplete: input[data-settings-dir-input] gets a datalist
    // wired to /api/dirs completions, no separate picker button needed.
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
