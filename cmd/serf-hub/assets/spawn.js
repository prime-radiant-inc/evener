(function () {
  "use strict";

  function projectKey(workingDir) {
    return "serf-hub.spawn-defaults." + (workingDir || "global");
  }

  function currentWorkingDir() {
    const el = document.querySelector('input[type=hidden][name="working_dir"]');
    return el ? el.value : "";
  }

  function loadDefaults() {
    try {
      const wd = currentWorkingDir();
      const perProject = JSON.parse(localStorage.getItem(projectKey(wd)) || "{}");
      // Layer the global model default underneath the per-project value
      const globalModel = localStorage.getItem("serf-hub.spawn-defaults.global.model") || "";
      if (!perProject.model && globalModel) perProject.model = globalModel;
      return perProject;
    } catch (e) { return {}; }
  }
  function saveDefaults(d) {
    const wd = currentWorkingDir();
    localStorage.setItem(projectKey(wd), JSON.stringify(d));
    // Persist model globally across projects
    if (d.model) localStorage.setItem("serf-hub.spawn-defaults.global.model", d.model);
  }

  function setChipValue(name, value) {
    const display = document.querySelector('[data-chip-value-' + name + ']');
    const hidden = document.querySelector('input[type=hidden][name="' + name + '"]');
    if (display) display.textContent = value || "(default)";
    if (hidden) hidden.value = value || "";
  }

  function init() {
    const form = document.querySelector("[data-spawn-form]");
    if (!form) return;

    // Apply sticky defaults on top of server-provided defaults
    const defaults = loadDefaults();
    ["model", "working_dir", "branch", "access_mode"].forEach(k => {
      if (defaults[k]) setChipValue(k, defaults[k]);
    });

    // Chip pickers
    document.querySelectorAll(".chip").forEach(chip => {
      chip.addEventListener("click", () => openPicker(chip));
    });

    // Recent task click pre-fills
    document.querySelectorAll("[data-recent-task]").forEach(row => {
      row.addEventListener("click", () => {
        form.querySelector("textarea[name=task]").value = row.dataset.recentTask;
        form.querySelector("textarea[name=task]").focus();
      });
    });

    // ⌘↵ submits
    form.querySelector("textarea[name=task]").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        form.requestSubmit();
      }
    });

    // Submit handler
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const fd = new FormData(form);
      const body = {
        task: fd.get("task") || "",
        model: fd.get("model") || "",
        working_dir: fd.get("working_dir") || "",
        branch: fd.get("branch") || "",
        access_mode: fd.get("access_mode") || "full",
        agent: fd.get("agent_override") || fd.get("agent") || "default",
        reasoning_effort: fd.get("reasoning_effort_override") || fd.get("reasoning_effort") || "",
      };
      // Persist sticky defaults (excluding the per-task override)
      saveDefaults({
        model: body.model,
        working_dir: body.working_dir,
        branch: body.branch,
        access_mode: body.access_mode,
      });
      const btn = form.querySelector(".spawn-btn");
      if (btn) { btn.disabled = true; btn.textContent = "spawning…"; }
      try {
        const resp = await fetch("/api/spawn", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          if (btn) { btn.disabled = false; btn.innerHTML = 'spawn <kbd>⌘↵</kbd>'; }
          alert("spawn failed: " + (await resp.text()));
          return;
        }
        const json = await resp.json();
        window.location.href = "/s/" + encodeURIComponent(json.session_id);
      } catch (err) {
        if (btn) { btn.disabled = false; btn.innerHTML = 'spawn <kbd>⌘↵</kbd>'; }
        alert("spawn failed: " + err.message);
      }
    });
  }

  function openPicker(chip) {
    const kind = chip.dataset.chip;
    if (kind === "model") { openModelPicker(chip); return; }
    if (kind === "working_dir") { openDirPicker(chip); return; }
    const display = chip.querySelector(".chip-value");
    const current = display.textContent.trim();
    let value;
    if (kind === "branch") {
      value = prompt("branch / worktree", current);
    } else if (kind === "access_mode") {
      value = current === "full" ? "read-only" : "full";
    }
    if (value !== null && value !== undefined && value !== "") setChipValue(kind, value);
  }

  function openModelPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }

    fetch("/api/models").then(r => r.json()).then(models => {
      if (!Array.isArray(models)) models = [];

      // Group by provider
      const byProvider = {};
      models.forEach(m => {
        if (!byProvider[m.provider]) byProvider[m.provider] = [];
        byProvider[m.provider].push(m);
      });
      const providers = Object.keys(byProvider).sort();

      const picker = document.createElement("div");
      picker.className = "chip-picker chip-picker-wide";

      // Search box
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

      function formatCtx(n) {
        if (n >= 1000000) return (n / 1000000).toFixed(1).replace(".0", "") + "M";
        if (n >= 1000) return (n / 1000).toFixed(0) + "K";
        return String(n);
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
          if (m.output_cost_per_million != null) parts.push("$" + m.output_cost_per_million.toFixed(2) + "/M out");
          meta.textContent = parts.join(" · ");
          el.appendChild(name);
          el.appendChild(meta);
          el.addEventListener("click", () => {
            setChipValue("model", m.provider + "/" + m.model);
            picker.remove();
          });
          modelCol.appendChild(el);
        });
      }

      search.addEventListener("input", () => {
        const q = search.value.toLowerCase().trim();
        // If query matches another provider's models, switch to that provider.
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

      chip.parentNode.style.position = "relative";
      chip.parentNode.appendChild(picker);
      picker.style.position = "absolute";
      picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
      picker.style.left = chip.offsetLeft + "px";
      picker.style.zIndex = "50";

      search.focus();

      setTimeout(() => {
        const offClick = (e) => {
          if (!picker.contains(e.target)) {
            picker.remove();
            document.removeEventListener("click", offClick);
          }
        };
        document.addEventListener("click", offClick);
      }, 0);
    });
  }

  function openDirPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }

    const display = chip.querySelector(".chip-value");
    const current = (display.textContent.trim() === "(pick a directory)") ? "" : display.textContent.trim();

    const picker = document.createElement("div");
    picker.className = "chip-picker chip-picker-dir";

    const input = document.createElement("input");
    input.className = "chip-picker-search";
    input.placeholder = "/path/to/repo";
    input.value = current || (window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") || "");
    picker.appendChild(input);

    const results = document.createElement("div");
    results.className = "chip-picker-results";
    picker.appendChild(results);

    let timer = null;

    function fetchDirs(prefix) {
      fetch("/api/dirs?prefix=" + encodeURIComponent(prefix)).then(r => r.json()).then(data => {
        results.innerHTML = "";
        const list = data.results || [];
        if (list.length === 0) {
          const empty = document.createElement("div");
          empty.className = "chip-picker-empty";
          empty.textContent = "no matching directories";
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
            setChipValue("working_dir", r.path);
            window.localStorage.setItem("serf-hub.spawn-defaults.global.last-working-dir", r.path);
            picker.remove();
          });
          results.appendChild(el);
        });
      });
    }

    input.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => fetchDirs(input.value), 150);
    });

    // Tab autocompletes to first result + "/"; Enter accepts first result or literal.
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-row");
        if (first) first.click();
        else if (input.value) {
          setChipValue("working_dir", input.value);
          picker.remove();
        }
      } else if (e.key === "Tab") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-path");
        if (first) input.value = first.textContent + "/";
      }
    });

    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
    picker.style.left = chip.offsetLeft + "px";
    picker.style.zIndex = "50";

    input.focus();
    fetchDirs(input.value);

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

  // Re-init whenever a spawn form appears in the DOM (initial load or
  // workspace swap). Idempotent via a per-form marker.
  function tryInit() {
    const form = document.querySelector("[data-spawn-form]");
    if (!form || form.__spawnInitialized) return;
    form.__spawnInitialized = true;
    init();
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", tryInit);
  } else {
    tryInit();
  }
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", tryInit);
  });
  if (document.body) document.body.addEventListener("htmx:afterSwap", tryInit);
})();
