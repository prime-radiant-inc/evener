// Sidebar: client-rendered navigation tree. Data (/api/tree) is the state; the
// DOM is a keyed projection reconciled on the server's scope-qualified RowID, so
// unchanged rows keep node identity (hover, scroll, open menus) across updates.
(function () {
  "use strict";

  var EXPAND_PREFIX = "serf-hub.sidebar.expanded.";
  var model = { tree: null, expanded: new Set(), lazyCache: new Map(), seq: 0, pending: new Map() };
  window.SerfSidebarModel = model; // test/inspection surface
  window.SerfSidebarInternal = { buildRow: buildRow, stateIconKey: stateIconKey, stateWord: stateWord }; // test/inspection surface

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

  // stateIconKey maps a tree-node state (+ optional ask_pending) to the
  // SerfIcons key and the hubapi.StateWord-equivalent tooltip text. Mirrors
  // hubapi.StateWord verbatim so the web tooltip and the TUI word agree.
  var STATE_WORDS = {
    active: "Working", warning: "Warning", errored: "Error",
    idle: "Idle", ended: "Ended", closed: "Ended", notLoaded: "Not loaded",
  };
  function stateIconKey(state, askPending) {
    if (state === "awaiting") return askPending ? "questionWaiting" : "yourMove";
    if (state === "active") return "working";
    if (state === "warning") return "warning";
    if (state === "errored") return "error";
    if (state === "idle") return "idle";
    return "ended";
  }
  function stateWord(state, askPending) {
    if (state === "awaiting") return askPending ? "Question waiting" : "Your move";
    return STATE_WORDS[state] || state;
  }

  function buildRow(n) {
    var a = document.createElement("a");
    a.className = "sb-row";
    a.setAttribute("data-row-id", n.row_id);
    a.setAttribute("data-state", n.state);
    a.setAttribute("data-ref", n.ref);
    if (n.__projectKey) a.setAttribute("data-project-key-of", n.__projectKey);
    a.setAttribute("href", "/s/" + n.session_id);
    a.setAttribute("hx-get", "/_partials/s/" + n.session_id + "/workspace");
    a.setAttribute("hx-target", "#workspace");
    a.setAttribute("hx-swap", "innerHTML");
    a.setAttribute("hx-push-url", "/s/" + n.session_id);
    a.innerHTML =
      '<div class="dot-col"><span class="status-dot" data-state="' + n.state + '"></span>' +
      '<span class="status-icon" data-state="' + n.state + '"></span></div>' +
      '<div class="text-col"><div class="title"></div><div class="meta"></div></div>';
    a.querySelector(".title").textContent = n.title;
    var icon = a.querySelector(".status-icon");
    icon.innerHTML = window.SerfIcons[stateIconKey(n.state, n.ask_pending)];
    icon.setAttribute("title", stateWord(n.state, n.ask_pending));
    var meta = a.querySelector(".meta");
    if (n.branch) meta.appendChild(metaSpan(n.branch));
    meta.appendChild(metaSpan(ageString(n.updated_at)));
    if (n.favorite) a.setAttribute("data-favorite", "");
    if (n.children && n.children.length) a.appendChild(buildChildrenToggle(n));
    var menuBtn = document.createElement("button");
    menuBtn.type = "button";
    menuBtn.className = "sb-menu-btn btn-icon";
    menuBtn.setAttribute("aria-label", "row menu");
    menuBtn.setAttribute("aria-haspopup", "menu");
    menuBtn.textContent = "⋯"; // ⋯
    menuBtn.addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation();
      openMenu(menuBtn, sessionMenuItems(n));
    });
    a.appendChild(menuBtn);
    return a;
  }
  function metaSpan(text) { var s = document.createElement("span"); s.textContent = text; return s; }

  function patchRow(a, n) {
    if (a.getAttribute("data-state") !== n.state) {
      a.setAttribute("data-state", n.state);
      var dot = a.querySelector(".status-dot");
      if (dot) dot.setAttribute("data-state", n.state);
    }
    var icon = a.querySelector(".status-icon");
    if (icon) {
      var key = stateIconKey(n.state, n.ask_pending);
      var word = stateWord(n.state, n.ask_pending);
      if (icon.getAttribute("title") !== word) {
        icon.setAttribute("data-state", n.state);
        icon.innerHTML = window.SerfIcons[key];
        icon.setAttribute("title", word);
      }
    }
    var title = a.querySelector(".title");
    if (title && title.textContent !== n.title) title.textContent = n.title;
    if (n.favorite) a.setAttribute("data-favorite", ""); else a.removeAttribute("data-favorite");
    patchChildrenToggle(a, n);
  }

  // --- Subagent children disclosure ------------------------------------------
  // A session row whose node carries `children` (subagent threads spawned
  // under it — hubapi.TreeNode.Children, capped at 50 server-side, Task 6)
  // gains a small disclosure toggle inside the row: collapsed by default,
  // expanding reveals the children as their own keyed rows (flatten's
  // pushChildren, below). Expansion key is "children:<row_id>" — the
  // "children:" prefix guarantees it can never collide with a project slug
  // or a "section:*"/cluster row_id, all of which share the one
  // model.expanded Set + localStorage namespace.
  function childrenKey(n) { return "children:" + n.row_id; }
  function buildChildrenToggle(n) {
    var count = n.children.length;
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "subagent-toggle sb-children-toggle";
    btn.setAttribute("aria-expanded", String(model.expanded.has(childrenKey(n))));
    btn.innerHTML = '<span class="sb-children-chevron">›</span><span class="sb-children-count"></span>';
    setChildrenToggleText(btn, count);
    // Nested inside the row's own <a>, so a click must not also navigate it —
    // same technique as the row's ⋯ menu button above.
    btn.addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation();
      toggleExpanded(childrenKey(n));
    });
    btn.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault(); e.stopPropagation();
      toggleExpanded(childrenKey(n));
    });
    return btn;
  }
  function setChildrenToggleText(btn, count) {
    btn.querySelector(".sb-children-count").textContent = String(count);
    // Bare count, not "Completed (N)": children may still be running, so
    // claiming "completed" would misrepresent live subagents.
    btn.setAttribute("aria-label", count + (count === 1 ? " subagent" : " subagents"));
  }
  // Keeps the toggle's DOM node identity across reconcile — add/remove/update
  // in place rather than rebuilding the row, mirroring how patchRow itself
  // never recreates .title/.meta.
  function patchChildrenToggle(a, n) {
    var btn = a.querySelector(".sb-children-toggle");
    var hasChildren = !!(n.children && n.children.length);
    if (hasChildren && !btn) { a.insertBefore(buildChildrenToggle(n), a.querySelector(".sb-menu-btn")); return; }
    if (!hasChildren && btn) { btn.remove(); return; }
    if (btn) {
      btn.setAttribute("aria-expanded", String(model.expanded.has(childrenKey(n))));
      setChildrenToggleText(btn, n.children.length);
    }
  }

  // --- Row menu (⋯ popover) ----------------------------------------------------
  var openMenuEl = null, menuAnchor = null;
  function closeMenu() {
    if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; menuAnchor = null; document.removeEventListener("click", onDocClick, true); }
  }
  function onDocClick(e) { if (openMenuEl && !openMenuEl.contains(e.target) && e.target !== menuAnchor) closeMenu(); }
  function openMenu(anchor, items) {
    closeMenu();
    var menu = document.createElement("div");
    menu.className = "sb-menu";
    menu.setAttribute("role", "menu");
    items.forEach(function (it) {
      if (it.hidden) return;
      var b = document.createElement("button");
      b.type = "button"; b.className = "sb-menu-item"; b.setAttribute("role", "menuitem"); b.textContent = it.label;
      b.addEventListener("click", function (e) { e.preventDefault(); e.stopPropagation(); closeMenu(); it.run(); });
      menu.appendChild(b);
    });
    document.body.appendChild(menu);
    var r = anchor.getBoundingClientRect();
    menu.style.position = "absolute";
    menu.style.top = (r.bottom + window.scrollY + 2) + "px";
    menu.style.left = (r.left + window.scrollX) + "px";
    openMenuEl = menu; menuAnchor = anchor;
    setTimeout(function () { document.addEventListener("click", onDocClick, true); }, 0);
    document.addEventListener("keydown", onMenuKeydown);
    var first = menu.querySelector(".sb-menu-item"); if (first) first.focus();
  }
  function onMenuKeydown(e) {
    if (!openMenuEl) { document.removeEventListener("keydown", onMenuKeydown); return; }
    var items = [].slice.call(openMenuEl.querySelectorAll(".sb-menu-item"));
    var idx = items.indexOf(document.activeElement);
    if (e.key === "Escape") { e.preventDefault(); var a = menuAnchor; closeMenu(); if (a) a.focus(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); (items[idx + 1] || items[0]).focus(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); (items[idx - 1] || items[items.length - 1]).focus(); }
  }

  function sessionMenuItems(n) {
    return [
      { label: "Open", run: function () { window.location.href = "/s/" + n.session_id; } },
      { label: "Open beside", run: function () { if (window.SerfPanes) window.SerfPanes.open("/thread/" + encodeURIComponent(n.ref), n.title); } },
      { label: n.favorite ? "Unfavorite" : "Favorite", run: function () { window.SerfSidebar.favorite(n.ref, !n.favorite); } },
      { label: "Rename", hidden: !n.rename, run: function () { startInlineRename(n); } },
      { label: n.tier === "archived" ? "Unarchive" : "Archive", run: function () { window.SerfSidebar.archive(n.ref, n.tier !== "archived"); } },
    ];
  }
  function projectMenuItems(p) {
    // A project stamped __archived (by pushArchivedSection, below) renders
    // inside the Archived section and offers Unarchive instead of Archive.
    var archived = !!p.__archived;
    return [
      { label: "New session", run: function () { window.location.href = "/new?dir=" + encodeURIComponent(p.working_dir); } },
      { label: "Settings", run: function () { window.location.href = "/settings/project?cwd=" + encodeURIComponent(p.working_dir); } },
      { label: archived ? "Unarchive" : "Archive", run: function () {
          window.fetch("/api/archive", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ kind: "project", id: p.working_dir, archived: !archived }) }).then(scheduleResync);
        } },
      { label: "Delete…", run: function () { confirmDeleteProject(p); } },
    ];
  }
  function startInlineRename(n) {
    var row = document.querySelector('#sidebar .sb-row[data-row-id="' + cssEscape(n.row_id) + '"]');
    if (!row) return;
    var title = row.querySelector(".title");
    var input = document.createElement("input");
    input.className = "sb-rename-input"; input.value = n.title;
    title.replaceWith(input); input.focus(); input.select();
    function commit() { var v = input.value.trim(); if (v && v !== n.title) window.SerfSidebar.rename(n.ref, v); if (model.tree) renderTree(model.tree); }
    input.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); commit(); } else if (e.key === "Escape") { e.preventDefault(); if (model.tree) renderTree(model.tree); } });
    input.addEventListener("blur", commit);
  }
  function confirmDeleteProject(p) {
    var n = p.session_count || (p.sessions || []).length;
    if (!window.confirm("Delete project " + p.name + "? " + n + " session(s), " + (p.worktrees || 0) + " worktree(s). Worktrees are not touched.")) return;
    window.fetch("/api/project/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key: p.key, working_dir: p.working_dir }) })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (res) {
        if (res && res.deleted && onDeletedRedirect(res.deleted)) return;
        scheduleResync();
      });
  }
  function onDeletedRedirect(deleted) {
    var path = window.location.pathname || "";
    for (var i = 0; i < deleted.length; i++) { if (path === "/s/" + deleted[i]) { window.location.href = "/new"; return true; } }
    return false;
  }
  function cssEscape(s) { return (window.CSS && window.CSS.escape) ? window.CSS.escape(s) : s.replace(/["\\]/g, "\\$&"); }

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
      if (ck && !seen[ck]) {
        if (menuAnchor && container.children[m].contains(menuAnchor)) {
          var survivor = container.children[m].previousElementSibling || container.children[m].nextElementSibling;
          closeMenu();
          if (survivor && survivor.focus) survivor.focus();
        }
        container.removeChild(container.children[m]);
      }
    }
  }

  // --- Full tree render ------------------------------------------------------
  function renderTree(tree) {
    model.tree = tree;
    restoreExpanded(tree);
    var el = sidebarEl();
    if (!el) return;
    var root = el.querySelector(".sb-tree");
    if (!root) { el.innerHTML = '<nav class="sb-tree" aria-label="Sessions"></nav>'; root = el.querySelector(".sb-tree"); }
    var flat = flatten(applyPending(tree)); // applyPending is Task 20; identity until then
    reconcile(root, flat, buildRowOrSection, patchRowOrSection);
    syncActiveRow();
  }

  // flatten emits one keyed element descriptor per rendered row/section in
  // order: NeedsYou, Pinned, active projects, Archived (N), Test runs (N).
  // Render session rows grouped under project section headers; expansion +
  // lazy children are honored via model.expanded.
  function flatten(tree) {
    var out = [];
    (tree.needs_you || []).forEach(function (n) { if (!n.__drop) { out.push(n); pushChildren(out, n); } });
    (tree.favorites || []).forEach(function (n) { if (!n.__drop) { out.push(n); pushChildren(out, n); } });
    (tree.projects || []).forEach(function (p) { pushProject(out, p); });
    pushArchivedSection(out, tree);
    pushTestRunsSection(out, tree);
    return out;
  }
  function pushProject(out, p) {
    // Project header is itself a keyed element (data-row-id="header:<key>").
    out.push({ row_id: "header:" + p.key, __project: p });
    var expanded = model.expanded.has(p.key) || p.default_expanded;
    if (!expanded) return;
    (p.sessions || []).forEach(function (n) {
      if (n.__drop) return;
      n.__projectKey = p.key;
      if (n.kind === "cluster") { pushCluster(out, n); return; }
      out.push(n);
      pushChildren(out, n);
    });
  }

  // Subagent children: emitted right after their parent row, once the
  // parent's own disclosure (childrenKey) is expanded. Any tier's node may
  // carry children — apiTreeNode recurses the same way for NeedsYou/Pinned
  // and project sessions alike — so this is called from every push site
  // above, not just pushProject. Children render via buildRow itself
  // (buildRowOrSection stamps the .subagent-row indent class from __child).
  function pushChildren(out, n) {
    if (!n.children || !n.children.length || !model.expanded.has(childrenKey(n))) return;
    n.children.forEach(function (c) {
      if (c.__drop) return;
      c.__child = true;
      c.__projectKey = n.__projectKey;
      out.push(c);
    });
  }

  // Cluster fold: a T5 synthetic kind:"cluster" node folding a run of
  // same-titled sessions into one row (hubcore.clusterRepeatedTitles). The
  // fold row itself is pushed like any other keyed element; its `children`
  // (the folded members — always plain, childless sessions per
  // hubcore.clusterable) are emitted only once the cluster's OWN row_id is
  // in model.expanded. Members render as ordinary rows — no __child stamp —
  // since they are real, independently navigable sessions, not de-weighted
  // subagents.
  function pushCluster(out, n) {
    out.push(n);
    if (!model.expanded.has(n.row_id)) return;
    (n.children || []).forEach(function (m) {
      if (m.__drop) return;
      m.__projectKey = n.__projectKey;
      out.push(m);
    });
  }

  // --- Sections (collapsed-by-default groupings below the top-of-rail tiers) -
  // A section is itself a keyed element (data-row-id="section:<key>"), built
  // via the same reconcile dispatch as rows/project headers. Its content
  // (projects + their sessions) is only emitted by flatten when expanded, and
  // reuses pushProject/buildProjectHeader/buildRow verbatim — a section
  // project's own expansion, menu, and pending-overlay participation work
  // exactly like an active project's.
  var SECTION_ARCHIVED = "section:archived";
  var SECTION_TEST_RUNS = "section:test-runs";
  var SECTION_KEYS = [SECTION_ARCHIVED, SECTION_TEST_RUNS];

  // pushSection appends a section header (if its bucket is non-empty) and,
  // once expanded, its projects. markKey (e.g. "__archived") is stamped onto
  // each project so projectMenuItems can offer Unarchive instead of Archive;
  // pass null/undefined for buckets that don't need that (Test runs: a
  // project there was never in the archived bucket, so it keeps plain
  // Archive — precedence between the two buckets is decided server-side,
  // never here).
  function pushSection(out, list, key, label, markKey) {
    if (!list.length) return; // no chrome for an empty bucket
    out.push({ row_id: key, __section: true, key: key, label: label, count: list.length });
    if (model.expanded.has(key)) {
      list.forEach(function (p) { if (markKey) p[markKey] = true; pushProject(out, p); });
    }
  }
  function pushArchivedSection(out, tree) {
    pushSection(out, tree.archived_projects || [], SECTION_ARCHIVED, "Archived", "__archived");
  }
  function pushTestRunsSection(out, tree) {
    pushSection(out, tree.test_runs || [], SECTION_TEST_RUNS, "Test runs", null);
  }

  function buildRowOrSection(n) {
    if (n.__section) return buildSectionHeader(n);
    if (n.kind === "cluster") return buildClusterFold(n);
    if (n.__project) return buildProjectHeader(n.__project);
    var row = buildRow(n);
    if (n.__child) row.classList.add("subagent-row");
    return row;
  }
  function patchRowOrSection(el, n) {
    if (n.__section) { patchSectionHeader(el, n); return; }
    if (n.kind === "cluster") { patchClusterFold(el, n); return; }
    if (n.__project) patchProjectHeader(el, n.__project); else patchRow(el, n);
  }

  // A cluster fold is a button-like row — NOT a session link, it has no
  // real session of its own — so it carries none of buildRow's href/hx-*.
  // Reuses the pre-rewrite .cluster-header/.cluster-chevron/.cluster-count
  // classes (style.css) verbatim for visual consistency; only the expand
  // mechanism (model.expanded + reconcile, keyed by the cluster's own
  // row_id) is new.
  function buildClusterFold(n) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "sb-row cluster-header";
    b.setAttribute("data-row-id", n.row_id);
    b.setAttribute("role", "button");
    b.setAttribute("aria-expanded", String(model.expanded.has(n.row_id)));
    b.innerHTML =
      '<div class="dot-col"><span class="cluster-chevron">›</span></div>' +
      '<div class="text-col"><span class="title"></span><span class="cluster-count"></span></div>';
    b.querySelector(".title").textContent = n.title;
    setClusterCount(b, n);
    b.addEventListener("click", function () { toggleExpanded(n.row_id); });
    b.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault();
      toggleExpanded(n.row_id);
    });
    return b;
  }
  function patchClusterFold(el, n) {
    el.setAttribute("aria-expanded", String(model.expanded.has(n.row_id)));
    var title = el.querySelector(".title");
    if (title && title.textContent !== n.title) title.textContent = n.title;
    setClusterCount(el, n);
  }
  function setClusterCount(el, n) {
    var el2 = el.querySelector(".cluster-count");
    if (el2) el2.textContent = "×" + n.cluster_count; // "describe this image ×5" (mockup #10 rec C copy)
  }

  function buildSectionHeader(sec) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "sb-section";
    b.setAttribute("data-row-id", sec.row_id);
    b.setAttribute("data-section-key", sec.key);
    // role="button" is redundant on a real <button> but kept for parity with
    // .project-header's div[role=button] — an explicit, unambiguous contract
    // for anything (tests, assistive tech quirks) that greps for it.
    b.setAttribute("role", "button");
    b.setAttribute("aria-expanded", String(model.expanded.has(sec.key)));
    b.innerHTML = '<span class="sb-section-label"></span>';
    setSectionLabel(b, sec);
    b.addEventListener("click", function () { toggleExpanded(sec.key); });
    // .project-header's toggle is click-only today; this is new code, so it
    // gets real keyboard support rather than inheriting that gap. A real
    // <button> gives native Tab focus; the explicit Enter/Space handler
    // below is the actual activation mechanism (not relied on implicitly),
    // so behavior is identical across browsers/environments.
    b.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault();
      toggleExpanded(sec.key);
    });
    return b;
  }
  function patchSectionHeader(el, sec) {
    el.setAttribute("aria-expanded", String(model.expanded.has(sec.key)));
    setSectionLabel(el, sec);
  }
  function setSectionLabel(el, sec) {
    var label = el.querySelector(".sb-section-label");
    if (label) label.textContent = sec.label + " (" + sec.count + ")";
  }

  function buildProjectHeader(p) {
    var d = document.createElement("div");
    // NOTE: intentionally NOT "sb-row" — a project header is not a session
    // row, and the existing template/CSS (partials/sidebar.html, style.css)
    // already style ".project-header" as its own standalone class.
    d.className = "project-header";
    d.setAttribute("data-row-id", "header:" + p.key);
    d.setAttribute("data-project-key", p.key);
    d.setAttribute("role", "button");
    d.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded === true));
    d.innerHTML = '<span class="project-name"></span><span class="project-rollup"></span>';
    d.querySelector(".project-name").textContent = p.name;
    setProjectRollup(d, p);
    d.addEventListener("click", function () { toggleExpanded(p.key); });
    var menuBtn = document.createElement("button");
    menuBtn.type = "button";
    menuBtn.className = "sb-menu-btn btn-icon";
    menuBtn.setAttribute("aria-label", "project menu");
    menuBtn.setAttribute("aria-haspopup", "menu");
    menuBtn.textContent = "⋯"; // ⋯
    menuBtn.addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation();
      openMenu(menuBtn, projectMenuItems(p));
    });
    d.appendChild(menuBtn);
    return d;
  }
  function patchProjectHeader(el, p) {
    el.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded === true));
    setProjectRollup(el, p);
  }

  // Magnitude rollup badges (mockup #10 rec A): "⟳N · ◆M" says how many of a
  // project's sessions are working vs. need you — the two counts the
  // attention spec's rollup rules define (needs-you outranks active; never a
  // third category). Shared by build + patch so the reconcile patch path
  // updates counts/tint on the SAME .project-rollup node instead of
  // rebuilding the header. data-state carries p.rollup_state (the
  // server-computed, rank-ranked winning state) purely as a CSS/test hook —
  // the badges' own rollup-live/rollup-attn classes already carry the
  // blue/amber tint (style.css), reusing the existing status-dot palette.
  function setProjectRollup(el, p) {
    var r = el.querySelector(".project-rollup");
    if (!r) return;
    r.setAttribute("data-state", p.rollup_state || "");
    r.textContent = ""; // clear prior badges/separator (not innerHTML)
    var live = p.rollup_live || 0;
    var attn = p.rollup_attn || 0;
    if (live > 0) r.appendChild(buildRollupBadge("rollup-live", "⟳", live));
    if (live > 0 && attn > 0) {
      var sep = document.createElement("span");
      sep.className = "rollup-sep";
      sep.textContent = "·"; // "·"
      r.appendChild(sep);
    }
    if (attn > 0) r.appendChild(buildRollupBadge("rollup-attn", "◆", attn)); // "◆"
  }
  function buildRollupBadge(cls, glyph, count) {
    var b = document.createElement("span");
    b.className = "rollup-badge " + cls;
    var g = document.createElement("span");
    g.className = "rollup-glyph";
    g.textContent = glyph;
    b.appendChild(g);
    b.appendChild(document.createTextNode(String(count)));
    return b;
  }

  // Generic expand/collapse toggle, keyed either by a project key or a
  // section key (e.g. SECTION_ARCHIVED) — both live in the same
  // model.expanded Set and persist through the same localStorage mechanism.
  function toggleExpanded(key) {
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
  function restoreExpanded(tree) {
    var all = (tree.projects || []).concat(tree.archived_projects || [], tree.test_runs || []);
    all.forEach(function (p) {
      try { if (window.localStorage.getItem(EXPAND_PREFIX + p.key) === "true") model.expanded.add(p.key); } catch (e) {}
    });
    SECTION_KEYS.forEach(function (key) {
      try { if (window.localStorage.getItem(EXPAND_PREFIX + key) === "true") model.expanded.add(key); } catch (e) {}
    });
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

  // Recurses into every node's .children — subagent children AND cluster
  // members alike travel on the same wire field — so pending ops
  // (favorite/rename/__drop) reach a child or cluster-member ref exactly as
  // they would a top-level session.
  function forEachNode(tree, fn) {
    var lists = [tree.needs_you, tree.favorites].concat(
      (tree.projects || []).map(function (p) { return p.sessions; }),
      (tree.archived_projects || []).map(function (p) { return p.sessions; }),
      (tree.test_runs || []).map(function (p) { return p.sessions; }));
    lists.forEach(function (l) { (l || []).forEach(function (n) { forEachNodeDeep(n, fn); }); });
  }
  function forEachNodeDeep(n, fn) {
    fn(n);
    (n.children || []).forEach(function (c) { forEachNodeDeep(c, fn); });
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
  // Task 22: a deep-link whose session is rendered nowhere yet (collapsed
  // behind a section/project/cluster/children disclosure) must still end up
  // visible + marked data-active, not just silently fail to highlight.
  // autoRevealedPath gates the search+expand+re-render side effect to at
  // most once per distinct pathname — the mark-active pass below always
  // runs, but the (potentially tree-wide) reveal search only runs when the
  // pathname just changed, so a genuinely-absent session can't cause a
  // render loop (each renderTree tail-calls syncActiveRow again; the second
  // call sees the same pathname and skips straight past the guard).
  var autoRevealedPath = null;
  function syncActiveRow() {
    var path = (window.location && window.location.pathname) || "";
    var clean = path.replace(/\/+$/, "");
    var alreadyVisible = markActiveRows(clean);
    if (clean === autoRevealedPath) return; // already attempted for this pathname
    autoRevealedPath = clean;
    if (alreadyVisible || !model.tree) return;
    var m = /^\/s\/(.+)$/.exec(clean);
    if (!m) return; // not a session deep-link
    var chain = findRevealChain(model.tree, m[1]);
    if (!chain || !chain.length) return; // not found anywhere, or needs no expansion
    var changed = false;
    chain.forEach(function (key) {
      if (!model.expanded.has(key)) { model.expanded.add(key); persistExpanded(key); changed = true; }
    });
    if (changed) renderTree(model.tree); // tail-calls syncActiveRow; guard above short-circuits the re-entry
  }
  // Marks the row whose href matches `clean` data-active (clearing every
  // other row) and reports whether a match was found, so callers can skip
  // the (potentially expensive) reveal search when the row is already
  // visible.
  function markActiveRows(clean) {
    var rows = document.querySelectorAll("#sidebar .sb-row[href]");
    var found = false;
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute("href") === clean) { rows[i].setAttribute("data-active", ""); found = true; }
      else rows[i].removeAttribute("data-active");
    }
    return found;
  }
  // Searches every tier of the tree (NeedsYou, Pinned, active/archived/
  // test-run projects — including nested cluster members and subagent
  // children) for the node whose session_id matches, and returns the list of
  // model.expanded keys needed to make it visible: any enclosing
  // section/project key, plus a cluster row_id or children:<row_id> key for
  // each disclosure the match sits behind. Returns null if sessionId is
  // nowhere in the tree.
  function findRevealChain(tree, sessionId) {
    return (
      searchNodes(tree.needs_you, sessionId, []) ||
      searchNodes(tree.favorites, sessionId, []) ||
      searchProjects(tree.projects, sessionId, []) ||
      searchProjects(tree.archived_projects, sessionId, [SECTION_ARCHIVED]) ||
      searchProjects(tree.test_runs, sessionId, [SECTION_TEST_RUNS]) ||
      null
    );
  }
  function searchProjects(projects, sessionId, prefix) {
    for (var i = 0; i < (projects || []).length; i++) {
      var p = projects[i];
      var found = searchNodes(p.sessions, sessionId, prefix.concat([p.key]));
      if (found) return found;
    }
    return null;
  }
  // `chain` is the expansion keys needed to reach `nodes` itself; a match
  // inside a cluster's members or a session's children extends the chain
  // with that disclosure's own key before returning.
  function searchNodes(nodes, sessionId, chain) {
    for (var i = 0; i < (nodes || []).length; i++) {
      var n = nodes[i];
      if (n.session_id === sessionId) return chain;
      if (n.kind === "cluster") {
        var cm = searchNodes(n.children, sessionId, chain.concat([n.row_id]));
        if (cm) return cm;
      } else if (n.children && n.children.length) {
        var cc = searchNodes(n.children, sessionId, chain.concat([childrenKey(n)]));
        if (cc) return cc;
      }
    }
    return null;
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
  // One-time migration of legacy basename-keyed expansion entries to the new
  // path-slug keys. Runs after the first render (needs each project's key+name);
  // co-basename collisions copy the old value to every matching new key.
  var migratedExpansion = false;
  function migrateExpansionKeys() {
    if (migratedExpansion || !model.tree) return;
    migratedExpansion = true;
    var all = (model.tree.projects || []).concat(model.tree.archived_projects || [], model.tree.test_runs || []);
    all.forEach(function (p) {
      try {
        var legacy = window.localStorage.getItem(EXPAND_PREFIX + p.name);
        if (legacy === null) return;
        if (window.localStorage.getItem(EXPAND_PREFIX + p.key) === null) window.localStorage.setItem(EXPAND_PREFIX + p.key, legacy);
        if (legacy === "true") model.expanded.add(p.key);
      } catch (e) {}
    });
  }

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

  // --- Survivor behaviors carried verbatim from the pre-rewrite sidebar.js ---

  // Open-beside control on sidebar subagent rows — delegate clicks on
  // .subagent-row-wrap .open-beside-btn → window.SerfPanes.open("/thread/<ref>", title).
  // Guards:
  //   • preventDefault + stopPropagation so the sibling <a> row does not navigate.
  //   • window.SerfPanes presence: absent when this sidebar runs inside a pane
  //     iframe where panes must not nest.
  function onSidebarOpenBeside(e) {
    var btn = e.target.closest(".open-beside-btn");
    if (!btn) return;
    var wrap = btn.closest(".subagent-row-wrap");
    if (!wrap) return; // only handle subagent-row-wrap open-beside here
    e.preventDefault();
    e.stopPropagation();
    if (!window.SerfPanes) return;
    var ref = wrap.getAttribute("data-ref") || "";
    var title = wrap.getAttribute("data-title") || ref;
    if (!ref) return;
    var href = window.SerfPanes.threadHref ? window.SerfPanes.threadHref(ref) : ("/thread/" + encodeURIComponent(ref));
    window.SerfPanes.open(href, title);
  }
  document.addEventListener("click", onSidebarOpenBeside);

  // Keyboard: Enter/Space on .subagent-row-wrap .open-beside-btn.
  function onSidebarOpenBesideKeydown(e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    var btn = e.target.closest(".open-beside-btn");
    if (!btn) return;
    if (!btn.closest(".subagent-row-wrap")) return;
    e.preventDefault();
    e.stopPropagation();
    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  }
  document.addEventListener("keydown", onSidebarOpenBesideKeydown);

  // Mobile hamburger: toggle a body[data-sidebar-open] flag that the
  // mobile media query reads to slide the sidebar in. Tapping a sidebar
  // link also closes (via htmx:beforeRequest) so navigating to a session
  // doesn't leave the drawer hanging. The close-on-outside handler is
  // only armed while open — keeping it always-on caused double-fires
  // under mobile emulation where the synthetic click stack toggled
  // open then immediately closed.
  var sidebarTrapHandle = null;

  function setSidebarOpen(open) {
    if (open) {
      document.body.setAttribute("data-sidebar-open", "");
      document.addEventListener("click", onOutsideClick, true);
      // Only trap focus on phone — desktop sidebar isn't a drawer. Match
      // the design-language breakpoint.
      var isPhone = window.matchMedia && window.matchMedia("(max-width: 767px)").matches;
      if (isPhone && window.SerfFocusTrap) {
        var sidebar = document.getElementById("sidebar");
        var trigger = document.querySelector("[data-sidebar-toggle]");
        if (sidebar) {
          sidebarTrapHandle = window.SerfFocusTrap.activate(sidebar, trigger);
        }
      }
    } else {
      document.body.removeAttribute("data-sidebar-open");
      document.removeEventListener("click", onOutsideClick, true);
      if (sidebarTrapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(sidebarTrapHandle);
        sidebarTrapHandle = null;
      }
    }
  }

  function isSidebarOpen() {
    return document.body.hasAttribute("data-sidebar-open");
  }

  function onOutsideClick(e) {
    var t = e.target;
    if (!t) return;
    if (t.closest && (t.closest("#sidebar") || t.closest("[data-sidebar-toggle]"))) {
      return;
    }
    setSidebarOpen(false);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t) return;
    var trigger = t.closest && t.closest("[data-sidebar-toggle]");
    if (trigger) {
      e.preventDefault();
      e.stopPropagation();
      setSidebarOpen(!isSidebarOpen());
    }
  });

  document.addEventListener("htmx:beforeRequest", function (e) {
    var trigger = e.detail && e.detail.elt;
    if (trigger && trigger.closest && trigger.closest("#sidebar")) {
      setSidebarOpen(false);
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && isSidebarOpen()) {
      // Do not close the mobile sidebar when the event originated inside the search dialog.
      if (e.target && e.target.closest && e.target.closest("#search-dialog")) return;
      var dlg = document.getElementById("search-dialog");
      if (dlg && dlg.open) return;
      setSidebarOpen(false);
    }
  });

  // Sidebar rail mode — persisted to localStorage. The body[data-sidebar-rail]
  // attribute is the single source of truth that CSS reads; the helper
  // syncs that attribute to storage and back.
  var RAIL_KEY = "serf-hub.sidebar.rail";

  function isRailEnabled() {
    try {
      return window.localStorage.getItem(RAIL_KEY) === "true";
    } catch (e) {
      return false;
    }
  }

  function setRail(enabled) {
    if (enabled) {
      document.body.setAttribute("data-sidebar-rail", "");
    } else {
      document.body.removeAttribute("data-sidebar-rail");
    }
    try {
      if (enabled) {
        window.localStorage.setItem(RAIL_KEY, "true");
      } else {
        window.localStorage.removeItem(RAIL_KEY);
      }
    } catch (e) {
      // localStorage may be disabled; flip still works for this session.
    }
    syncRailToggleLabel();
  }

  function toggleRail() {
    setRail(!document.body.hasAttribute("data-sidebar-rail"));
  }

  // Apply persisted rail state ASAP — before first paint when possible.
  if (isRailEnabled()) {
    setRail(true);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var btn = t.closest("[data-sidebar-rail-toggle]");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    toggleRail();
  });

  // ⌘B / Ctrl+B — toggle rail mode. Skip when the focus is on an editable
  // surface (textarea, contenteditable, input) so the shortcut doesn't fire
  // while the user is typing browser-native chords. Mobile (no
  // matchMedia "(min-width: 768px)") ignores the shortcut because rail
  // mode is a desktop affordance.
  function isEditableTarget(el) {
    if (!el) return false;
    var tag = (el.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || tag === "select") return true;
    if (el.isContentEditable) return true;
    return false;
  }

  document.addEventListener("keydown", function (e) {
    if (e.key !== "b" && e.key !== "B") return;
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.altKey || e.shiftKey) return;
    if (isEditableTarget(e.target)) return;
    // Desktop only — match the design-language breakpoint.
    if (window.matchMedia && window.matchMedia("(max-width: 767px)").matches) return;
    e.preventDefault();
    toggleRail();
  });

  // Sync the rail-toggle button's aria-label so screen readers hear the
  // correct direction after each flip. Runs on init + after each htmx
  // swap (the sidebar partial re-renders frequently).
  function syncRailToggleLabel() {
    var btn = document.querySelector("[data-sidebar-rail-toggle]");
    if (!btn) return;
    var railed = document.body.hasAttribute("data-sidebar-rail");
    btn.setAttribute("aria-label", railed ? "expand sidebar" : "collapse sidebar");
    btn.setAttribute("title", railed ? "expand sidebar (⌘B)" : "collapse sidebar (⌘B)");
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", syncRailToggleLabel);
  } else {
    syncRailToggleLabel();
  }
  document.addEventListener("htmx:afterSwap", syncRailToggleLabel);

  if (window.SerfAppwire && window.SerfAppwire.onNotification) window.SerfAppwire.onNotification(onNotification);
  if (window.SerfAppwire && window.SerfAppwire.onConnectionRestored) window.SerfAppwire.onConnectionRestored(scheduleResync);
  setInterval(scheduleResync, 60000); // 60s idle resync

  paintSkeleton();
  fetchTree();

  document.body && document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "workspace") syncActiveRow();
  });

  window.SerfSidebar = { renderTree: renderTree, refresh: fetchTree, favorite: favorite, archive: archive, rename: rename, close: function () { setSidebarOpen(false); } };
})();
