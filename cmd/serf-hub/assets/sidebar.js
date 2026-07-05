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
    (tree.needs_you || []).forEach(function (n) { if (!n.__drop) out.push(n); });
    (tree.favorites || []).forEach(function (n) { if (!n.__drop) out.push(n); });
    (tree.projects || []).forEach(function (p) { pushProject(out, p); });
    // archived_projects + test_runs handled by Task 21/22 section wrappers.
    return out;
  }
  function pushProject(out, p) {
    // Project header is itself a keyed element (data-row-id="header:<key>").
    out.push({ row_id: "header:" + p.key, __project: p });
    var expanded = model.expanded.has(p.key) || p.default_expanded;
    if (expanded) (p.sessions || []).forEach(function (n) { if (!n.__drop) out.push(n); });
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

  // Pending overlay: every optimistic op is re-applied on top of every admitted
  // resync until it completes. Per-op predicates (round-2 A3): mutation-type
  // ops (favorite, rename) complete when a resync reflects the field;
  // disappearance-type ops (archive, delete) complete on POST-2xx (absence is
  // success). Eviction at 30s is a safety net only.
  function applyPending(tree) {
    if (!model.pending.size) return tree;
    var clone = JSON.parse(JSON.stringify(tree));
    model.pending.forEach(function (op) { op.apply(clone); });
    return clone;
  }

  function forEachNode(tree, fn) {
    var lists = [tree.needs_you, tree.favorites].concat(
      (tree.projects || []).map(function (p) { return p.sessions; }),
      (tree.archived_projects || []).map(function (p) { return p.sessions; }),
      (tree.test_runs || []).map(function (p) { return p.sessions; }));
    lists.forEach(function (l) { (l || []).forEach(fn); });
  }

  function nodeReflects(tree, ref, check) {
    var ok = false;
    forEachNode(tree, function (n) { if (n.ref === ref && check(n)) ok = true; });
    return ok;
  }

  function addPending(op) {
    op.at = Date.now();
    model.pending.set(op.id, op);
    if (model.tree) renderTree(model.tree);
    setTimeout(function () {
      var p = model.pending.get(op.id);
      if (p && p === op) { model.pending.delete(op.id); if (op.onEvict) op.onEvict(); if (model.tree) renderTree(model.tree); }
    }, 30000);
  }

  // Called by every admitted resync: drop mutation-type ops the payload now
  // reflects.
  function reconcilePending(tree) {
    model.pending.forEach(function (op, id) {
      if (op.confirm && op.confirm(tree)) model.pending.delete(id);
    });
  }

  // --- Public mutation API ---------------------------------------------------
  function favorite(ref, on) {
    var sid = ref.replace(/^local:/, "");
    addPending({
      id: "fav:" + ref, apply: function (t) { forEachNode(t, function (n) { if (n.ref === ref) n.favorite = on; }); },
      confirm: function (t) { return nodeReflects(t, ref, function (n) { return !!n.favorite === on; }); },
    });
    postJSON("/api/favorite", { kind: "session", id: sid, favorited: on });
  }
  function archive(ref, on) {
    var sid = ref.replace(/^local:/, "");
    var op = { id: "arch:" + ref, apply: function (t) { if (on) forEachNode(t, function (n) { if (n.ref === ref) n.__drop = true; }); } };
    addPending(op);
    postJSON("/api/archive", { kind: "session", id: sid, archived: on }).then(function () { model.pending.delete(op.id); scheduleResync(); });
  }
  function rename(ref, name) {
    var sid = ref.replace(/^local:/, "");
    addPending({
      id: "name:" + ref, apply: function (t) { forEachNode(t, function (n) { if (n.ref === ref) n.title = name; }); },
      confirm: function (t) { return nodeReflects(t, ref, function (n) { return n.title === name; }); },
    });
    postJSON("/api/sessions/" + encodeURIComponent(ref) + "/rename", { name: name }).catch(function () {});
  }

  function postJSON(url, body) {
    return window.fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      .then(function (r) {
        if (!r || !r.ok) { throw new Error("post failed"); }
        scheduleResync(); // every mutation POST-2xx schedules a resync (round-3 H1)
        return r;
      })
      .catch(function (e) {
        if (window.SerfToast) window.SerfToast.show("Action failed", "error");
        throw e;
      });
  }

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

  // --- Coalesced resync + event wiring ---------------------------------------
  var resyncTimer = null, lastResync = 0;
  function scheduleResync() {
    if (resyncTimer) return;
    var wait = Math.max(0, 2000 - (Date.now() - lastResync)); // ≥2s spacing, trailing
    resyncTimer = setTimeout(function () { resyncTimer = null; lastResync = Date.now(); doResync(); }, wait);
  }
  function doResync() {
    var mySeq = ++model.seq;
    window.fetch("/api/tree").then(function (r) { return r.json(); }).then(function (tree) {
      if (mySeq !== model.seq) return; // sequence guard
      reconcilePending(tree);
      renderTree(tree);
      // Re-request children for projects the user expanded (served from the memo).
      model.expanded.forEach(function (key) { refetchProjectChildren(key); });
    }).catch(function () {}); // resync failure keeps the last good model rendered
  }
  function refetchProjectChildren(key) {
    window.fetch("/api/tree/project?key=" + encodeURIComponent(key)).then(function (r) { return r.ok ? r.json() : null; }).then(function (p) {
      if (!p || !model.tree) return;
      model.lazyCache.set(key, p);
      var projects = (model.tree.projects || []).concat(model.tree.archived_projects || [], model.tree.test_runs || []);
      for (var i = 0; i < projects.length; i++) { if (projects[i].key === key) { projects[i].sessions = p.sessions; } }
      renderTree(model.tree);
    }).catch(function () {});
  }

  var QUALIFYING = { "thread/started": 1, "thread/closed": 1, "thread/status/changed": 1, "serf/job/started": 1, "serf/job/finished": 1, "serf/attention/changed": 1 };
  function onNotification(method, params) {
    if (method === "serf/attention/changed") { applyAttentionInstant(params); scheduleResync(); return; }
    if (QUALIFYING[method]) scheduleResync();
  }
  // Instant path: attention changed[] carries the coarse 4-value level and only
  // fires on level transitions; adjust existing rows' tint + badge instantly.
  function applyAttentionInstant(params) {
    (params && params.changed || []).forEach(function (ch) {
      var rows = document.querySelectorAll('#sidebar .sb-row[data-ref="local:' + ch.threadId + '"]');
      for (var i = 0; i < rows.length; i++) rows[i].setAttribute("data-attention", ch.level);
    });
  }

  if (window.SerfAppwire && window.SerfAppwire.onNotification) window.SerfAppwire.onNotification(onNotification);
  if (window.SerfAppwire && window.SerfAppwire.onConnectionRestored) window.SerfAppwire.onConnectionRestored(scheduleResync);
  setInterval(scheduleResync, 60000); // 60s idle resync

  paintSkeleton();
  fetchTree();

  document.body && document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "workspace") syncActiveRow();
  });

  window.SerfSidebar = { renderTree: renderTree, refresh: fetchTree, favorite: favorite, archive: archive, rename: rename, close: function () {} };
})();
