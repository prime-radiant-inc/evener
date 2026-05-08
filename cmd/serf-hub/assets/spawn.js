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
    if (kind === "model") {
      openModelPicker(chip);
      return;
    }
    const display = chip.querySelector(".chip-value");
    const current = display.textContent.trim();
    let value;
    if (kind === "working_dir") {
      value = prompt("working directory (absolute path)", current);
    } else if (kind === "branch") {
      value = prompt("branch / worktree", current);
    } else if (kind === "access_mode") {
      value = current === "full" ? "read-only" : "full";
    }
    if (value !== null && value !== undefined && value !== "") setChipValue(kind, value);
  }

  function openModelPicker(chip) {
    // Remove any existing picker (toggle)
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }

    fetch("/api/models").then(r => r.json()).then(models => {
      const picker = document.createElement("div");
      picker.className = "chip-picker";
      models.forEach(m => {
        const opt = document.createElement("div");
        opt.className = "chip-picker-option";
        opt.textContent = m.provider + " · " + m.model;
        opt.addEventListener("click", () => {
          setChipValue("model", m.provider + "/" + m.model);
          picker.remove();
        });
        picker.appendChild(opt);
      });
      chip.parentNode.style.position = "relative";
      chip.parentNode.appendChild(picker);
      picker.style.position = "absolute";
      picker.style.top = (chip.offsetTop + chip.offsetHeight + 4) + "px";
      picker.style.left = chip.offsetLeft + "px";
      picker.style.zIndex = "50";

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
