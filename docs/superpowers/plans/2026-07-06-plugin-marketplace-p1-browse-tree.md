# Plugin Marketplace Improvements — Part 1: Browse Tree Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the web plugins pane's "Browse" section from a single-marketplace `<select>` dropdown into a client-side tree of every registered marketplace — collapsible marketplace nodes, lazy-loaded plugin catalogs cached in memory, inline install-state per plugin row exactly as today — plus a debounced filter box above the tree that searches across all marketplaces' catalogs, lazy-loading any not-yet-expanded ones on first use.

**Architecture:** All the work lives in one file: the inline `<script>` inside `cmd/serf-hub/templates/partials/settings/plugins-manager.html`. The single-selection state (`browseName`/`browseCatalog`/`browseError`) is replaced by two structures — `expandedMarketplaces` (a `Set` of marketplace names currently expanded) and `browseCatalogs` (a plain object mapping marketplace name to `{loading:true}` / `{error}` / `{plugins}`, populated lazily and never cleared except on an explicit marketplace refresh/removal or a full pane reload). `renderBrowseSection` renders one `renderMarketplaceNode` per registered marketplace; a node's own plugin rows reuse the existing `renderBrowseRow`, now carrying `data-plugin`/`data-marketplace` on the `<li>` (mirroring `installedRow`'s convention) so install no longer depends on a single globally-selected marketplace. The expand toggle is a plain `<button aria-expanded>` with a `›` chevron rotated by CSS — the same idiom `sidebar.js`'s `.sb-children-toggle`/`.cluster-header` already use — which gets keyboard support for free (a native `<button>` fires `click` on Enter/Space) and needs none of `sidebar.js`'s custom keydown handling (that handling exists there only because its toggle is nested inside a navigating `<a>`; this tree has no such conflict). The filter box reuses `search.js`'s tiny local `debounce` helper (duplicated inline, matching the codebase's existing precedent of small deliberate duplication) and, because a naive full `render()` on every keystroke would blow away the filter `<input>`'s focus and cursor, `render()` gains a small save/restore of the input's identity and `selectionStart`/`selectionEnd` around the `innerHTML` replace. No backend or wire change: all three tasks reuse `serf/marketplace/list`, `serf/marketplace/browse`, `serf/plugin/list`, and `serf/plugin/install` exactly as `cmd/serf-hub/assets/plugins.js`'s existing `pluginsAdmin` wrappers already expose them.

**Tech Stack:** Vanilla JS (no bundler) inside a Go `html/template` partial, CSS (`cmd/serf-hub/assets/style.css`), JSDOM jstest (`cmd/serf-hub/jstest`, agent-run). No Go changes in this part.

**Anchors verified** 2026-07-06 against this worktree (`plugin-marketplace-improvements`, `/Users/jesse/git/prime-radiant-inc/serf/.worktrees/plugin-marketplace`): every line-numbered citation below was re-read from the live file, not assumed from the design doc. No drift was found — the design doc's own anchors (`renderBrowseSection` 133-157, `isInstalled` 39-41, `renderBrowseRow` 117-131, `refreshBrowse` 230-239, the delegated `change` listener 278-287, the install click handler 307-319, `render()`'s section-composition line 204, `plugins.js`'s RPC wrappers 8-23) all matched exactly.

## Global Constraints

- **Client-side only.** No backend/wire change for Part 1 (verbatim from the design doc: "No backend change for Part 1 — it reuses the existing `serf/marketplace/list`, `serf/marketplace/browse`, `serf/plugin/list`, `serf/plugin/install` RPCs"). `cmd/serf-hub/assets/plugins.js` is not modified.
- **Tree shape is flat marketplace → plugin** (Jesse's explicit decision) — not category- or component-type-grouped. Do not invent additional grouping levels.
- **The Marketplaces (add/remove/refresh) and Installed (enable/disable/upgrade/remove) sections stay unchanged** — this plan touches only the Browse section's rendering and the two handlers (`mkt-refresh`, `mkt-remove`) that must stop referencing the removed single-selection state.
- **Filter debounce is ~150ms** (verbatim from the design doc). Use the same real-timer debounce idiom already in `cmd/serf-hub/assets/search.js` (`function debounce(fn, ms) { let t; return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); }; }`), duplicated locally rather than imported (the design doc's sibling plan documents this exact kind of small, deliberate duplication for private per-file helpers).
- jstest is agent-run, not part of `make`/CI: from `cmd/serf-hub/jstest`, run a single test with `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node <file>.js`, or the full suite with `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`.
- `make lint` runs `serf-namingcheck` (JSON keys must be snake_case). This part adds no new JSON wire fields, so the check is not expected to fire — but run it once at the end as a regression gate anyway.
- Never `git add -A`; stage only the exact paths listed in each task's commit step (after a `git status`).
- **Out of scope for Part 1** (verbatim from the design doc): category- or component-type-grouped tree; folding Marketplaces/Installed into the tree; surfacing `SkippedPlugins` in the browse tree; per-plugin version/source in the browse row (the browse payload doesn't carry them today); and all of Part 2 (manifest-less plugin backend support), which is a separate, independent plan.

---

## File Structure

- `cmd/serf-hub/templates/partials/settings/plugins-manager.html` — the entire feature. State (`expandedMarketplaces`, `browseCatalogs` replace `browseName`/`browseCatalog`/`browseError`); `renderBrowseRow` gains `data-plugin`/`data-marketplace` on its `<li>`; a new `renderMarketplaceNode` renders one collapsible tree node; `renderBrowseSection` is rewritten around the tree (Task 1), then gains the filter `<input>` and empty/loading states (Task 2), then gains lazy-load-on-filter (Task 3); new functions `loadMarketplaceCatalog`, `toggleMarketplaceExpanded` (Task 1), `pluginMatchesFilter`, `applyFilter`, `debounce` (Task 2), `ensureAllCatalogsLoadedForFilter` (Task 3); `render()` gains filter-input focus/cursor preservation (Task 2); the `change` listener drops its `browse-marketplace-select` branch (Task 1); the click listener gains a `toggle-browse-marketplace` branch and the `install`/`mkt-refresh`/`mkt-remove` branches are updated to stop referencing the removed single-selection state (Task 1); a new `input` listener drives the filter (Task 2).
- `cmd/serf-hub/assets/style.css` — a new self-contained block (`.browse-marketplace-toggle`/`.browse-marketplace-chevron`/`.browse-marketplace-count`/`.browse-marketplace-plugins`/`.browse-filter`) appended after the existing `.settings-collection-add .row-error[hidden]` rule (line 3670), reusing `.settings-collection`/`.settings-collection-row`/`.settings-collection-empty`/`.settings-collection-add` for everything else.
- `cmd/serf-hub/jstest/test-plugins-manager-browse-tree.js` (new, Task 1) — tree render, lazy-load-once-cached, per-node loading/error, inline install, marketplace-refresh cache invalidation, zero-marketplaces empty state.
- `cmd/serf-hub/jstest/test-plugins-manager-browse-filter.js` (new, Task 2) — filtering already-loaded catalogs, auto-expand/collapse, empty-result message, clear-restores-collapsed, focus/cursor preservation across the debounced re-render.
- `cmd/serf-hub/jstest/test-plugins-manager-browse-filter-lazyload.js` (new, Task 3) — first-keystroke lazy-load of not-yet-cached marketplaces, the loading affordance while pending, no re-fetch on later keystrokes.
- Not modified: `cmd/serf-hub/assets/plugins.js` (RPC wrappers are reused as-is), any Go file, any wire type.

---

**Cache invalidation on `serf/marketplace/updated` needs no new code.** The design doc requires the Browse cache to be invalidated "on the `serf/marketplace/updated` notification (re-fetch on next expand)" in addition to an explicit refresh. `cmd/serf-hub/assets/notifications.js:415-430` already handles this today for the whole pane: on `serf/marketplace/updated` (or `serf/plugin/updated`), if `/settings/plugins-manager` is the active settings tab, it does a full `htmx.ajax` reload of the partial, which re-evaluates the inline `<script>` from scratch — wiping `browseCatalogs`/`expandedMarketplaces` back to empty. So a mutation from another tab already forces a full re-render with a cold cache; the next expand re-fetches for free. No task below needs to touch `notifications.js`.

## Task 1 — Browse becomes a collapsible marketplace → plugin tree

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/plugins-manager.html` (state ~33-35, `renderBrowseRow` ~117-131, `renderBrowseSection` ~133-157, `refreshBrowse` ~230-239, `change` listener ~278-287, click listener's `install`/`mkt-refresh`/`mkt-remove` branches ~307-352)
- Modify: `cmd/serf-hub/assets/style.css` (new block after line 3670)
- Test: `cmd/serf-hub/jstest/test-plugins-manager-browse-tree.js` (new)

**Interfaces:**
- Consumes: `pluginsAdmin.marketplaceList()`, `pluginsAdmin.pluginList()`, `pluginsAdmin.marketplaceBrowse(name)`, `pluginsAdmin.pluginInstall(plugin, marketplace)`, `pluginsAdmin.marketplaceRefresh(name)`, `pluginsAdmin.marketplaceRemove(name)` (all existing, `cmd/serf-hub/assets/plugins.js:8-23`, unchanged).
- Produces: `expandedMarketplaces` (`Set<string>`), `browseCatalogs` (`{[name]: {loading:true} | {error:string} | {plugins:MarketplaceCatalogPlugin[]}}`), `loadMarketplaceCatalog(name)`, `toggleMarketplaceExpanded(name)`, `renderMarketplaceNode(m)` — all consumed by Task 2 and Task 3.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-plugins-manager-browse-tree.js`:
```js
// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM, mocks window.pluginsAdmin, and exercises the Browse tree (Part 1
// of the plugin-marketplace-improvements design): marketplace nodes collapsed
// by default, lazy-loaded catalogs on first expand (cached thereafter),
// per-node loading/error states, inline install, marketplace-refresh cache
// invalidation, and the zero-marketplaces empty state.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/settings/plugins-manager.html"),
  "utf8",
);
const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/);
if (!scriptMatch) { console.error("FAIL: plugins-manager.html should contain a <script> block"); process.exit(1); }
const src = scriptMatch[1];

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const tick = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));

function makeDom() {
  return new JSDOM(
    `<!DOCTYPE html><html><body>
      <div id="plugins-manager-root" data-loaded="false">
        <p class="settings-help">Loading…</p>
      </div>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/settings/plugins-manager" },
  );
}

async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("plugins-manager-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await tick(dom, 0);
  pass(root.dataset.loaded === "true", "plugins-manager pane should finish loading");
  return root;
}

(async function main() {
  // Case 1: two marketplaces render as collapsed nodes; expanding one
  // lazy-loads its catalog once (cached on re-expand); the other node is
  // unaffected by the first's error; install works from within the tree.
  {
    const dom = makeDom();
    const browseCalls = [];
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [
          { name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 },
          { name: "beta", source: { kind: "url", url: "https://example.com/beta.git" }, lastUpdated: 0 },
        ],
      }),
      pluginList: async () => ({
        plugins: [{ plugin: "already-installed", marketplace: "alpha", version: "1.0.0", enabled: true }],
      }),
      marketplaceBrowse: async (name) => {
        browseCalls.push(name);
        if (name === "alpha") {
          return { name: "alpha", plugins: [
            { name: "already-installed", description: "Already on the box" },
            { name: "fresh-plugin", description: "Not installed yet", category: "tools" },
          ] };
        }
        throw new Error("network down");
      },
      marketplaceRefresh: async () => ({}),
      pluginInstall: async () => ({}),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    const nodes = doc.querySelectorAll("#browse-section .browse-marketplace-node");
    pass(nodes.length === 2, "both marketplaces render as tree nodes, got " + nodes.length);
    pass(browseCalls.length === 0, "no catalog is fetched before any node is expanded, got " + browseCalls.length);
    pass(doc.querySelectorAll("#browse-section .browse-marketplace-plugins").length === 0,
      "collapsed nodes render no plugin list");

    const alphaToggle = doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]');
    alphaToggle.click();
    await tick(dom, 20);
    pass(browseCalls.filter(n => n === "alpha").length === 1, "expanding alpha fetches its catalog exactly once");
    pass(alphaToggle.getAttribute("aria-expanded") === "true", "alpha toggle reports aria-expanded=true");
    let alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
    pass(alphaRows.length === 2, "alpha shows both catalog plugins, got " + alphaRows.length);
    const installedRow = Array.from(alphaRows).find(r => r.textContent.includes("already-installed"));
    pass(installedRow && installedRow.querySelector(".status-badge"), "already-installed plugin shows the Installed badge");
    const freshRow = Array.from(alphaRows).find(r => r.textContent.includes("fresh-plugin"));
    pass(freshRow && freshRow.querySelector('button[data-action="install"]'), "fresh-plugin shows an Install button");

    // Collapse then re-expand alpha: no second fetch, cached rows reappear.
    alphaToggle.click();
    await tick(dom, 20);
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 0,
      "collapsing alpha hides its rows");
    alphaToggle.click();
    await tick(dom, 20);
    pass(browseCalls.filter(n => n === "alpha").length === 1, "re-expanding alpha does not re-fetch, still 1 call");
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 2,
      "re-expanding alpha shows the cached rows");

    // Expand beta: its browse call fails; alpha's already-rendered rows are unaffected.
    const betaToggle = doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]');
    betaToggle.click();
    await tick(dom, 20);
    const betaBody = doc.querySelector('.browse-marketplace-node[data-marketplace-node="beta"] .browse-marketplace-plugins');
    pass(betaBody && /Failed to browse: network down/.test(betaBody.textContent), "beta shows its own inline error");
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 2,
      "alpha's rows are unaffected by beta's error");

    // Installing fresh-plugin from the tree calls pluginInstall(plugin, marketplace)
    // and flips the row to Installed.
    let installArgs = null;
    dom.window.pluginsAdmin.pluginInstall = async (plugin, marketplace) => { installArgs = [plugin, marketplace]; return {}; };
    dom.window.pluginsAdmin.pluginList = async () => ({
      plugins: [
        { plugin: "already-installed", marketplace: "alpha", version: "1.0.0", enabled: true },
        { plugin: "fresh-plugin", marketplace: "alpha", version: "1.0.0", enabled: true },
      ],
    });
    doc.querySelector('.browse-marketplace-node[data-marketplace-node="alpha"] button[data-action="install"]').click();
    await tick(dom, 20);
    pass(installArgs && installArgs[0] === "fresh-plugin" && installArgs[1] === "alpha",
      "install calls pluginInstall with (plugin, marketplace) = " + JSON.stringify(installArgs));
    alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
    const freshRowAfter = Array.from(alphaRows).find(r => r.textContent.includes("fresh-plugin"));
    pass(freshRowAfter && freshRowAfter.querySelector(".status-badge"), "fresh-plugin flips to Installed after install");
  }

  // Case 2: refreshing a marketplace from the Marketplaces section invalidates
  // its Browse cache; the next fetch happens immediately if the node is
  // currently expanded.
  {
    const dom = makeDom();
    let browseCalls = 0;
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [{ name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 }],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async () => { browseCalls++; return { name: "alpha", plugins: [{ name: "p1", description: "" }] }; },
      marketplaceRefresh: async () => ({}),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
    await tick(dom, 20);
    pass(browseCalls === 1, "initial expand fetches once, got " + browseCalls);

    doc.querySelector('#marketplaces-section [data-marketplace="alpha"] button[data-action="mkt-refresh"]').click();
    await tick(dom, 20);
    pass(browseCalls === 2, "refreshing an expanded marketplace re-fetches its catalog, calls=" + browseCalls);
  }

  // Case 3: zero marketplaces registered shows the empty-state guidance.
  {
    const dom = makeDom();
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({ marketplaces: [] }),
      pluginList: async () => ({ plugins: [] }),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;
    pass(/No marketplaces registered/.test(doc.querySelector("#browse-section").textContent),
      "zero marketplaces shows the empty-state guidance in Browse");
  }

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse tree");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-tree.js` → expect FAIL (`#browse-marketplace-select` still exists; no `.browse-marketplace-node`/`.browse-marketplace-toggle` elements; `pluginsAdmin.marketplaceBrowse` is never called since nothing drives it without a selection).

- [ ] **Implement — state, row, and tree render.** In `cmd/serf-hub/templates/partials/settings/plugins-manager.html`, replace the state block:
```js
    // ---- state ----
    let marketplaces = [];   // last serf/marketplace/list result
    let installed = [];      // last serf/plugin/list result
    let browseName = "";     // marketplace currently selected in Browse, "" = none
    let browseCatalog = null; // last serf/marketplace/browse result for browseName
    let browseError = "";
    let addMarketplaceOpen = false;
    let loadError = "";
```
with:
```js
    // ---- state ----
    let marketplaces = [];   // last serf/marketplace/list result
    let installed = [];      // last serf/plugin/list result
    // Browse is a tree: every registered marketplace is a node, expanded
    // on demand. expandedMarketplaces tracks which nodes are open;
    // browseCatalogs caches each marketplace's serf/marketplace/browse
    // result (or its loading/error state) so re-expanding never re-fetches.
    let expandedMarketplaces = new Set();
    let browseCatalogs = {}; // marketplace name -> {loading:true} | {error} | {plugins}
    let addMarketplaceOpen = false;
    let loadError = "";
```
Replace `renderBrowseRow` (unchanged behavior, but the `<li>` now carries `data-plugin`/`data-marketplace` — mirroring `installedRow`'s convention — so install no longer needs a globally-selected marketplace):
```js
    function renderBrowseRow(p, marketplace) {
      const already = isInstalled(p.name, marketplace);
      return `
        <li class="settings-collection-row" data-plugin="${escapeHtml(p.name)}" data-marketplace="${escapeHtml(marketplace)}">
          <div>
            <div class="row-text">${escapeHtml(p.name)}</div>
            <div class="row-meta">${escapeHtml(p.description || "")}${p.category ? " · " + escapeHtml(p.category) : ""}</div>
          </div>
          <div class="row-actions">
            ${already
              ? `<span class="status-badge" data-state="idle">Installed</span>`
              : `<button type="button" class="btn btn-primary" data-action="install">Install</button>`}
          </div>
        </li>`;
    }

    function renderMarketplaceNode(m) {
      const name = m.name;
      const expanded = expandedMarketplaces.has(name);
      const cache = browseCatalogs[name];
      const countLabel = (cache && cache.plugins) ? ` <span class="browse-marketplace-count">(${cache.plugins.length})</span>` : "";
      let childrenHtml = "";
      if (expanded) {
        let body;
        if (cache && cache.loading) {
          body = `<li class="settings-collection-empty">Loading…</li>`;
        } else if (cache && cache.error) {
          body = `<li class="settings-collection-empty">Failed to browse: ${escapeHtml(cache.error)}</li>`;
        } else if (cache && cache.plugins) {
          body = cache.plugins.length
            ? cache.plugins.map(p => renderBrowseRow(p, name)).join("")
            : `<li class="settings-collection-empty">This marketplace has no plugins.</li>`;
        } else {
          body = `<li class="settings-collection-empty">Loading…</li>`;
        }
        childrenHtml = `<ul class="settings-collection-list browse-marketplace-plugins" role="list">${body}</ul>`;
      }
      return `
        <li class="browse-marketplace-node" data-marketplace-node="${escapeHtml(name)}">
          <button type="button" class="browse-marketplace-toggle" data-action="toggle-browse-marketplace"
            data-marketplace="${escapeHtml(name)}" aria-expanded="${expanded}">
            <span class="browse-marketplace-chevron" aria-hidden="true">›</span>${escapeHtml(name)}${countLabel}
          </button>
          ${childrenHtml}
        </li>`;
    }
```
Replace `renderBrowseSection`:
```js
    function renderBrowseSection() {
      const treeBody = marketplaces.length === 0
        ? `<li class="settings-collection-empty">No marketplaces registered. Add one above to browse plugins.</li>`
        : marketplaces.map(renderMarketplaceNode).join("");
      return `
        <section class="settings-collection" id="browse-section">
          <header class="settings-collection-head">
            <h3>Browse</h3>
          </header>
          <ul class="settings-collection-list browse-tree" role="list">${treeBody}</ul>
        </section>`;
    }
```

- [ ] **Implement — lazy-load, toggle, and event wiring.** Replace `refreshBrowse`:
```js
    async function refreshBrowse() {
      if (!browseName) { browseCatalog = null; browseError = ""; return; }
      try {
        browseCatalog = await pluginsAdmin.marketplaceBrowse(browseName);
        browseError = "";
      } catch (err) {
        browseCatalog = null;
        browseError = (err && err.message) ? err.message : String(err);
      }
    }
```
with:
```js
    // Fetches one marketplace's catalog and caches the result (or the error),
    // then re-renders. Callers that need the fetch to have settled before
    // continuing (e.g. a refresh of a currently-expanded node) should await it.
    async function loadMarketplaceCatalog(name) {
      try {
        const resp = await pluginsAdmin.marketplaceBrowse(name);
        browseCatalogs[name] = { plugins: resp.plugins || [] };
      } catch (err) {
        browseCatalogs[name] = { error: (err && err.message) ? err.message : String(err) };
      }
      render();
    }

    function toggleMarketplaceExpanded(name) {
      if (expandedMarketplaces.has(name)) {
        expandedMarketplaces.delete(name);
        render();
        return;
      }
      expandedMarketplaces.add(name);
      if (!browseCatalogs[name]) {
        browseCatalogs[name] = { loading: true };
        loadMarketplaceCatalog(name);
      }
      render();
    }
```
In the `change` listener, remove the now-dead `browse-marketplace-select` branch:
```js
    root.addEventListener("change", (ev) => {
      if (ev.target && ev.target.id === "browse-marketplace-select") {
        browseName = ev.target.value;
        refreshBrowse().then(render);
        return;
      }
      if (ev.target && ev.target.name === "mkt-kind") {
        updateAddMarketplaceKindFields(ev.target.closest("form"));
      }
    });
```
becomes:
```js
    root.addEventListener("change", (ev) => {
      if (ev.target && ev.target.name === "mkt-kind") {
        updateAddMarketplaceKindFields(ev.target.closest("form"));
      }
    });
```
In the click listener, add the tree-toggle branch right after `const action = btn.dataset.action;`:
```js
      if (action === "toggle-browse-marketplace") {
        toggleMarketplaceExpanded(btn.dataset.marketplace);
        return;
      }
```
Replace the `install` branch (it no longer takes the plugin name off the button, and no longer reads the removed `browseName`):
```js
      if (action === "install") {
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.pluginInstall(btn.dataset.plugin, browseName);
            await refreshInstalled();
            render();
            if (window.SerfToast) window.SerfToast.show("Installed " + btn.dataset.plugin, "success");
          } catch (err) {
            toastError("Install", err);
          }
        });
        return;
      }
```
becomes:
```js
      if (action === "install") {
        const row = btn.closest("[data-plugin]");
        const plugin = row.dataset.plugin;
        const marketplace = row.dataset.marketplace;
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.pluginInstall(plugin, marketplace);
            await refreshInstalled();
            render();
            if (window.SerfToast) window.SerfToast.show("Installed " + plugin, "success");
          } catch (err) {
            toastError("Install", err);
          }
        });
        return;
      }
```
Replace the `mkt-refresh` branch (invalidates that marketplace's Browse cache; re-fetches immediately only if it's currently expanded):
```js
      if (mktRow && action === "mkt-refresh") {
        const name = mktRow.dataset.marketplace;
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.marketplaceRefresh(name);
            await refreshMarketplaces();
            if (name === browseName) await refreshBrowse();
            render();
            if (window.SerfToast) window.SerfToast.show("Refreshed " + name, "success");
          } catch (err) {
            toastError("Refresh", err);
          }
        });
        return;
      }
```
becomes:
```js
      if (mktRow && action === "mkt-refresh") {
        const name = mktRow.dataset.marketplace;
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.marketplaceRefresh(name);
            await refreshMarketplaces();
            delete browseCatalogs[name];
            if (expandedMarketplaces.has(name)) {
              browseCatalogs[name] = { loading: true };
              await loadMarketplaceCatalog(name);
            } else {
              render();
            }
            if (window.SerfToast) window.SerfToast.show("Refreshed " + name, "success");
          } catch (err) {
            toastError("Refresh", err);
          }
        });
        return;
      }
```
Replace the `mkt-remove` branch (drops the removed `browseName`/`browseCatalog`/`browseError` reset in favor of clearing that marketplace's tree state):
```js
      if (mktRow && action === "mkt-remove") {
        const name = mktRow.dataset.marketplace;
        if (!confirm(`Remove marketplace "${name}"? Installed plugins from it are unaffected.`)) return;
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.marketplaceRemove(name);
            if (name === browseName) { browseName = ""; browseCatalog = null; browseError = ""; }
            await refreshMarketplaces();
            render();
            if (window.SerfToast) window.SerfToast.show("Removed marketplace " + name, "success");
          } catch (err) {
            toastError("Remove marketplace", err);
          }
        });
        return;
      }
```
becomes:
```js
      if (mktRow && action === "mkt-remove") {
        const name = mktRow.dataset.marketplace;
        if (!confirm(`Remove marketplace "${name}"? Installed plugins from it are unaffected.`)) return;
        await withBusy(btn, async () => {
          try {
            await pluginsAdmin.marketplaceRemove(name);
            delete browseCatalogs[name];
            expandedMarketplaces.delete(name);
            await refreshMarketplaces();
            render();
            if (window.SerfToast) window.SerfToast.show("Removed marketplace " + name, "success");
          } catch (err) {
            toastError("Remove marketplace", err);
          }
        });
        return;
      }
```

- [ ] **Implement CSS.** In `cmd/serf-hub/assets/style.css`, immediately after the existing `.settings-collection-add .row-error[hidden] { display: none; }` rule (line 3670), add:
```css

/* ── Browse tree (marketplace → plugin, plugin-marketplace-improvements Part 1) ── */
.browse-marketplace-toggle {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  background: none;
  border: none;
  padding: var(--space-3) 0;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
  text-align: left;
  cursor: pointer;
}
.browse-marketplace-chevron {
  display: inline-block;
  transition: transform 0.1s ease;
  color: var(--text-muted);
}
.browse-marketplace-toggle[aria-expanded="true"] .browse-marketplace-chevron { transform: rotate(90deg); }
.browse-marketplace-count { color: var(--text-muted); font-size: var(--text-xs); }
.browse-marketplace-plugins { padding-left: var(--space-5); }
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-tree.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` (full suite — confirm no other test referenced `#browse-marketplace-select` or the old single-selection Browse shape) → all green.
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/plugins-manager.html cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-plugins-manager-browse-tree.js` → `feat(plugins-ui): Browse becomes a lazy-loaded marketplace → plugin tree`.

---

## Task 2 — Filter box: search already-loaded catalogs

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/plugins-manager.html` (`renderBrowseSection`, `render()`, add `input` listener)
- Modify: `cmd/serf-hub/assets/style.css` (append `.browse-filter`)
- Test: `cmd/serf-hub/jstest/test-plugins-manager-browse-filter.js` (new)

**Interfaces:**
- Consumes: `browseCatalogs`, `expandedMarketplaces`, `renderMarketplaceNode` (Task 1).
- Produces: `filterQuery` (`string`), `pluginMatchesFilter(p, query)`, `applyFilter()`, module-local `debounce(fn, ms)` — `applyFilter` and `filterQuery` are extended by Task 3.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-plugins-manager-browse-filter.js`:
```js
// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM and exercises the Browse tree's filter box (Part 1 of the
// plugin-marketplace-improvements design, Task 2): typing filters plugins
// across already-loaded marketplace catalogs (debounced ~150ms), matching
// marketplaces auto-expand while non-matching ones collapse, an empty result
// shows a "No plugins match" message, clearing the filter collapses the whole
// tree, and the input keeps focus/cursor position across the re-render the
// debounced filter triggers.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/settings/plugins-manager.html"),
  "utf8",
);
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const tick = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));

function makeDom() {
  return new JSDOM(
    `<!DOCTYPE html><html><body>
      <div id="plugins-manager-root" data-loaded="false">
        <p class="settings-help">Loading…</p>
      </div>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/settings/plugins-manager" },
  );
}

async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("plugins-manager-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await tick(dom, 0);
  return root;
}

function typeInFilter(doc, dom, value) {
  const input = doc.getElementById("browse-filter-input");
  input.focus();
  input.value = value;
  input.setSelectionRange(value.length, value.length);
  input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
}

(async function main() {
  const dom = makeDom();
  dom.window.pluginsAdmin = {
    marketplaceList: async () => ({
      marketplaces: [
        { name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 },
        { name: "beta", source: { kind: "url", url: "https://example.com/beta.git" }, lastUpdated: 0 },
      ],
    }),
    pluginList: async () => ({ plugins: [] }),
    marketplaceBrowse: async (name) => name === "alpha"
      ? { name: "alpha", plugins: [{ name: "elements-of-style", description: "Strunk's writing rules" }] }
      : { name: "beta", plugins: [{ name: "private-journal-mcp", description: "Journal MCP server" }] },
  };
  dom.window.eval(src);
  await waitLoaded(dom);
  const doc = dom.window.document;

  // Preload both catalogs (Task 2 filters already-loaded catalogs only).
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]').click();
  await tick(dom, 20);

  // A name match: beta (no match) collapses, alpha (match) stays/auto-expands
  // and shows only the matching row.
  typeInFilter(doc, dom, "elements");
  await tick(dom, 170); // past the 150ms debounce
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "true",
    "alpha (has a match) stays/auto-expands");
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]').getAttribute("aria-expanded") === "false",
    "beta (no match) collapses");
  let alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
  pass(alphaRows.length === 1 && alphaRows[0].textContent.includes("elements-of-style"),
    "alpha shows only the matching row");

  // A description match also auto-expands.
  typeInFilter(doc, dom, "journal");
  await tick(dom, 170);
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]').getAttribute("aria-expanded") === "true",
    "a description match auto-expands beta");
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "false",
    "alpha (no longer matching) collapses");

  // No matches anywhere: the global empty-result message, no tree nodes.
  typeInFilter(doc, dom, "zzz-nothing-matches");
  await tick(dom, 170);
  pass(/No plugins match "zzz-nothing-matches"/.test(doc.getElementById("browse-section").textContent),
    "empty filter result shows the no-matches message");
  pass(doc.querySelectorAll(".browse-marketplace-node").length === 0,
    "no marketplace nodes render when nothing matches");

  // Clearing the filter restores the fully collapsed tree.
  typeInFilter(doc, dom, "");
  await tick(dom, 170);
  const toggles = doc.querySelectorAll(".browse-marketplace-toggle");
  pass(toggles.length === 2, "clearing the filter shows both marketplace nodes again");
  pass(Array.from(toggles).every(t => t.getAttribute("aria-expanded") === "false"),
    "clearing the filter collapses the whole tree");

  // Focus/cursor survive the debounced re-render (the filter input must not
  // be recreated wholesale mid-type, or render() would steal focus).
  typeInFilter(doc, dom, "ele");
  await tick(dom, 170);
  const liveInput = doc.getElementById("browse-filter-input");
  pass(doc.activeElement === liveInput, "the filter input keeps focus after the debounced re-render");
  pass(liveInput.selectionStart === 3 && liveInput.selectionEnd === 3, "cursor stays at the end of the typed text");

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse filter");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-filter.js` → expect FAIL (no `#browse-filter-input` exists yet).

- [ ] **Implement — filter state, matching, and render.** In `cmd/serf-hub/templates/partials/settings/plugins-manager.html`, add to the state block (right after `browseCatalogs`):
```js
    let filterQuery = ""; // Browse tree filter box's current value
```
Add near `isInstalled`:
```js
    function pluginMatchesFilter(p, query) {
      const q = query.toLowerCase();
      return p.name.toLowerCase().includes(q) || (p.description || "").toLowerCase().includes(q);
    }

    // anyPluginsMatch reports whether any already-loaded catalog has a match,
    // independent of expandedMarketplaces bookkeeping — used only to decide
    // whether to show the global "No plugins match" message.
    function anyPluginsMatch(query) {
      return marketplaces.some(m => {
        const cache = browseCatalogs[m.name];
        return !!(cache && cache.plugins && cache.plugins.some(p => pluginMatchesFilter(p, query)));
      });
    }

    // applyFilter auto-expands every marketplace with a match in its
    // already-loaded catalog and collapses every marketplace without one.
    // Marketplaces not yet loaded are left collapsed here (Task 3 adds
    // lazy-loading them on the first filter keystroke).
    function applyFilter() {
      const q = filterQuery.trim();
      if (!q) {
        expandedMarketplaces = new Set();
        render();
        return;
      }
      marketplaces.forEach(m => {
        const cache = browseCatalogs[m.name];
        const matches = !!(cache && cache.plugins && cache.plugins.some(p => pluginMatchesFilter(p, q)));
        if (matches) expandedMarketplaces.add(m.name);
        else expandedMarketplaces.delete(m.name);
      });
      render();
    }

    // debounce mirrors search.js's helper (private to that file's IIFE, so
    // duplicated here rather than imported — the same small, deliberate
    // duplication search.js/spawn.js already use for their own shared bits).
    function debounce(fn, ms) {
      let t;
      return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
    }
    const debouncedApplyFilter = debounce(() => { applyFilter(); }, 150);
```
Replace `renderBrowseSection`:
```js
    function renderBrowseSection() {
      const treeBody = marketplaces.length === 0
        ? `<li class="settings-collection-empty">No marketplaces registered. Add one above to browse plugins.</li>`
        : marketplaces.map(renderMarketplaceNode).join("");
      return `
        <section class="settings-collection" id="browse-section">
          <header class="settings-collection-head">
            <h3>Browse</h3>
          </header>
          <ul class="settings-collection-list browse-tree" role="list">${treeBody}</ul>
        </section>`;
    }
```
becomes:
```js
    function renderBrowseSection() {
      const q = filterQuery.trim();
      let treeBody;
      if (marketplaces.length === 0) {
        treeBody = `<li class="settings-collection-empty">No marketplaces registered. Add one above to browse plugins.</li>`;
      } else if (q && !anyPluginsMatch(q)) {
        treeBody = `<li class="settings-collection-empty">No plugins match "${escapeHtml(q)}".</li>`;
      } else {
        treeBody = marketplaces.map(renderMarketplaceNode).join("");
      }
      return `
        <section class="settings-collection" id="browse-section">
          <header class="settings-collection-head">
            <h3>Browse</h3>
          </header>
          <div class="settings-collection-add browse-filter">
            <input type="text" id="browse-filter-input" class="val-input" placeholder="Filter plugins…" value="${escapeHtml(filterQuery)}">
          </div>
          <ul class="settings-collection-list browse-tree" role="list">${treeBody}</ul>
        </section>`;
    }
```
In `render()`, save and restore the filter input's focus/cursor around the `innerHTML` replace (the filter box is the first `<input>` in this pane that re-renders live, on every debounced keystroke — without this, `render()` would silently steal focus mid-type):
```js
    function render() {
      if (loadError) {
        root.innerHTML = `<p class="settings-error">Failed to load: ${escapeHtml(loadError)}</p>`;
        return;
      }
      root.innerHTML = renderMarketplacesSection() + renderBrowseSection() + renderInstalledSection();
      root.dataset.loaded = "true";
      if (window.SettingsPickers) window.SettingsPickers.init(root);
      if (addMarketplaceOpen) {
        const nameInput = root.querySelector("#mkt-name-input");
        if (nameInput) nameInput.focus();
      }
    }
```
becomes:
```js
    function render() {
      if (loadError) {
        root.innerHTML = `<p class="settings-error">Failed to load: ${escapeHtml(loadError)}</p>`;
        return;
      }
      const filterHadFocus = document.activeElement && document.activeElement.id === "browse-filter-input";
      const priorFilterInput = root.querySelector("#browse-filter-input");
      const selStart = priorFilterInput ? priorFilterInput.selectionStart : null;
      const selEnd = priorFilterInput ? priorFilterInput.selectionEnd : null;
      root.innerHTML = renderMarketplacesSection() + renderBrowseSection() + renderInstalledSection();
      root.dataset.loaded = "true";
      if (window.SettingsPickers) window.SettingsPickers.init(root);
      if (addMarketplaceOpen) {
        const nameInput = root.querySelector("#mkt-name-input");
        if (nameInput) nameInput.focus();
      }
      if (filterHadFocus) {
        const newFilterInput = root.querySelector("#browse-filter-input");
        if (newFilterInput) {
          newFilterInput.focus();
          if (selStart != null) newFilterInput.setSelectionRange(selStart, selEnd);
        }
      }
    }
```
Add a new `input` listener (alongside the existing `change`/`click`/`submit` listeners):
```js
    root.addEventListener("input", (ev) => {
      if (ev.target && ev.target.id === "browse-filter-input") {
        filterQuery = ev.target.value;
        debouncedApplyFilter();
      }
    });
```

- [ ] **Implement CSS.** In `cmd/serf-hub/assets/style.css`, immediately after the `.browse-marketplace-plugins` rule added in Task 1, add:
```css
.browse-filter { border-top: none; padding-top: 0; }
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-filter.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-tree.js` (Task 1's test — no regression) then `sh run-all.sh` (full suite) → all green.
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/plugins-manager.html cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-plugins-manager-browse-filter.js` → `feat(plugins-ui): filter the Browse tree across already-loaded marketplace catalogs`.

---

## Task 3 — Filter lazy-loads not-yet-loaded catalogs on first keystroke

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/plugins-manager.html` (`renderBrowseSection`, the `input` listener's debounced callback)
- Test: `cmd/serf-hub/jstest/test-plugins-manager-browse-filter-lazyload.js` (new)

**Interfaces:**
- Consumes: `browseCatalogs`, `marketplaces`, `pluginsAdmin.marketplaceBrowse` (Task 1); `applyFilter`, `filterQuery` (Task 2).
- Produces: `filterLoading` (`boolean`), `ensureAllCatalogsLoadedForFilter()`.

- [ ] **Failing test** — create `cmd/serf-hub/jstest/test-plugins-manager-browse-filter-lazyload.js`:
```js
// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM and exercises the Browse filter's lazy-load-on-first-keystroke
// behavior (Part 1 of the plugin-marketplace-improvements design, Task 3):
// the first non-empty filter keystroke fetches every not-yet-loaded
// marketplace's catalog once (showing a "Loading marketplaces…" affordance
// meanwhile), then filters over the now-cached data; a later keystroke
// filters the cache with no further RPCs.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/settings/plugins-manager.html"),
  "utf8",
);
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const tick = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));

function makeDom() {
  return new JSDOM(
    `<!DOCTYPE html><html><body>
      <div id="plugins-manager-root" data-loaded="false">
        <p class="settings-help">Loading…</p>
      </div>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/settings/plugins-manager" },
  );
}

async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("plugins-manager-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await tick(dom, 0);
  return root;
}

function typeInFilter(doc, dom, value) {
  const input = doc.getElementById("browse-filter-input");
  input.value = value;
  input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
}

(async function main() {
  // Case 1: first keystroke fetches every not-yet-loaded marketplace once,
  // then filters over the (now cached) data; a later keystroke does not
  // re-fetch.
  {
    const dom = makeDom();
    const browseCalls = [];
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [
          { name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 },
          { name: "beta", source: { kind: "url", url: "https://example.com/beta.git" }, lastUpdated: 0 },
        ],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async (name) => {
        browseCalls.push(name);
        if (name === "alpha") return { name: "alpha", plugins: [{ name: "elements-of-style", description: "" }] };
        return { name: "beta", plugins: [{ name: "private-journal-mcp", description: "" }] };
      },
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    pass(browseCalls.length === 0, "no catalogs are fetched before the filter is used");

    typeInFilter(doc, dom, "elements");
    await tick(dom, 170); // past the 150ms debounce
    pass(browseCalls.includes("alpha") && browseCalls.includes("beta"),
      "first filter keystroke fetches every not-yet-loaded marketplace, got " + JSON.stringify(browseCalls));
    pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "true",
      "alpha auto-expands once its (now-loaded) catalog matches the query");
    pass(!/Loading marketplaces/.test(doc.getElementById("browse-section").textContent),
      "the loading-marketplaces affordance clears once every fetch settles");

    const callsBefore = browseCalls.length;
    typeInFilter(doc, dom, "element");
    await tick(dom, 170);
    pass(browseCalls.length === callsBefore, "a later keystroke filters the cache with no new RPCs, calls=" + browseCalls.length);
  }

  // Case 2: while a not-yet-loaded catalog's fetch is still pending, the tree
  // shows the "Loading marketplaces…" affordance.
  {
    const dom = makeDom();
    let resolvePending;
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [{ name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 }],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async () => new Promise((resolve) => {
        resolvePending = () => resolve({ name: "alpha", plugins: [] });
      }),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    typeInFilter(doc, dom, "x");
    await tick(dom, 170);
    pass(/Loading marketplaces…/.test(doc.getElementById("browse-section").textContent),
      "while a not-yet-loaded catalog is still fetching, the tree shows the loading-marketplaces affordance");

    resolvePending();
    await tick(dom, 20);
    pass(!/Loading marketplaces…/.test(doc.getElementById("browse-section").textContent),
      "the affordance clears once the fetch settles");
  }

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse filter lazy-load");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-filter-lazyload.js` → expect FAIL (typing never calls `marketplaceBrowse` for uncached marketplaces; no "Loading marketplaces…" text exists).

- [ ] **Implement.** In `cmd/serf-hub/templates/partials/settings/plugins-manager.html`, add to the state block (right after `filterQuery`):
```js
    let filterLoading = false; // true while the filter's first-keystroke lazy-load is in flight
```
Add near `applyFilter`:
```js
    // ensureAllCatalogsLoadedForFilter fetches every marketplace the filter
    // hasn't cached yet, once, in parallel. A ref no longer needed (query
    // cleared or changed again) mid-flight is not cancelled — the fetch
    // still completes and populates the cache, which is harmless and simpler
    // than a cancellation token (not required by the design doc).
    async function ensureAllCatalogsLoadedForFilter() {
      const missing = marketplaces.filter(m => !browseCatalogs[m.name]).map(m => m.name);
      if (missing.length === 0) return;
      filterLoading = true;
      render();
      await Promise.all(missing.map(name =>
        pluginsAdmin.marketplaceBrowse(name).then(
          resp => { browseCatalogs[name] = { plugins: resp.plugins || [] }; },
          err => { browseCatalogs[name] = { error: (err && err.message) ? err.message : String(err) }; },
        )
      ));
      filterLoading = false;
    }
```
Change `debouncedApplyFilter`'s callback from:
```js
    const debouncedApplyFilter = debounce(() => { applyFilter(); }, 150);
```
to:
```js
    const debouncedApplyFilter = debounce(async () => {
      const q = filterQuery.trim();
      if (!q) { applyFilter(); return; } // applyFilter() itself handles the clear-restores-collapsed case
      await ensureAllCatalogsLoadedForFilter();
      applyFilter();
    }, 150);
```
In `renderBrowseSection`, add the loading-affordance branch ahead of the empty-result check:
```js
      if (marketplaces.length === 0) {
        treeBody = `<li class="settings-collection-empty">No marketplaces registered. Add one above to browse plugins.</li>`;
      } else if (q && !anyPluginsMatch(q)) {
        treeBody = `<li class="settings-collection-empty">No plugins match "${escapeHtml(q)}".</li>`;
      } else {
        treeBody = marketplaces.map(renderMarketplaceNode).join("");
      }
```
becomes:
```js
      if (marketplaces.length === 0) {
        treeBody = `<li class="settings-collection-empty">No marketplaces registered. Add one above to browse plugins.</li>`;
      } else if (q && filterLoading) {
        treeBody = `<li class="settings-collection-empty">Loading marketplaces…</li>`;
      } else if (q && !anyPluginsMatch(q)) {
        treeBody = `<li class="settings-collection-empty">No plugins match "${escapeHtml(q)}".</li>`;
      } else {
        treeBody = marketplaces.map(renderMarketplaceNode).join("");
      }
```

- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-plugins-manager-browse-filter-lazyload.js` → PASS.
- [ ] **Run** `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` (full suite, including Task 1's and Task 2's plugins-manager tests — no regression) → all green.
- [ ] **Run** `make lint` from the repo root → 0 issues (no new JSON wire fields were added in Part 1, so `serf-namingcheck` is not expected to flag anything; this is a regression gate).
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/settings/plugins-manager.html cmd/serf-hub/jstest/test-plugins-manager-browse-filter-lazyload.js` → `feat(plugins-ui): the Browse filter lazy-loads uncached marketplace catalogs on first use`.

---

## Testing (shared, Part 1 scope)

Per the design doc's Testing section, Part 1's coverage is entirely jstest (`cmd/serf-hub/jstest`): tree render from `serf/marketplace/list`, expand → plugins from `serf/marketplace/browse`; lazy-load (browse called only on first expand, cached after); per-node loading + error states; install-state badge vs. Install button (against a mocked `serf/plugin/list`); inline install flips the row; the filter (matches across marketplaces by name/description, auto-expand, empty-result, clear restores collapsed). All three new test files mock the RPC layer via `window.pluginsAdmin`, matching the existing pattern in `cmd/serf-hub/jstest/test-credentials.js` (load the partial's inline `<script>`, mock the admin object it calls into, eval into JSDOM). No Go tests are needed for Part 1 — there is no Go change.

## Out of scope (Part 1, verbatim from the design doc)

- Category- or component-type-grouped tree (flat marketplace → plugin only).
- Folding Marketplaces/Installed into the tree — they stay separate sections.
- Surfacing `SkippedPlugins` in the browse tree.
- Per-plugin version/source in the browse row (the browse payload doesn't carry them today).
- Part 2 (manifest-less plugin backend support) — a fully separate plan; this plan makes no Go changes.
