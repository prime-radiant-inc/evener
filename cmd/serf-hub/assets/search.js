(function () {
  "use strict";

  let dialog, input, results, items = [], active = -1;

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
    fetch("/api/search?q=" + encodeURIComponent(q))
      .then(r => r.json())
      .then(render)
      .catch(() => { results.innerHTML = '<div class="search-empty">search failed</div>'; });
  }

  function render(resp) {
    items = [];
    let html = "";
    if (resp.live && resp.live.length) {
      html += '<div class="search-section-header">Live</div>';
      resp.live.forEach(r => {
        const idx = items.length;
        items.push(r);
        html += renderRow(r, idx);
      });
    }
    if (resp.past && resp.past.length) {
      html += '<div class="search-section-header">Past &middot; ' + resp.past.length + '</div>';
      resp.past.forEach(r => {
        const idx = items.length;
        items.push(r);
        html += renderRow(r, idx);
      });
    }
    if (items.length === 0) {
      html += '<div class="search-empty">no matches</div>';
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

  function renderRow(r, idx) {
    return '<a class="search-row" data-idx="' + idx + '">' +
           '<span class="status-dot" data-state="' + escapeHtml(r.state || "ended") + '"></span>' +
           '<span class="search-title">' + escapeHtml(r.title) + '</span>' +
           '<span class="search-project">' + escapeHtml(r.project || "") + '</span>' +
           '<span class="search-age">' + escapeHtml(r.age || "") + '</span>' +
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
      if (i === active) el.scrollIntoView({ block: "nearest" });
    });
  }
  function openActive() {
    if (active < 0 || !items[active]) return;
    close();
    window.location.href = "/s/" + encodeURIComponent(items[active].id);
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
