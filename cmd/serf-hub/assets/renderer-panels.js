(function () {
  "use strict";

  // Side panels and chrome: the tasks panel and details panel, the tab-title
  // updater (with its own DOMContentLoaded/afterSwap wiring), session-action
  // triggers, scrim/click-outside helpers, and the task badge. Reaches the
  // live renderer through the window.SerfRenderer global at call time, so this
  // loads after renderer-tools.js and before renderer.js. toggleTasksPanel,
  // currentTaskSummary, and updateTasksBadge are the core-facing exports.

  const { partialFetch, sessionPartialPath, rememberTask, clip, buildTaskRowLine } =
    window.SerfRendererInternal;

  // Tab title — track sessions awaiting reply for the title-count notification
  // (off by default; opt-in via Settings → Notifications).
  // Pattern: "<section> · serf hub" for named pages; "serf hub" for the root.
  // Use the shared map from notifications.js if loaded; fall back to a
  // local copy so renderer.js still works if the asset load order ever
  // changes.
  const SETTINGS_SECTION_LABELS = window.SerfSectionLabels || {
    "general": "general", "theme": "theme", "notifications": "notifications",
    "providers": "providers", "agents": "agents",
    "launch-serf": "serf launch", "launch-codex": "codex launch",
    "inrepo": "in-repo config",
    "plugins": "plugins", "skills": "skills", "mcp": "mcp servers",
    "hub": "hub", "storage": "storage", "project": "project",
  };
  function pageSection() {
    // First check for an explicit data-settings-section attribute on the title span
    // (set by the settings shell on initial load).
    const el = document.querySelector(".workspace-title .title[data-settings-section]");
    if (el) {
      // After htmx nav within settings the URL may have changed — prefer the URL.
      const urlMatch = location.pathname.match(/^\/settings\/([^/?]+)/);
      if (urlMatch) return SETTINGS_SECTION_LABELS[urlMatch[1]] || urlMatch[1];
      const sec = el.dataset.settingsSection;
      return SETTINGS_SECTION_LABELS[sec] || sec;
    }
    const titleEl = document.querySelector(".workspace-title .title");
    return titleEl ? titleEl.textContent.trim() : "";
  }
  function formatTitle(section, prefix) {
    const base = section ? section + " \xb7 serf hub" : "serf hub";
    return prefix ? prefix + base : base;
  }
  function refreshTabTitle() {
    const prefs = readNotifPrefs();
    const section = pageSection();
    if (!prefs.title) {
      document.title = formatTitle(section);
      return;
    }
    const searchPromise = window.SerfAppwire
      ? window.SerfAppwire.search("")
      : fetch("/api/search?q=").then(r => r.json());
    searchPromise.then(resp => {
      const awaiting = (resp.live || []).filter(s => s.state === "awaiting").length;
      const prefix = awaiting > 0 ? "(" + awaiting + ") " : "";
      document.title = formatTitle(pageSection(), prefix);
    }).catch(() => { document.title = formatTitle(pageSection()); });
  }
  function readNotifPrefs() {
    try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
    catch (e) { return {}; }
  }

  // Tool-call expand/collapse caret. Delegated on document so it fires even
  // after htmx swaps the conversation element.
  document.addEventListener("click", (e) => {
    const btn = e.target && (e.target.matches("[data-expand-toggle]") ? e.target : (e.target.closest && e.target.closest("[data-expand-toggle]")));
    if (!btn) return;
    e.preventDefault();
    const toolCall = btn.closest(".tool-call");
    if (!toolCall) return;
    const expanded = toolCall.dataset.expanded === "true";
    const next = !expanded;
    toolCall.dataset.expanded = next ? "true" : "false";
    btn.textContent = next ? "▾" : "▸";
    btn.setAttribute("aria-label", next ? "collapse tool details" : "expand tool details");
    btn.setAttribute("aria-expanded", next ? "true" : "false");
  });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const btn = e.target && e.target.matches && e.target.matches("[data-expand-toggle]") ? e.target : null;
    if (!btn) return;
    e.preventDefault();
    btn.click();
  });

  // Copy-session-ID button.
  document.body && document.body.addEventListener("click", (e) => {
    const t = e.target;
    if (!t || !t.matches) return;
    if (t.matches("[data-copy-id]")) {
      e.preventDefault();
      const id = t.getAttribute("data-copy-id");
      if (id && navigator.clipboard) {
        navigator.clipboard.writeText(id).then(() => {
          if (window.SerfToast) window.SerfToast.show("Session ID copied", "success");
        }, () => {
          if (window.SerfToast) window.SerfToast.show("Copy failed — clipboard blocked", "error");
        });
      }
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
      e.preventDefault();
      var detailsTrigger = t.matches("[data-details-trigger]") ? t : t.closest("[data-details-trigger]");
      toggleDetailsPanel(detailsTrigger);
    } else if (t.matches("[data-tasks-trigger]") || t.closest && t.closest("[data-tasks-trigger]")) {
      e.preventDefault();
      var tasksTrigger = t.matches("[data-tasks-trigger]") ? t : t.closest("[data-tasks-trigger]");
      toggleTasksPanel(tasksTrigger);
    } else {
      const actionBtn = t.matches("[data-action-trigger]") ? t : (t.closest && t.closest("[data-action-trigger]"));
      if (actionBtn) {
        e.preventDefault();
        if (actionBtn.disabled) return;
        triggerSessionAction(actionBtn.getAttribute("data-action-trigger"));
      }
    }
  });

  // triggerSessionAction sends the app-wire action for the currently-rendered
  // workspace session. Actions are silent on success and surface failures via
  // appendBanner.
  function triggerSessionAction(action) {
    const conv = document.getElementById("conversation");
    const sessionId = conv && conv.getAttribute("data-session-id");
    if (!sessionId) return;
    const turnId = (window.SerfRenderer && window.SerfRenderer.activeTurnId) || (conv && conv.getAttribute("data-active-turn-id")) || "";
    const actionPromise = window.SerfAppwire
      ? window.SerfAppwire.action(sessionId, action, turnId).then(() => ({ ok: true, text: () => Promise.resolve("") }))
      : fetch("/s/" + encodeURIComponent(sessionId) + "/" + action, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // REST shim is snake_case; the appwire path above keeps the
        // protocol's camelCase `turnId`.
        body: action === "interrupt" ? JSON.stringify({ turn_id: turnId }) : undefined,
      });
    actionPromise
      .then((resp) => {
        if (!resp.ok) {
          return resp.text().then((txt) => {
            if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
              window.SerfRenderer.appendBanner("error", action + " failed: " + (txt || resp.status), { source: "hub", title: "Hub action error" });
            }
          });
        }
      })
      .catch((err) => {
        if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
          window.SerfRenderer.appendBanner("error", action + " failed: " + err.message, { source: "hub", title: "Hub action error" });
        }
      });
  }

  function setPanelToggleActive(selector, active) {
    const btn = document.querySelector(selector);
    if (!btn) return;
    if (active) {
      btn.setAttribute("data-active", "");
    } else {
      btn.removeAttribute("data-active");
    }
  }

  // addPanelScrim inserts a semi-transparent overlay behind the slide-over panel.
  // onClose is called when the user clicks the scrim directly (click-outside
  // via the scrim element rather than bindClickOutside).
  function addPanelScrim(onClose) {
    removePanelScrim();
    const scrim = document.createElement("div");
    scrim.className = "panel-scrim";
    scrim.id = "panel-scrim";
    scrim.addEventListener("mousedown", (ev) => {
      ev.stopPropagation();
      onClose();
    });
    document.body.appendChild(scrim);
  }
  function removePanelScrim() {
    const s = document.getElementById("panel-scrim");
    if (s) s.remove();
  }

  // bindClickOutside dismisses a slide-over panel when the user clicks
  // anywhere outside it AND outside the trigger button that opened it.
  // Capture-phase mousedown so a click that would otherwise land on a
  // different control still closes the panel first. The handler self-removes
  // when (a) it dismisses the panel or (b) it sees the panel was already
  // detached (e.g. because the OTHER panel was opened, swapping it out).
  function bindClickOutside(panel, triggerSelector, closeFn) {
    const onDown = (ev) => {
      if (!panel.parentNode) {
        document.removeEventListener("mousedown", onDown, true);
        return;
      }
      if (panel.contains(ev.target)) return;
      if (ev.target.closest && ev.target.closest(triggerSelector)) return;
      // Do not close the panel when the click is inside the search dialog.
      if (ev.target.closest && ev.target.closest("#search-dialog")) return;
      const dlg = document.getElementById("search-dialog");
      if (dlg && dlg.open) return;
      closeFn();
      document.removeEventListener("mousedown", onDown, true);
    };
    document.addEventListener("mousedown", onDown, true);
  }

  function toggleTasksPanel(trigger) {
    const existing = document.getElementById("tasks-panel");
    if (existing) {
      if (existing.__pollTimer) clearInterval(existing.__pollTimer);
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      removePanelScrim();
      setPanelToggleActive("[data-tasks-trigger]", false);
      return;
    }
    // Close details panel if open — they share the same slot.
    const details = document.getElementById("details-panel");
    if (details) {
      if (details.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(details.__trapHandle);
      }
      details.remove();
    }
    setPanelToggleActive("[data-details-trigger]", false);

    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-tasks-trigger]");

    const panel = document.createElement("aside");
    panel.id = "tasks-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);
    addPanelScrim(closePanel);

    const refresh = () => {
      const tasksPromise = window.SerfAppwire
        ? window.SerfAppwire.tasks(id)
        : partialFetch(sessionPartialPath(id, "tasks")).then(r => r.json());
      tasksPromise.then(tasks => {
        renderTasksInto(panel, tasks);
      }).catch(() => {
        panel.innerHTML = "<div class='details-loading'>failed to load</div>";
      });
    };
    refresh();
    panel.__pollTimer = setInterval(refresh, 2000);
    setPanelToggleActive("[data-tasks-trigger]", true);

    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    function closePanel() {
      if (panel.__pollTimer) clearInterval(panel.__pollTimer);
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
      panel.remove();
      removePanelScrim();
      setPanelToggleActive("[data-tasks-trigger]", false);
    }
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        // Do not close the panel if the event originated inside the search dialog.
        if (ev.target && ev.target.closest && ev.target.closest("#search-dialog")) return;
        const dlg = document.getElementById("search-dialog");
        if (dlg && dlg.open) return;
        closePanel();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-tasks-trigger]", closePanel);
  }

  function renderTasksInto(panel, tasks) {
    // Cache descriptions for inline rendering (system-line uses this).
    if (Array.isArray(tasks)) {
      for (const t of tasks) {
        if (t && t.id != null && t.description) {
          rememberTask(t);
        }
      }
    }

    const total = tasks.length;
    const done = tasks.filter(t => t.status === "done").length;
    const inProg = tasks.filter(t => t.status === "in_progress").length;
    const open = tasks.filter(t => t.status === "open").length;
    const cancelled = tasks.filter(t => t.status === "cancelled").length;

    // Update the tasks-button progress/status text as a side-effect — visible
    // without opening the panel.
    updateTasksBadge(done, total, currentTaskSummary(tasks));

    const parts = [];
    parts.push("<header class='details-panel-header'>");
    parts.push("<span>tasks · " + done + "/" + total + "</span>");
    parts.push("<button class='details-panel-close' aria-label='close panel' onclick=\"document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))\">✕</button>");
    parts.push("</header>");

    if (total === 0) {
      parts.push("<div class='empty-state empty-state-tasks'><p class='empty-state-title'>No tasks yet</p><p class='empty-state-body'>The agent's task list is empty for this session.</p></div>");
      panel.innerHTML = parts.join("");
      return;
    }

    const counts = [];
    if (inProg) counts.push(inProg + " in progress");
    if (open) counts.push(open + " open");
    if (done) counts.push(done + " done");
    if (cancelled) counts.push(cancelled + " cancelled");
    parts.push("<div class='tasks-summary'>" + counts.join(" · ") + "</div>");
    parts.push("<ul class='tasks-list'></ul>");
    panel.innerHTML = parts.join("");

    // The living list shares the task-update card's row grammar (glyph · step ·
    // checked-off time, with done receding / current breathing) via the shared
    // buildTaskRowLine widget, then wraps each row in an expandable disclosure
    // carrying the full task fields.
    const ul = panel.querySelector(".tasks-list");
    for (const t of tasks) {
      const li = document.createElement("li");
      li.className = "task-row";
      const details = document.createElement("details");
      details.className = "task-row-details";
      const summary = document.createElement("summary");
      summary.appendChild(buildTaskRowLine(t));
      const chevron = document.createElement("span");
      chevron.className = "task-row-chevron";
      chevron.setAttribute("aria-hidden", "true");
      chevron.textContent = "›";
      summary.appendChild(chevron);
      details.appendChild(summary);
      details.appendChild(buildTaskDetailList(t));
      li.appendChild(details);
      ul.appendChild(li);
    }
  }

  function currentTaskSummary(tasks) {
    if (!Array.isArray(tasks) || tasks.length === 0) return "";
    const current = tasks.find(t => t && t.status === "in_progress") || tasks.find(t => t && t.status === "open") || null;
    if (!current) return "all tasks complete";
    const desc = String(current.description || "task " + (current.id || "")).trim();
    return desc ? clip(desc, 120) : "task " + (current.id || "");
  }

  function updateTasksBadge(done, total, currentText) {
    const triggers = Array.from(document.querySelectorAll("[data-tasks-trigger]"));
    if (!triggers.length) return;
    for (const btn of triggers) {
      let badge = btn.querySelector(".panel-toggle-badge");
      if (total === 0) {
        if (badge) badge.remove();
      } else {
        if (!badge) {
          badge = document.createElement("span");
          badge.className = "panel-toggle-badge";
          btn.appendChild(badge);
        }
        badge.textContent = done + "/" + total;
      }
      const textEl = btn.querySelector("[data-task-status-text]");
      if (textEl) {
        if (total === 0) textEl.textContent = "no tasks yet";
        else textEl.textContent = done + "/" + total + (currentText ? " · " + currentText : "");
      }
    }
  }

  // buildTaskDetailList builds the <dl> of full task fields revealed when a
  // sidebar row expands: type · status · depends on · reasoning · prompt ·
  // notes. Built with textContent (no manual escaping needed).
  function buildTaskDetailList(t) {
    const dl = document.createElement("dl");
    dl.className = "task-detail";
    const dt = (label) => { const e = document.createElement("dt"); e.textContent = label; return e; };
    const ddText = (text, cls) => {
      const e = document.createElement("dd");
      if (cls) e.className = cls;
      e.textContent = text;
      return e;
    };
    if (t.type) {
      const pill = document.createElement("span");
      pill.className = "task-type-pill";
      pill.textContent = t.type;
      const dd = document.createElement("dd");
      dd.appendChild(pill);
      dl.append(dt("type"), dd);
    }
    if (t.status) dl.append(dt("status"), ddText(t.status));
    if (Array.isArray(t.depends_on) && t.depends_on.length > 0) {
      dl.append(dt("depends on"), ddText(t.depends_on.map(x => "#" + x).join(", ")));
    }
    if (t.reasoning_effort) dl.append(dt("reasoning"), ddText(t.reasoning_effort));
    if (t.prompt) dl.append(dt("prompt"), ddText(t.prompt, "task-prompt"));
    if (Array.isArray(t.notes) && t.notes.length > 0) {
      const ol = document.createElement("ol");
      ol.className = "task-notes-list";
      t.notes.forEach((n, i) => {
        const li = document.createElement("li");
        li.className = "task-note";
        const num = document.createElement("span");
        num.className = "task-note-num";
        num.textContent = String(i + 1);
        const txt = document.createElement("span");
        txt.className = "task-note-text";
        txt.textContent = n;
        li.append(num, txt);
        ol.appendChild(li);
      });
      const dd = document.createElement("dd");
      dd.appendChild(ol);
      dl.append(dt("notes"), dd);
    }
    return dl;
  }

  function toggleDetailsPanel(trigger) {
    const existing = document.getElementById("details-panel");
    if (existing) {
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      removePanelScrim();
      setPanelToggleActive("[data-details-trigger]", false);
      return;
    }
    // Close tasks panel if open — they share the same slot.
    const tasks = document.getElementById("tasks-panel");
    if (tasks) {
      if (tasks.__pollTimer) clearInterval(tasks.__pollTimer);
      if (tasks.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(tasks.__trapHandle);
      }
      tasks.remove();
      removePanelScrim();
      setPanelToggleActive("[data-tasks-trigger]", false);
    }
    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-details-trigger]");

    const panel = document.createElement("aside");
    panel.id = "details-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);
    addPanelScrim(closePanel);
    partialFetch(sessionPartialPath(id, "details")).then(r => r.text()).then(html => {
      panel.innerHTML = html;
      if (window.SerfFocusTrap && !panel.__trapHandle) {
        // Re-activate now that the panel has real focusable children.
        panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
      }
    }).catch(() => { panel.innerHTML = "<div class='details-loading'>failed to load</div>"; });
    setPanelToggleActive("[data-details-trigger]", true);

    // Initial activation (may have no focusable children until fetch resolves;
    // helper falls back to focusing the panel itself).
    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    function closePanel() {
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
      panel.remove();
      removePanelScrim();
      setPanelToggleActive("[data-details-trigger]", false);
    }
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        // Do not close the panel if the event originated inside the search dialog.
        if (ev.target && ev.target.closest && ev.target.closest("#search-dialog")) return;
        const dlg = document.getElementById("search-dialog");
        if (dlg && dlg.open) return;
        closePanel();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-details-trigger]", closePanel);
  }

  document.addEventListener("DOMContentLoaded", refreshTabTitle);
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", refreshTabTitle);
  });
  if (document.body) document.body.addEventListener("htmx:afterSwap", refreshTabTitle);

  window.SerfRendererInternal = Object.assign(window.SerfRendererInternal || {}, {
    toggleTasksPanel,
    currentTaskSummary,
    updateTasksBadge,
  });
})();
