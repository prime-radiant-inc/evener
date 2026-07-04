// Sidebar: client-rendered navigation tree. Data (/api/tree) is the state; the
// DOM is a keyed projection reconciled on the server's scope-qualified RowID, so
// unchanged rows keep node identity (hover, scroll, open menus) across updates.
(function () {
  "use strict";

  var EXPAND_PREFIX = "serf-hub.sidebar.expanded.";
  var model = { tree: null, expanded: new Set(), lazyCache: new Map(), seq: 0, pending: new Map() };
  window.SerfSidebarModel = model; // test/inspection surface

  function sidebarEl() { return document.getElementById("sidebar"); }

  // --- Skeleton first paint --------------------------------------------------
  function paintSkeleton() {
    var el = sidebarEl();
    if (!el || el.querySelector(".sb-tree")) return;
    var html = '<div class="sb-skeleton" aria-hidden="true">';
    for (var i = 0; i < 6; i++) html += '<div class="sb-skeleton-row"></div>';
    html += "</div>";
    el.innerHTML = html;
  }

  // --- Row + section builders ------------------------------------------------
  function rowKey(n) { return n.row_id; }

  function buildRow(n) {
    var a = document.createElement("a");
    a.className = "sb-row";
    a.setAttribute("data-row-id", n.row_id);
    a.setAttribute("data-state", n.state);
    a.setAttribute("data-ref", n.ref);
    a.setAttribute("href", "/s/" + n.session_id);
    a.setAttribute("hx-get", "/_partials/s/" + n.session_id + "/workspace");
    a.setAttribute("hx-target", "#workspace");
    a.setAttribute("hx-swap", "innerHTML");
    a.setAttribute("hx-push-url", "/s/" + n.session_id);
    a.innerHTML =
      '<div class="dot-col"><span class="status-dot" data-state="' + n.state + '"></span></div>' +
      '<div class="text-col"><div class="title"></div><div class="meta"></div></div>';
    a.querySelector(".title").textContent = n.title;
    var meta = a.querySelector(".meta");
    if (n.branch) meta.appendChild(metaSpan(n.branch));
    meta.appendChild(metaSpan(ageString(n.updated_at)));
    if (n.favorite) a.setAttribute("data-favorite", "");
    // Task 21 attaches the ⋯ menu button here.
    return a;
  }
  function metaSpan(text) { var s = document.createElement("span"); s.textContent = text; return s; }

  function patchRow(a, n) {
    if (a.getAttribute("data-state") !== n.state) {
      a.setAttribute("data-state", n.state);
      var dot = a.querySelector(".status-dot");
      if (dot) dot.setAttribute("data-state", n.state);
    }
    var title = a.querySelector(".title");
    if (title && title.textContent !== n.title) title.textContent = n.title;
    if (n.favorite) a.setAttribute("data-favorite", ""); else a.removeAttribute("data-favorite");
  }

  // --- Keyed reconcile -------------------------------------------------------
  // Patch children of `container` to match `nodes` (an array of TreeNodes),
  // keyed by row_id. Existing keys are patched in place and reordered; missing
  // keys are removed; new keys are created + htmx.process'd.
  function reconcile(container, nodes, build, patch) {
    var existing = {};
    var kids = container.children;
    for (var i = 0; i < kids.length; i++) {
      var k = kids[i].getAttribute("data-row-id");
      if (k) existing[k] = kids[i];
    }
    var seen = {};
    var prev = null;
    for (var j = 0; j < nodes.length; j++) {
      var n = nodes[j];
      var key = rowKey(n);
      seen[key] = true;
      var el = existing[key];
      if (el) { patch(el, n); } else { el = build(n); if (window.htmx && window.htmx.process) window.htmx.process(el); }
      // Place el right after prev (stable order without destroying nodes).
      var ref = prev ? prev.nextSibling : container.firstChild;
      if (el !== ref) container.insertBefore(el, ref);
      prev = el;
    }
    for (var m = container.children.length - 1; m >= 0; m--) {
      var ck = container.children[m].getAttribute("data-row-id");
      if (ck && !seen[ck]) container.removeChild(container.children[m]);
    }
  }

  // --- Full tree render ------------------------------------------------------
  function renderTree(tree) {
    model.tree = tree;
    var el = sidebarEl();
    if (!el) return;
    var root = el.querySelector(".sb-tree");
    if (!root) { el.innerHTML = '<nav class="sb-tree" aria-label="Sessions"></nav>'; root = el.querySelector(".sb-tree"); }
    var flat = flatten(applyPending(tree)); // applyPending is Task 20; identity until then
    reconcile(root, flat, buildRowOrSection, patchRowOrSection);
    syncActiveRow();
  }

  // flatten emits one keyed element descriptor per rendered row/section in
  // order: NeedsYou, Pinned, active projects, archived projects, test runs.
  // For the core task, render session rows grouped under project section
  // headers; expansion + lazy children are honored via model.expanded.
  function flatten(tree) {
    var out = [];
    (tree.needs_you || []).forEach(function (n) { out.push(n); });
    (tree.favorites || []).forEach(function (n) { out.push(n); });
    (tree.projects || []).forEach(function (p) { pushProject(out, p); });
    // archived_projects + test_runs handled by Task 21/22 section wrappers.
    return out;
  }
  function pushProject(out, p) {
    // Project header is itself a keyed element (data-row-id="header:<key>").
    out.push({ row_id: "header:" + p.key, __project: p });
    var expanded = model.expanded.has(p.key) || p.default_expanded;
    if (expanded) (p.sessions || []).forEach(function (n) { out.push(n); });
  }

  function buildRowOrSection(n) { return n.__project ? buildProjectHeader(n.__project) : buildRow(n); }
  function patchRowOrSection(el, n) { if (n.__project) patchProjectHeader(el, n.__project); else patchRow(el, n); }

  function buildProjectHeader(p) {
    var d = document.createElement("div");
    // NOTE: intentionally NOT "sb-row" — a project header is not a session
    // row, and the existing template/CSS (partials/sidebar.html, style.css)
    // already style ".project-header" as its own standalone class.
    d.className = "project-header";
    d.setAttribute("data-row-id", "header:" + p.key);
    d.setAttribute("data-project-key", p.key);
    d.setAttribute("role", "button");
    d.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded));
    d.innerHTML = '<span class="project-name"></span><span class="project-rollup"></span>';
    d.querySelector(".project-name").textContent = p.name;
    d.addEventListener("click", function () { toggleProject(p.key); });
    return d;
  }
  function patchProjectHeader(el, p) {
    el.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded));
  }

  function toggleProject(key) {
    if (model.expanded.has(key)) model.expanded.delete(key); else model.expanded.add(key);
    persistExpanded(key);
    if (model.tree) renderTree(model.tree);
  }
  function persistExpanded(key) {
    try {
      if (model.expanded.has(key)) window.localStorage.setItem(EXPAND_PREFIX + key, "true");
      else window.localStorage.removeItem(EXPAND_PREFIX + key);
    } catch (e) {}
  }

  function applyPending(tree) { return tree; } // Task 20 replaces this.

  // --- Age formatting (client-side; server ages are up to one bucket stale) --
  function ageString(iso) {
    if (!iso) return "";
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var d = (Date.now() - t) / 1000;
    if (d < 60) return "now";
    if (d < 3600) return Math.floor(d / 60) + "m";
    if (d < 86400) return Math.floor(d / 3600) + "h";
    return Math.floor(d / 86400) + "d";
  }

  // --- Active row (driven off htmx:afterSwap on #workspace) -------------------
  function syncActiveRow() {
    var path = (window.location && window.location.pathname) || "";
    var clean = path.replace(/\/+$/, "");
    var rows = document.querySelectorAll("#sidebar .sb-row[href]");
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute("href") === clean) {
        rows[i].setAttribute("data-active", "");
        var hdr = rows[i].closest ? null : null; // project auto-expand: Task 22
      } else {
        rows[i].removeAttribute("data-active");
      }
    }
  }

  // --- Fetch + lifecycle -----------------------------------------------------
  function fetchTree() {
    var mySeq = ++model.seq;
    return window.fetch("/api/tree").then(function (r) { return r.json(); }).then(function (tree) {
      if (mySeq !== model.seq) return; // sequence guard: a newer fetch won
      renderTree(tree);
      migrateExpansionKeys(); // Task 22 (no-op until then)
    }).catch(function () {});
  }
  function migrateExpansionKeys() {} // Task 22 replaces this.

  paintSkeleton();
  fetchTree();

  document.body && document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "workspace") syncActiveRow();
  });

  window.SerfSidebar = { renderTree: renderTree, refresh: fetchTree, close: function () {} };
})();
