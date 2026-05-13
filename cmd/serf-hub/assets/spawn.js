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
    const saved = Object.assign({}, d);
    if (!harnessUsesSerfModels(saved.harness)) delete saved.model;
    localStorage.setItem(projectKey(wd), JSON.stringify(saved));
    // Persist model globally across projects
    if (harnessUsesSerfModels(d.harness) && d.model) localStorage.setItem("serf-hub.spawn-defaults.global.model", d.model);
  }

  function currentHarness() {
    const el = document.querySelector('input[type=hidden][name="harness"]');
    return (el && el.value) || "serf";
  }

  function harnessUsesSerfModels(harness) {
    return !harness || harness === "serf";
  }

  function modelPlaceholder(harness) {
    harness = harness || "serf";
    return harnessUsesSerfModels(harness) ? "(pick a model)" : harness + " default";
  }

  function setModelValue(value, displayValue) {
    const display = document.querySelector("[data-chip-value-model]");
    const hidden = document.querySelector('input[type=hidden][name="model"]');
    if (display) display.textContent = displayValue || value || modelPlaceholder(currentHarness());
    if (hidden) hidden.value = value || "";
  }

  function setChipValue(name, value) {
    if (name === "model") {
      setModelValue(value);
      return;
    }
    const display = document.querySelector('[data-chip-value-' + name + ']');
    const hidden = document.querySelector('input[type=hidden][name="' + name + '"]');
    if (display) display.textContent = value || "(default)";
    if (hidden) hidden.value = value || "";
  }

  function applyHarnessModelPolicy(harness) {
    if (!harnessUsesSerfModels(harness)) {
      setModelValue("");
      return;
    }
    const hidden = document.querySelector('input[type=hidden][name="model"]');
    if (!hidden || !hidden.value || !hidden.value.includes("/")) setModelValue("");
  }

  function routeID(spawnResult) {
    spawnResult = spawnResult || {};
    const ref = String(spawnResult.ref || "");
    if (ref && !ref.startsWith("local:")) return ref;
    const sessionID = String(spawnResult.session_id || spawnResult.sessionId || "");
    if (sessionID.startsWith("local:")) return sessionID.slice("local:".length);
    if (sessionID) return sessionID;
    if (ref.startsWith("local:")) return ref.slice("local:".length);
    return ref;
  }

  function sessionPath(spawnResult) {
    return "/s/" + encodeURIComponent(routeID(spawnResult));
  }

  function spawnErrorMessage(text) {
    try {
      const parsed = JSON.parse(text || "{}");
      if (parsed && parsed.error) return parsed.error;
    } catch (e) {
      // Plain-text errors come from older fallback paths.
    }
    return text;
  }

  function clearSpawnError(form) {
    const existing = form.querySelector("[data-spawn-error]");
    if (existing) existing.remove();
  }

  function spawnFailureMessage(err) {
    const detail = (err && err.message) ? err.message : String(err || "unknown error");
    return detail.toLowerCase().startsWith("spawn failed") ? detail : "spawn failed: " + detail;
  }

  function fallbackSpawnDiagnostic(message) {
    const el = document.createElement("div");
    el.className = "diagnostic diagnostic-error diagnostic-source-hub";
    el.setAttribute("role", "alert");

    const header = document.createElement("div");
    header.className = "diagnostic-header";

    const badge = document.createElement("span");
    badge.className = "diagnostic-badge";
    badge.textContent = "Hub error";
    header.appendChild(badge);

    const title = document.createElement("span");
    title.className = "diagnostic-title";
    title.textContent = "Hub spawn error";
    header.appendChild(title);
    el.appendChild(header);

    const body = document.createElement("div");
    body.className = "diagnostic-message";
    body.textContent = message;
    el.appendChild(body);

    return el;
  }

  function renderSpawnError(form, err) {
    clearSpawnError(form);
    const message = spawnFailureMessage(err);
    const el = window.SerfDiagnostics && window.SerfDiagnostics.render
      ? window.SerfDiagnostics.render({ severity: "error", source: "hub", title: "Hub spawn error", message })
      : fallbackSpawnDiagnostic(message);
    el.dataset.spawnError = "true";
    const actions = form.querySelector(".spawn-actions");
    form.insertBefore(el, actions || null);
  }

  function init() {
    const form = document.querySelector("[data-spawn-form]");
    if (!form) return;

    // Apply sticky defaults on top of server-provided defaults
    const defaults = loadDefaults();
    ["harness", "working_dir", "branch", "access_mode"].forEach(k => {
      if (defaults[k]) setChipValue(k, defaults[k]);
    });
    if (harnessUsesSerfModels(currentHarness()) && defaults.model) {
      setChipValue("model", defaults.model);
    } else {
      applyHarnessModelPolicy(currentHarness());
    }

    // Chip pickers
    document.querySelectorAll(".chip").forEach(chip => {
      chip.addEventListener("click", () => openPicker(chip));
    });

    // Recent prompt click pre-fills
    document.querySelectorAll("[data-recent-prompt]").forEach(row => {
      row.addEventListener("click", () => {
        form.querySelector("textarea[name=prompt]").value = row.dataset.recentPrompt;
        form.querySelector("textarea[name=prompt]").focus();
      });
    });

    // ⌘↵ submits
    form.querySelector("textarea[name=prompt]").addEventListener("keydown", (e) => {
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
        prompt: fd.get("prompt") || "",
        harness: fd.get("harness") || "serf",
        model: fd.get("model") || "",
        working_dir: fd.get("working_dir") || "",
        branch: fd.get("branch") || "",
        access_mode: fd.get("access_mode") || "full",
        agent: fd.get("agent_override") || fd.get("agent") || "default",
        reasoning_effort: fd.get("reasoning_effort_override") || fd.get("reasoning_effort") || "",
      };
      // Persist sticky defaults (excluding the prompt override)
      saveDefaults({
        model: body.model,
        harness: body.harness,
        working_dir: body.working_dir,
        branch: body.branch,
        access_mode: body.access_mode,
      });
      clearSpawnError(form);
      const btn = form.querySelector(".spawn-btn");
      if (btn) { btn.disabled = true; btn.textContent = "spawning…"; }
      try {
        const json = window.SerfAppwire
          ? await window.SerfAppwire.startThread(body)
          : await fetch("/api/spawn", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(body),
            }).then(async (resp) => {
              if (!resp.ok) throw new Error(spawnErrorMessage(await resp.text()));
              return resp.json();
            });
        window.location.href = sessionPath(json);
      } catch (err) {
        if (btn) { btn.disabled = false; btn.innerHTML = 'spawn <kbd>⌘↵</kbd>'; }
        renderSpawnError(form, err);
      }
    });
  }

  function openPicker(chip) {
    const kind = chip.dataset.chip;
    if (kind === "harness") { openHarnessPicker(chip); return; }
    if (kind === "model") { openModelPicker(chip); return; }
    if (kind === "working_dir") { openDirPicker(chip); return; }
    if (kind === "branch") { openTextPicker(chip, "branch", "branch / worktree"); return; }
    const display = chip.querySelector(".chip-value");
    const current = display.textContent.trim();
    let value;
    if (kind === "access_mode") {
      value = current === "full" ? "read-only" : "full";
    }
    if (value !== null && value !== undefined && value !== "") setChipValue(kind, value);
  }

  function openTextPicker(chip, name, placeholder) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }

    const display = chip.querySelector(".chip-value");
    const current = display ? display.textContent.trim() : "";

    const picker = document.createElement("div");
    picker.className = "chip-picker chip-picker-text";

    const input = document.createElement("input");
    input.className = "chip-picker-search";
    input.placeholder = placeholder;
    input.value = current === "(default)" ? "" : current;
    picker.appendChild(input);

    function accept() {
      const value = input.value.trim();
      if (value) setChipValue(name, value);
      picker.remove();
    }

    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        accept();
      } else if (e.key === "Escape") {
        e.preventDefault();
        picker.remove();
      }
    });

    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
    picker.style.left = chip.offsetLeft + "px";
    picker.style.zIndex = "50";

    input.focus();

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

  function openHarnessPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }
    const picker = document.createElement("div");
    picker.className = "chip-picker";
    const options = Array.from(document.querySelectorAll("[data-harness-option]")).map(el => ({
      id: el.value || "",
      label: el.dataset.label || el.value || "",
    })).filter(opt => opt.id);
    if (options.length === 0) options.push({ id: "serf", label: "serf" });
    for (const opt of options) {
      const row = document.createElement("div");
      row.className = "chip-picker-option";
      row.textContent = opt.label;
      row.addEventListener("click", () => {
        setChipValue("harness", opt.id);
        applyHarnessModelPolicy(opt.id);
        picker.remove();
      });
      picker.appendChild(row);
    }
    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
    picker.style.left = chip.offsetLeft + "px";
    picker.style.zIndex = "50";
  }

  function openModelPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }
    const harness = currentHarness();

    const modelsPromise = listModelsForHarness(harness);
    modelsPromise.then(models => {
      if (!Array.isArray(models)) models = [];
      if (models.length === 0 && !harnessUsesSerfModels(harness)) {
        openHarnessDefaultModelPicker(chip);
        return;
      }

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
            if (harnessUsesSerfModels(harness)) {
              setChipValue("model", m.provider + "/" + m.model);
            } else {
              setModelValue(m.model, modelOptionLabel(m));
            }
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
          // Use composedPath, not picker.contains(e.target) — the click
          // target may have already been removed from the DOM by the
          // picker's own re-render (e.g., clicking a provider re-renders
          // the column), and a stale target reads as "outside the picker".
          const path = (e.composedPath && e.composedPath()) || [];
          if (!path.includes(picker)) {
            picker.remove();
            document.removeEventListener("click", offClick);
          }
        };
        document.addEventListener("click", offClick);
      }, 0);
    }).catch(() => {
      if (!harnessUsesSerfModels(harness)) {
        openHarnessDefaultModelPicker(chip);
      }
    });
  }

  function listModelsForHarness(harness) {
    if (window.SerfAppwire) {
      return window.SerfAppwire.listModels(harnessUsesSerfModels(harness) ? {} : { harness });
    }
    const suffix = harnessUsesSerfModels(harness) ? "" : "?harness=" + encodeURIComponent(harness);
    return fetch("/api/models" + suffix).then(r => r.json());
  }

  function modelOptionLabel(model) {
    const name = model && model.model ? String(model.model) : "";
    const provider = model && model.provider ? String(model.provider) : "";
    return provider ? provider + "/" + name : name;
  }

  function openHarnessDefaultModelPicker(chip) {
    const picker = document.createElement("div");
    picker.className = "chip-picker";
    const row = document.createElement("div");
    row.className = "chip-picker-option";
    row.textContent = modelPlaceholder(currentHarness());
    row.addEventListener("click", () => {
      setModelValue("");
      picker.remove();
    });
    picker.appendChild(row);
    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
    picker.style.left = chip.offsetLeft + "px";
    picker.style.zIndex = "50";
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
      const dirsPromise = window.SerfAppwire
        ? window.SerfAppwire.completeDirs(prefix)
        : fetch("/api/dirs?prefix=" + encodeURIComponent(prefix)).then(r => r.json());
      dirsPromise.then(data => {
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
  window.SerfSpawn = {
    sessionPath,
    spawnErrorMessage,
  };
})();
