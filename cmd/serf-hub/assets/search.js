(function () {
  "use strict";

  let dialog, input, results, items = [], active = -1;
  // Each entry in `items` is { kind: "live"|"past"|"insession", ...payload }
  // For "insession": { kind, el, turn, snippet }

  function init() {
    dialog = document.getElementById("search-dialog");
    input = document.getElementById("search-input");
    results = document.getElementById("search-results");
    if (!dialog) return;

    document.addEventListener("keydown", (e) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); open(); }
      if (e.key === "Escape" && dialog.open) close();
    });
    document.querySelectorAll("[data-search-trigger]").forEach(el => {
      el.addEventListener("click", (e) => { e.preventDefault(); open(); });
    });
    input.addEventListener("input", debounce(() => search(input.value), 150));
    input.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
      else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); openActiveNewTab(); }
      else if (e.key === "Enter" && e.shiftKey) {
        e.preventDefault();
        // ⇧↵: jump-to-turn for in-session results; no-op otherwise.
        if (active >= 0 && items[active] && items[active].kind === "insession") {
          activateInSession(items[active]);
        }
      }
      else if (e.key === "Enter") { e.preventDefault(); openActive(); }
    });
    dialog.addEventListener("click", (e) => {
      if (e.target === dialog) close();
    });
  }

  function open() {
    dialog.showModal();
    input.value = "";
    input.focus();
    search("");
  }
  function close() { dialog.close(); }

  function search(q) {
    const query = (q || "").trim();
    // Empty query: hide everything.
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
      // ⌘↵ on in-session is treated as plain jump.
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
})();
