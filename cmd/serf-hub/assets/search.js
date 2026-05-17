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
  let preArgsFilter = "/";       // input value before entering args mode; restored on back-out
  const recentCommandsKey = "serf.search.recentCommands";
  const recentCommandsLimit = 5;

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
    const turnId = activeTurnId();
    if (action === "interrupt" && !turnId) {
      showTurnActionUnavailable("interrupt failed: no active turn");
      return Promise.resolve();
    }
    if (window.SerfAppwire) return window.SerfAppwire.action(ctx.sessionId, action, turnId);
    return fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/" + action, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // REST shim uses snake_case; the appwire path above keeps the
      // protocol's camelCase `turnId`.
      body: action === "interrupt" ? JSON.stringify({ turn_id: turnId }) : undefined,
    });
  }

  function activeTurnId() {
    const conv = document.getElementById("conversation");
    return (window.SerfRenderer && window.SerfRenderer.activeTurnId) || (conv && conv.getAttribute("data-active-turn-id")) || "";
  }

  function showTurnActionUnavailable(message) {
    if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
      window.SerfRenderer.appendBanner("error", message, { source: "hub", title: "Hub action error" });
    }
  }

  function fetchModels() {
    // /api/models returns an array of { provider, model, display_name, ... }.
    // The session /model POST mirrors the spawn picker convention by sending
    // "provider/model" so the daemon can route across providers.
    const modelsPromise = window.SerfAppwire
      ? window.SerfAppwire.listModels()
      : fetch("/api/models").then(r => r.ok ? r.json() : []);
    return modelsPromise.then(list => {
      if (!Array.isArray(list)) return [];
      return list.map(m => ({
        id: m.provider + "/" + m.model,
        label: m.display_name || m.model,
        hint: m.provider,
      }));
    }).catch(() => []);
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
      { id: "spawn", title: "Spawn with prompt", hint: "new session, prefilled", keywords: ["start"], scope: "global",
        args: { kind: "free", placeholder: "prompt to spawn…",
          run: (_ctx, text) => { Nav.go("/new?prompt=" + encodeURIComponent(text || "")); } } },
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
      { id: "help", title: "Show keyboard shortcuts", hint: "TUI parity reference", keywords: ["?", "keys", "shortcuts"], scope: "global",
        stayOpen: true,
        run: () => { renderHelpPanel(); input.focus(); } },

      // session (live only)
      { id: "compact", title: "Compact transcript", hint: "free up token space", keywords: ["compress"], scope: "session",
        run: (ctx) => postSession(ctx, "compact") },
      { id: "interrupt", title: "Interrupt agent", hint: "cancel in-flight turn", keywords: ["cancel", "stop"], scope: "session",
        run: (ctx) => postSession(ctx, "interrupt") },
      { id: "clear", title: "Clear context", hint: "start fresh in this session", keywords: [], scope: "session",
        run: (ctx) => postSession(ctx, "clear") },
      { id: "shutdown", title: "Shut down daemon", hint: "ends this session", keywords: ["kill"], scope: "session",
        run: (ctx) => postSession(ctx, "shutdown") },
      { id: "model", title: "Switch model", hint: "", keywords: [], scope: "session",
        args: { kind: "enum", placeholder: "choose a model…",
          source: () => fetchModels(),
          run: (ctx, item) => window.SerfAppwire ? window.SerfAppwire.setModel(ctx.sessionId, item.id) : fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/model", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ model: item.id }),
          }) } },
      { id: "steer", title: "Steer model", hint: "inject mid-turn", keywords: [], scope: "session",
        args: { kind: "free", placeholder: "steer text…",
          run: (ctx, text) => {
            const turnId = activeTurnId();
            if (!turnId) {
              showTurnActionUnavailable("steer failed: no active turn");
              return Promise.resolve();
            }
            return window.SerfAppwire ? window.SerfAppwire.steer(ctx.sessionId, turnId, text) : fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/steer", {
            method: "POST", headers: { "Content-Type": "application/json" },
            // REST shim uses snake_case; appwire path above keeps `turnId`.
            body: JSON.stringify({ text: text, turn_id: turnId }),
          });
          } } },
      // /fork omitted: fork requires an edited message and the palette has
      // no way to gather one. Use the "edit" affordance on the user-message
      // row in the transcript instead. See kata #34.

      // session info (live or ended)
      { id: "copy-id", title: "Copy session ID", hint: "clipboard", keywords: ["clipboard"], scope: "ended-ok",
        run: (ctx) => {
          if (!ctx.sessionId) return;
          copyToClipboard(ctx.sessionId).catch(() => {
            if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
              window.SerfRenderer.appendBanner("error", "couldn't copy to clipboard", { source: "ui", title: "UI error" });
            }
          });
        } },
      { id: "tasks", title: "Toggle tasks panel", hint: "", keywords: [], scope: "ended-ok",
        run: () => { const btn = document.querySelector("[data-tasks-trigger]"); if (btn) btn.click(); } },
      { id: "status", title: "Toggle session details", hint: "", keywords: ["details", "info"], scope: "ended-ok",
        run: () => { const btn = document.querySelector("[data-details-trigger]"); if (btn) btn.click(); } },
      { id: "project", title: "Reveal session's project in sidebar", hint: "scroll sidebar", keywords: ["folder"], scope: "ended-ok",
        run: (ctx) => revealProject(ctx) },
    ];
  }

  // copyToClipboard prefers the async Clipboard API but falls back to the
  // legacy execCommand path when the page isn't a secure context (HTTP-only
  // deployments, file://, etc.). Returns a Promise<boolean>; rejects only if
  // both paths fail outright.
  function copyToClipboard(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).catch(() => execCopyFallback(text));
    }
    return execCopyFallback(text);
  }

  function execCopyFallback(text) {
    return new Promise((resolve, reject) => {
      try {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.setAttribute("readonly", "");
        ta.style.position = "absolute";
        ta.style.left = "-9999px";
        document.body.appendChild(ta);
        ta.select();
        const ok = document.execCommand && document.execCommand("copy");
        document.body.removeChild(ta);
        if (ok) resolve(true); else reject(new Error("execCommand copy returned false"));
      } catch (e) {
        reject(e);
      }
    });
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

  function commandScore(command, q) {
    if (!q) return 0;
    const fields = [command.id, command.title].concat(command.keywords || []);
    let best = -1;
    for (const raw of fields) {
      const text = String(raw || "").toLowerCase();
      const exact = text.indexOf(q);
      if (exact >= 0) best = Math.max(best, 200 - exact);
      best = Math.max(best, fuzzyScore(q, text));
    }
    return best;
  }

  function fuzzyScore(needle, haystack) {
    if (!needle) return 0;
    let score = 0;
    let pos = -1;
    let streak = 0;
    for (const ch of needle) {
      const next = haystack.indexOf(ch, pos + 1);
      if (next < 0) return -1;
      streak = next === pos + 1 ? streak + 1 : 0;
      const boundary = next === 0 || /[\s/_-]/.test(haystack.charAt(next - 1));
      score += 10 + (boundary ? 8 : 0) + (streak * 4) - Math.min(next - pos - 1, 8);
      pos = next;
    }
    return score;
  }

  function readRecentCommandIDs() {
    try {
      const raw = window.localStorage && window.localStorage.getItem(recentCommandsKey);
      const parsed = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed) ? parsed.filter(id => typeof id === "string") : [];
    } catch (_) {
      return [];
    }
  }

  function rememberCommand(id) {
    if (!id || !window.localStorage) return;
    const next = [id].concat(readRecentCommandIDs().filter(existing => existing !== id)).slice(0, recentCommandsLimit);
    try { window.localStorage.setItem(recentCommandsKey, JSON.stringify(next)); } catch (_) {}
  }

  // -------- command-filter rendering --------

  function renderCommands(query) {
    const ctx = buildCtx();
    const q = (query || "").replace(/^\//, "").toLowerCase().trim();
    const scoped = commandsInScope(ctx);
    const recentCommands = !q
      ? readRecentCommandIDs().map(id => scoped.find(c => c.id === id)).filter(Boolean)
      : [];
    const recentSet = new Set(recentCommands.map(c => c.id));
    const visible = q
      ? scoped.map((c, idx) => ({ command: c, idx: idx, score: commandScore(c, q) }))
          .filter(row => row.score >= 0)
          .sort((a, b) => (b.score - a.score) || (a.idx - b.idx))
          .map(row => row.command)
      : scoped.filter(c => !recentSet.has(c.id));
    items = [];

    let html = "";
    if (recentCommands.length) {
      html += '<div class="search-section-header">Recent</div>';
      recentCommands.forEach(c => {
        const idx = items.length;
        items.push({ kind: "command", command: c });
        html += renderCommandRow(c, idx);
      });
    }
    html += '<div class="search-section-header">Commands</div>';
    visible.forEach(c => {
      const idx = items.length;
      items.push({ kind: "command", command: c });
      html += renderCommandRow(c, idx);
    });
    if (!items.length) {
      html += '<div class="search-empty">no commands match.</div>';
    }
    active = items.length > 0 ? 0 : -1;
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
    return '<a class="search-row search-row-command" role="option" id="search-row-' + idx + '" data-idx="' + idx + '">' +
           '<span class="search-cmd-glyph" aria-hidden="true">/</span>' +
           '<span class="search-title">' + escapeHtml(c.title) + '</span>' +
           '<span class="search-cmd-id">/' + escapeHtml(c.id) + '</span>' +
           '<span class="search-cmd-hint">' + escapeHtml(c.hint || "") + '</span>' +
           '</a>';
  }

  // renderHelpPanel paints a keyboard-shortcut reference into the results
  // pane and clears the active item list so Enter is a no-op. Typing
  // anything new fires the input event and restores normal filtering.
  function renderHelpPanel() {
    items = [];
    active = -1;
    const rows = [
      ["⌘K / Ctrl-K", "open the palette from anywhere"],
      ["/", "at the start of an empty message textarea — opens command mode"],
      ["↑ ↓", "navigate the list"],
      ["↵", "run the highlighted command (or open a search result)"],
      ["⌘↵", "open a search result in a new tab"],
      ["⇧↵", "jump to a turn in the current session"],
      ["Esc", "close the palette (or back out of args mode)"],
    ];
    let html = '<div class="search-section-header">Keyboard shortcuts</div>';
    rows.forEach(([keys, desc]) => {
      html += '<div class="search-help-row">' +
              '<span class="search-help-keys">' + escapeHtml(keys) + '</span>' +
              '<span class="search-help-desc">' + escapeHtml(desc) + '</span>' +
              '</div>';
    });
    results.innerHTML = html;
  }

  // -------- command-args mode --------

  function enterArgsMode(cmd) {
    // Remember the filter that got us here so Esc back-out doesn't reset
    // the user's typing. Default to "/" so an empty filter still drops
    // them in command-filter mode rather than empty search mode.
    preArgsFilter = input.value && input.value.startsWith("/") ? input.value : "/";
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
    input.value = preArgsFilter || "/";
    input.placeholder = "search live + past sessions";
    input.focus();
    renderCommands(input.value);
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
    return '<a class="search-row search-row-argitem" role="option" id="search-row-' + idx + '" data-idx="' + idx + '">' +
           '<span class="search-title">' + escapeHtml(it.label || it.id) + '</span>' +
           '<span class="search-cmd-hint">' + escapeHtml(it.hint || "") + '</span>' +
           '</a>';
  }

  // -------- command execution --------

  function runArgless(cmd) {
    const ctx = buildCtx();
    try { cmd.run(ctx); } catch (_) {}
    if (!cmd.stayOpen) rememberCommand(cmd.id);
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
    const searchPromise = window.SerfAppwire
      ? window.SerfAppwire.search(query)
      : fetch("/api/search?q=" + encodeURIComponent(query)).then(r => r.json());
    searchPromise
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
    return '<a class="search-row" role="option" id="search-row-' + idx + '" data-idx="' + idx + '">' +
           '<span class="status-dot" data-state="' + escapeHtml(r.state || "ended") + '"></span>' +
           '<span class="search-title">' + highlight(r.title || "", query) + '</span>' +
           '<span class="search-project">' + highlight(r.project || "", query) + '</span>' +
           '<span class="search-age">' + escapeHtml(r.age || "") + '</span>' +
           '</a>';
  }

  function renderInSessionRow(r, idx) {
    return '<a class="search-row search-row-insession" role="option" id="search-row-' + idx + '" data-idx="' + idx + '">' +
           '<span class="search-insession-glyph" aria-hidden="true">↳</span>' +
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
    let activeId = "";
    results.querySelectorAll(".search-row").forEach((el, i) => {
      const isActive = i === active;
      el.classList.toggle("active", isActive);
      el.setAttribute("aria-selected", isActive ? "true" : "false");
      if (isActive) {
        activeId = el.id || "";
        if (typeof el.scrollIntoView === "function") {
          el.scrollIntoView({ block: "nearest" });
        }
      }
    });
    if (input) {
      if (activeId) input.setAttribute("aria-activedescendant", activeId);
      else input.removeAttribute("aria-activedescendant");
    }
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
