(function () {
  "use strict";

  // The dialog hosts three modes that share the same input + results list:
  //   "search"         — query is empty or doesn't start with "/" (default)
  //   "command-filter" — query starts with "/"; filter the local registry
  //   "command-args"   — a command with args was picked; collect the arg
  // Mode is recomputed on every input event from query + selectedCommand.

  let dialog, input, results, pill, pillLabel;
  let items = [], active = -1;
  let selectedCommand = null;   // non-null only in command-args mode
  let argsEnumLoaded = false;   // tracks whether async enum source resolved

  function init() {
    dialog = document.getElementById("search-dialog");
    input = document.getElementById("search-input");
    results = document.getElementById("search-results");
    if (!dialog) return;

    installPill();

    document.addEventListener("keydown", (e) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); open(); }
      if (e.key === "Escape" && dialog.open) {
        // Esc from command-args mode goes back to filter mode, not closed.
        if (selectedCommand) {
          e.preventDefault();
          exitArgsMode();
          return;
        }
        close();
      }
    });
    document.querySelectorAll("[data-search-trigger]").forEach(el => {
      el.addEventListener("click", (e) => { e.preventDefault(); open(); });
    });

    const debouncedSearch = debounce((q) => search(q), 150);
    input.addEventListener("input", () => {
      const m = mode();
      if (m === "search") {
        debouncedSearch(input.value);
      } else if (m === "command-filter") {
        renderCommands(input.value);
      } else {
        // command-args
        if (selectedCommand.args.kind === "enum") {
          renderArgsEnum(input.value);
        } else {
          renderArgsFree(input.value);
        }
      }
    });
    input.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
      else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        // ⌘↵ is only meaningful in search mode (open in new tab).
        if (mode() === "search") { e.preventDefault(); openActiveNewTab(); }
      }
      else if (e.key === "Enter" && e.shiftKey) {
        // ⇧↵ jumps to the in-session turn; search-mode only.
        if (mode() === "search") {
          e.preventDefault();
          if (active >= 0 && items[active] && items[active].kind === "insession") {
            activateInSession(items[active]);
          }
        }
      }
      else if (e.key === "Enter") {
        e.preventDefault();
        enterPressed();
      }
    });
    dialog.addEventListener("click", (e) => {
      if (e.target === dialog) close();
    });
  }

  // installPill adds the command-args header pill into the dialog header
  // once at init. It's hidden in search and command-filter modes.
  function installPill() {
    const header = dialog.querySelector(".search-dialog-header");
    if (!header) return;
    pill = document.createElement("span");
    pill.className = "search-cmd-pill";
    pill.hidden = true;
    pillLabel = document.createElement("span");
    pillLabel.className = "search-cmd-pill-label";
    pill.appendChild(pillLabel);
    const back = document.createElement("button");
    back.type = "button";
    back.className = "search-cmd-pill-back";
    back.setAttribute("aria-label", "back to command list");
    back.textContent = "×";
    back.addEventListener("click", (e) => { e.preventDefault(); exitArgsMode(); });
    pill.appendChild(back);
    header.insertBefore(pill, input);
  }

  function mode() {
    if (selectedCommand) return "command-args";
    if ((input.value || "").startsWith("/")) return "command-filter";
    return "search";
  }

  function open() {
    selectedCommand = null;
    hidePill();
    dialog.showModal();
    input.value = "";
    input.placeholder = "search live + past sessions";
    input.focus();
    items = [];
    active = -1;
    results.innerHTML = "";
  }

  function openWith(initialQuery) {
    open();
    if (!initialQuery) return;
    input.value = initialQuery;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function close() { dialog.close(); }

  function enterPressed() {
    const m = mode();
    if (m === "search") { openActive(); return; }
    if (m === "command-filter") {
      if (active < 0 || !items[active]) return;
      const cmd = items[active].command;
      if (!cmd) return;
      if (cmd.args) { enterArgsMode(cmd); return; }
      runArgless(cmd);
      return;
    }
    // command-args
    if (selectedCommand.args.kind === "enum") {
      if (active < 0 || !items[active]) return;
      runWithArg(selectedCommand, items[active].argItem);
    } else {
      runWithArg(selectedCommand, input.value);
    }
  }

  // -------- command registry --------

  function postSession(ctx, action) {
    if (!ctx.sessionId) return Promise.resolve();
    return fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/" + action, { method: "POST" });
  }

  function fetchModels() {
    return fetch("/models").then(r => r.ok ? r.json() : { models: [] }).then(resp => {
      const list = (resp && resp.models) || [];
      return list.map(m => ({
        id: m.id,
        label: m.display_name || m.id,
        hint: m.id !== (m.display_name || m.id) ? m.id : "",
      }));
    }).catch(() => []);
  }

  function collectUserTurns() {
    const conv = document.getElementById("conversation");
    if (!conv) return [];
    const out = [];
    let turn = 0;
    conv.querySelectorAll(".user-message").forEach(el => {
      turn += 1;
      const text = (el.textContent || "").replace(/\s+/g, " ").trim();
      out.push({ id: String(turn), label: text.slice(0, 80) || ("turn " + turn), hint: "turn " + turn });
    });
    return out;
  }

  // Nav is the navigation indirection. JSDOM's Location.assign is
  // non-configurable, so production code routes navigations through this
  // mutable holder. Tests replace Nav.go to capture targets.
  const Nav = { go: (url) => { window.location.assign(url); } };

  function commands() {
    return [
      // global
      { id: "new", title: "New session", hint: "blank spawn page", keywords: [], scope: "global",
        run: () => { Nav.go("/new"); } },
      { id: "spawn", title: "Spawn with task", hint: "new session, prefilled", keywords: ["start"], scope: "global",
        args: { kind: "free", placeholder: "task to spawn…",
          run: (_ctx, text) => { Nav.go("/new?task=" + encodeURIComponent(text || "")); } } },
      { id: "settings", title: "Open settings", hint: "", keywords: ["prefs"], scope: "global",
        run: () => { Nav.go("/settings"); } },
      { id: "theme", title: "Switch theme", hint: "dark/light", keywords: [], scope: "global",
        args: { kind: "enum", placeholder: "choose a theme…",
          source: () => [{ id: "dark", label: "Dark" }, { id: "light", label: "Light" }],
          run: (_ctx, item) => { applyTheme(item.id); } } },
      { id: "dashboard", title: "Go to dashboard", hint: "", keywords: ["home"], scope: "global",
        run: () => { Nav.go("/"); } },
      { id: "search", title: "Search sessions", hint: "clear / and search", keywords: ["find"], scope: "global",
        stayOpen: true,
        run: () => { input.value = ""; input.dispatchEvent(new Event("input", { bubbles: true })); input.focus(); } },
      { id: "help", title: "Show all commands", hint: "TUI parity reference", keywords: ["?"], scope: "global",
        stayOpen: true,
        run: () => { input.value = "/"; input.dispatchEvent(new Event("input", { bubbles: true })); input.focus(); } },

      // session (live only)
      { id: "compact", title: "Compact transcript", hint: "free up token space", keywords: ["compress"], scope: "session",
        run: (ctx) => postSession(ctx, "compact") },
      { id: "interrupt", title: "Interrupt agent", hint: "cancel in-flight turn", keywords: ["cancel", "stop"], scope: "session",
        run: (ctx) => postSession(ctx, "interrupt") },
      { id: "clear", title: "Clear context", hint: "start fresh in this session", keywords: [], scope: "session",
        run: (ctx) => postSession(ctx, "clear") },
      { id: "shutdown", title: "Shut down daemon", hint: "ends this session", keywords: ["kill"], scope: "session",
        run: (ctx) => {
          if (!window.confirm("Shut down this daemon? The session will end.")) return Promise.resolve();
          return postSession(ctx, "shutdown");
        } },
      { id: "model", title: "Switch model", hint: "", keywords: [], scope: "session",
        args: { kind: "enum", placeholder: "choose a model…",
          source: () => fetchModels(),
          run: (ctx, item) => fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/model", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ model: item.id }),
          }) } },
      { id: "steer", title: "Steer model", hint: "inject mid-turn", keywords: [], scope: "session",
        args: { kind: "free", placeholder: "steer text…",
          run: (ctx, text) => fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/steer", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ text: text }),
          }) } },
      { id: "fork", title: "Fork from turn", hint: "edit and branch", keywords: ["branch"], scope: "session",
        args: { kind: "enum", placeholder: "choose a turn…",
          source: () => collectUserTurns(),
          run: (ctx, item) => fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/fork", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ turn: parseInt(item.id, 10), edited_message: "", label: "fork" }),
          }) } },

      // session info (live or ended)
      { id: "copy-id", title: "Copy session ID", hint: "clipboard", keywords: ["clipboard"], scope: "ended-ok",
        run: (ctx) => {
          if (!ctx.sessionId) return;
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(ctx.sessionId);
          }
        } },
      { id: "tasks", title: "Toggle tasks panel", hint: "", keywords: [], scope: "ended-ok",
        run: () => { const btn = document.querySelector("[data-tasks-trigger]"); if (btn) btn.click(); } },
      { id: "status", title: "Toggle session details", hint: "", keywords: ["details", "info"], scope: "ended-ok",
        run: () => { const btn = document.querySelector("[data-details-trigger]"); if (btn) btn.click(); } },
      { id: "project", title: "Reveal session's project", hint: "scroll sidebar", keywords: ["folder"], scope: "ended-ok",
        run: (ctx) => revealProject(ctx) },
    ];
  }

  function applyTheme(name) {
    document.body.classList.toggle("light-theme", name === "light");
    document.body.classList.toggle("dark-theme", name === "dark");
    try { localStorage.setItem("serf-hub.theme", name); } catch (_) {}
  }

  function revealProject(ctx) {
    if (!ctx.sessionId) return;
    const link = document.querySelector('a[href="/s/' + ctx.sessionId + '"]');
    if (!link) return;
    const section = link.closest("[data-project-key]");
    if (!section) return;
    section.classList.remove("collapsed");
    if (typeof section.scrollIntoView === "function") {
      section.scrollIntoView({ block: "center", behavior: "smooth" });
    }
  }

  function buildCtx() {
    const conv = document.getElementById("conversation");
    const sessionId = conv && conv.getAttribute("data-session-id");
    const sessionState = conv && conv.getAttribute("data-state");
    const path = window.location.pathname || "";
    let onPage = "other";
    if (sessionId) onPage = "session";
    else if (path === "/" || path === "") onPage = "home";
    else if (path.startsWith("/settings")) onPage = "settings";
    else if (path.startsWith("/new") || path.startsWith("/spawn")) onPage = "spawn";
    return { sessionId: sessionId || null, sessionState: sessionState || null, onPage: onPage };
  }

  function commandsInScope(ctx) {
    return commands().filter(c => {
      if (c.scope === "global") return true;
      if (c.scope === "ended-ok") return ctx.onPage === "session";
      // "session" — live only.
      return ctx.onPage === "session" && ctx.sessionState !== "ended";
    });
  }

  // -------- command-filter rendering --------

  function renderCommands(query) {
    const ctx = buildCtx();
    const q = (query || "").replace(/^\//, "").toLowerCase().trim();
    const visible = commandsInScope(ctx).filter(c => {
      if (!q) return true;
      if (c.id.toLowerCase().indexOf(q) >= 0) return true;
      if (c.title.toLowerCase().indexOf(q) >= 0) return true;
      if (c.keywords.some(k => k.toLowerCase().indexOf(q) >= 0)) return true;
      return false;
    });
    items = visible.map(c => ({ kind: "command", command: c }));
    active = items.length > 0 ? 0 : -1;

    let html = '<div class="search-section-header">Commands</div>';
    if (!items.length) {
      html += '<div class="search-empty">no commands match.</div>';
    } else {
      items.forEach((it, idx) => { html += renderCommandRow(it.command, idx); });
    }
    results.innerHTML = html;
    updateActive();
    results.querySelectorAll("[data-idx]").forEach(el => {
      el.addEventListener("click", () => {
        active = parseInt(el.dataset.idx, 10);
        enterPressed();
      });
    });
  }

  function renderCommandRow(c, idx) {
    return '<a class="search-row search-row-command" data-idx="' + idx + '">' +
           '<span class="search-cmd-glyph">/</span>' +
           '<span class="search-title">' + escapeHtml(c.title) + '</span>' +
           '<span class="search-cmd-id">/' + escapeHtml(c.id) + '</span>' +
           '<span class="search-cmd-hint">' + escapeHtml(c.hint || "") + '</span>' +
           '</a>';
  }

  // -------- command-args mode --------

  function enterArgsMode(cmd) {
    selectedCommand = cmd;
    argsEnumLoaded = false;
    showPill(cmd.title);
    input.value = "";
    input.placeholder = cmd.args.placeholder || "";
    input.focus();
    if (cmd.args.kind === "enum") {
      renderArgsEnum("");
    } else {
      renderArgsFree("");
    }
  }

  function exitArgsMode() {
    selectedCommand = null;
    argsEnumLoaded = false;
    hidePill();
    input.value = "/";
    input.placeholder = "search live + past sessions";
    input.focus();
    renderCommands("/");
  }

  function showPill(title) {
    if (!pill) return;
    pillLabel.textContent = title;
    pill.hidden = false;
  }
  function hidePill() {
    if (!pill) return;
    pill.hidden = true;
    pillLabel.textContent = "";
  }

  function renderArgsEnum(query) {
    const q = (query || "").toLowerCase().trim();
    const source = selectedCommand.args.source;
    const ctx = buildCtx();
    const resolved = (val) => {
      const list = Array.isArray(val) ? val : [];
      const filtered = q ? list.filter(it => (
        (it.label || "").toLowerCase().indexOf(q) >= 0 ||
        (it.id || "").toLowerCase().indexOf(q) >= 0
      )) : list;
      items = filtered.map(it => ({ kind: "arg", argItem: it }));
      active = items.length > 0 ? 0 : -1;
      let html = "";
      if (!items.length) {
        html = '<div class="search-empty">' + (argsEnumLoaded ? "no matches." : "loading…") + '</div>';
      } else {
        items.forEach((it, idx) => { html += renderArgItemRow(it.argItem, idx); });
      }
      results.innerHTML = html;
      updateActive();
      results.querySelectorAll("[data-idx]").forEach(el => {
        el.addEventListener("click", () => {
          active = parseInt(el.dataset.idx, 10);
          enterPressed();
        });
      });
    };
    let val;
    try { val = source(ctx); } catch (_) { val = []; }
    if (val && typeof val.then === "function") {
      // initial loading frame
      results.innerHTML = '<div class="search-empty">loading…</div>';
      items = [];
      active = -1;
      val.then(list => { argsEnumLoaded = true; resolved(list); })
         .catch(() => { argsEnumLoaded = true; resolved([]); });
    } else {
      argsEnumLoaded = true;
      resolved(val);
    }
  }

  function renderArgsFree(query) {
    items = [];
    active = -1;
    const hint = (query || "").trim()
      ? 'press ↵ to run with: <code>' + escapeHtml(query) + '</code>'
      : 'type a value and press ↵';
    results.innerHTML = '<div class="search-empty">' + hint + '</div>';
  }

  function renderArgItemRow(it, idx) {
    return '<a class="search-row search-row-argitem" data-idx="' + idx + '">' +
           '<span class="search-title">' + escapeHtml(it.label || it.id) + '</span>' +
           '<span class="search-cmd-hint">' + escapeHtml(it.hint || "") + '</span>' +
           '</a>';
  }

  // -------- command execution --------

  function runArgless(cmd) {
    const ctx = buildCtx();
    try { cmd.run(ctx); } catch (_) {}
    // Commands flagged stayOpen reroute the palette in-place (search/help)
    // and should keep the modal open.
    if (!cmd.stayOpen) close();
  }

  function runWithArg(cmd, arg) {
    const ctx = buildCtx();
    try { cmd.args.run(ctx, arg); } catch (_) {}
    close();
  }

  // -------- existing search-mode behavior --------

  function search(q) {
    const query = (q || "").trim();
    if (!query) {
      items = [];
      active = -1;
      results.innerHTML = "";
      return;
    }
    fetch("/api/search?q=" + encodeURIComponent(query))
      .then(r => r.json())
      .then(resp => render(resp, query))
      .catch(() => { results.innerHTML = '<div class="search-empty">search failed</div>'; });
  }

  function render(resp, query) {
    items = [];
    let html = "";
    if (resp.live && resp.live.length) {
      html += '<div class="search-section-header">Live</div>';
      resp.live.forEach(r => {
        const idx = items.length;
        items.push({ kind: "live", ...r });
        html += renderRow({ kind: "live", ...r }, idx, query);
      });
    }
    if (resp.past && resp.past.length) {
      html += '<div class="search-section-header">Past &middot; ' + resp.past.length + '</div>';
      resp.past.forEach(r => {
        const idx = items.length;
        items.push({ kind: "past", ...r });
        html += renderRow({ kind: "past", ...r }, idx, query);
      });
    }
    const inSession = findInSessionMatches(query);
    if (inSession.length) {
      html += '<div class="search-section-header">In session &middot; ' + inSession.length + '</div>';
      inSession.forEach(r => {
        const idx = items.length;
        items.push(r);
        html += renderInSessionRow(r, idx);
      });
    }
    if (items.length === 0) {
      html += '<div class="search-empty">no matches in live, past, or this session.</div>';
    }
    results.innerHTML = html;
    active = items.length > 0 ? 0 : -1;
    updateActive();
    results.querySelectorAll("[data-idx]").forEach(el => {
      el.addEventListener("click", () => {
        active = parseInt(el.dataset.idx, 10);
        openActive();
      });
    });
  }

  function findInSessionMatches(query) {
    const conv = document.getElementById("conversation");
    if (!conv || !query) return [];
    const q = query.toLowerCase();
    const nodes = conv.querySelectorAll(".user-message, .assistant-message, .system-line");
    const out = [];
    let turn = 0;
    nodes.forEach(el => {
      turn += 1;
      const text = (el.textContent || "").replace(/\s+/g, " ").trim();
      const lower = text.toLowerCase();
      const hit = lower.indexOf(q);
      if (hit < 0) return;
      out.push({
        kind: "insession",
        el: el,
        turn: turn,
        snippet: buildSnippet(text, hit, query.length),
      });
    });
    return out;
  }

  function buildSnippet(text, hit, len) {
    const ctx = 40;
    const start = Math.max(0, hit - ctx);
    const end = Math.min(text.length, hit + len + ctx);
    const before = (start > 0 ? "…" : "") + text.slice(start, hit);
    const match = text.slice(hit, hit + len);
    const after = text.slice(hit + len, end) + (end < text.length ? "…" : "");
    return escapeHtml(before) + "<mark>" + escapeHtml(match) + "</mark>" + escapeHtml(after);
  }

  function highlight(text, query) {
    if (!query) return escapeHtml(text);
    const lower = String(text).toLowerCase();
    const q = query.toLowerCase();
    const i = lower.indexOf(q);
    if (i < 0) return escapeHtml(text);
    return escapeHtml(text.slice(0, i)) +
           "<mark>" + escapeHtml(text.slice(i, i + query.length)) + "</mark>" +
           escapeHtml(text.slice(i + query.length));
  }

  function renderRow(r, idx, query) {
    return '<a class="search-row" data-idx="' + idx + '">' +
           '<span class="status-dot" data-state="' + escapeHtml(r.state || "ended") + '"></span>' +
           '<span class="search-title">' + highlight(r.title || "", query) + '</span>' +
           '<span class="search-project">' + highlight(r.project || "", query) + '</span>' +
           '<span class="search-age">' + escapeHtml(r.age || "") + '</span>' +
           '</a>';
  }

  function renderInSessionRow(r, idx) {
    return '<a class="search-row search-row-insession" data-idx="' + idx + '">' +
           '<span class="search-insession-glyph">↳</span>' +
           '<span class="search-title search-snippet">' + r.snippet + '</span>' +
           '<span class="search-age">turn ' + r.turn + '</span>' +
           '</a>';
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  function move(dir) {
    if (items.length === 0) return;
    active = (active + dir + items.length) % items.length;
    updateActive();
  }
  function updateActive() {
    results.querySelectorAll(".search-row").forEach((el, i) => {
      el.classList.toggle("active", i === active);
      if (i === active && typeof el.scrollIntoView === "function") {
        el.scrollIntoView({ block: "nearest" });
      }
    });
  }
  function openActive() {
    if (active < 0 || !items[active]) return;
    const it = items[active];
    if (it.kind === "insession") {
      activateInSession(it);
      return;
    }
    close();
    window.location.href = "/s/" + encodeURIComponent(it.id);
  }
  function openActiveNewTab() {
    if (active < 0 || !items[active]) return;
    const it = items[active];
    if (it.kind === "insession") {
      activateInSession(it);
      return;
    }
    close();
    window.open("/s/" + encodeURIComponent(it.id), "_blank");
  }

  function activateInSession(it) {
    if (!it || !it.el) return;
    close();
    try { it.el.scrollIntoView({ block: "center", behavior: "smooth" }); } catch (_) {}
    it.el.classList.add("search-hit");
    setTimeout(() => { it.el.classList.remove("search-hit"); }, 2000);
  }

  function debounce(fn, ms) {
    let t;
    return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }

  window.SerfSearch = { open: open, close: close, openWith: openWith, Nav: Nav };
})();
