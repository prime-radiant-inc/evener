(function () {
  "use strict";

  const DEFAULTS_KEY = "serf-hub.spawn-defaults";

  function loadDefaults() {
    try {
      return JSON.parse(localStorage.getItem(DEFAULTS_KEY) || "{}");
    } catch (e) { return {}; }
  }
  function saveDefaults(d) {
    localStorage.setItem(DEFAULTS_KEY, JSON.stringify(d));
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
      try {
        const resp = await fetch("/api/spawn", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          alert("spawn failed: " + (await resp.text()));
          return;
        }
        const json = await resp.json();
        window.location.href = "/s/" + encodeURIComponent(json.session_id);
      } catch (err) {
        alert("spawn failed: " + err.message);
      }
    });
  }

  function openPicker(chip) {
    const kind = chip.dataset.chip;
    const display = chip.querySelector(".chip-value");
    const current = display.textContent.trim();
    let value;
    if (kind === "model") {
      // Fetch /api/models and prompt
      fetch("/api/models").then(r => r.json()).then(models => {
        const choices = models.map(m => m.provider + "/" + m.model).join("\n");
        const picked = prompt("model (current: " + current + ")\n" + choices);
        if (picked) setChipValue("model", picked);
      });
      return;
    }
    if (kind === "working_dir") {
      value = prompt("working directory (absolute path)", current);
    } else if (kind === "branch") {
      value = prompt("branch / worktree", current);
    } else if (kind === "access_mode") {
      value = current === "full" ? "read-only" : "full";
    }
    if (value !== null && value !== undefined && value !== "") setChipValue(kind, value);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
