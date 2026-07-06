# Plugin marketplace improvements — design

Date: 2026-07-06
Status: approved (Jesse, 2026-07-06 brainstorm); implementation pending plan + SDD.
Base: branch `plugin-marketplace-improvements` off main. Code anchors verified 2026-07-06.

## Problem

Two plugin-marketplace pain points, raised together:

1. **Browsing is a flat dropdown.** The web plugins pane's "Browse" section is a single
   `<select>` of marketplaces; picking one populates a flat `<ul>` of that marketplace's
   plugins. You see one marketplace at a time and can't scan across them.
2. **Installing a manifest-less plugin crashes.** `agent/plugin/plugin.go` `Load` hard-fails
   when a plugin source has neither `.claude-plugin/plugin.json` nor `.codex-plugin/plugin.json`,
   with a misleading error (it names the `.codex-plugin` path — the last tried — and the install
   path then deletes the cache dir). Real cause: some marketplace plugins (e.g.
   `private-journal-mcp` in `superpowers-marketplace`) are bare MCP/npm packages with no manifest
   in the source; Claude Code treats the **marketplace entry** as the manifest in that case
   (`"strict": true`), but serf drops the entry's manifest-shaped fields and can't install them.

## Decisions (Jesse, 2026-07-06)

1. **Tree shape:** flat **marketplace → plugin** — a tree of all registered marketplaces you
   expand to see their plugins (not category- or type-grouped).
2. **Tree scope:** **browse + install-state + inline install** per plugin row; the separate
   Marketplaces (add/remove/refresh) and Installed (enable/disable/upgrade/remove) sections stay.
3. **Search box:** yes — a filter above the tree to find plugins across all marketplaces.
4. **Manifest-less plugins:** **full support** — when the source has no `plugin.json`, honor the
   marketplace entry's embedded manifest fields so the plugin actually works.

---

## Part 1 — Browse becomes a marketplace → plugin tree

All UI, client-side, in `cmd/serf-hub/templates/partials/settings/plugins-manager.html` (the pane
is fully rendered by its inline `<script>`); RPCs via `cmd/serf-hub/assets/plugins.js`. No backend
change for Part 1 — it reuses the existing `serf/marketplace/list`, `serf/marketplace/browse`,
`serf/plugin/list`, `serf/plugin/install` RPCs.

### Structure

The "Browse" section (`renderBrowseSection()`, plugins-manager.html:133-157) becomes a tree:

```
─ Browse ──────────────────────────  [🔍 filter plugins…]
  ▸ superpowers-marketplace  (12)
  ▾ my-local                 (3)
      elements-of-style    Strunk's writing rules     ✓ Installed
      private-journal-mcp  Journal MCP server           Install
      some-plugin          …                            Install
```

- **Top level:** one collapsible node per registered marketplace (from `serf/marketplace/list`),
  showing the marketplace name + a plugin count once its catalog is loaded. Collapsed by default.
- **Expand → plugins:** the marketplace's plugins as rows, reusing the existing `renderBrowseRow`
  content (name, description, and — kept from today — the `· category` suffix when present).
- **Reuses the sidebar tree's interaction idioms** where cheap (expand/collapse chevron,
  keyboard-navigable), but this is a self-contained widget in the plugins pane, not the sidebar.

### Lazy-load

- A marketplace's catalog is fetched via `serf/marketplace/browse` (`pluginsAdmin.marketplaceBrowse`,
  plugins.js:9) on **first expand**, then cached client-side (keyed by marketplace name).
- While fetching: a small inline "loading…" under the node. On error: the existing
  "Failed to browse: …" text inline under that node (per-marketplace, non-fatal — other nodes
  still work).
- Rationale: browsing every marketplace on pane-open could be many RPCs; lazy-load keeps it snappy.
- Cache is invalidated for a marketplace on `serf/marketplace/refresh` of that marketplace and on
  the `serf/marketplace/updated` notification (re-fetch on next expand).

### Inline install + state

- Each plugin row shows install-state, computed client-side as today by cross-referencing
  `serf/plugin/list` (`isInstalled(plugin, marketplace)`, plugins-manager.html:39-41):
  a `✓ Installed` badge, or an **Install** button (`data-action="install"`, existing handler
  plugins-manager.html:307-319 calling `pluginInstall(plugin, marketplace)`).
- On install success: refresh the installed set + flip that row to Installed (existing flow), and
  toast (existing).
- The **Marketplaces** management section (`renderMarketplacesSection`) and **Installed**
  management section (`renderInstalledSection`, with enable/disable/upgrade/remove) are unchanged.

### Search / filter

- A filter input above the tree. Typing filters plugins across **all** marketplaces by name
  (and description) — matching marketplaces auto-expand and show only matching plugin rows;
  non-matching marketplaces collapse/hide. Clearing the filter restores the collapsed tree.
- To filter across marketplaces, filtering needs each marketplace's catalog loaded. Approach:
  on first non-empty filter keystroke, lazy-load any not-yet-loaded catalogs (one-time, with a
  subtle "loading marketplaces…" affordance), then filter client-side over the cached catalogs.
  Subsequent keystrokes filter the in-memory cache (no refetch). Debounce input (~150ms).
- Empty result: "No plugins match '<query>'."

### Part 1 edge cases

- Zero marketplaces registered: the tree shows the existing empty guidance ("Add a marketplace
  to browse plugins" style), consistent with today.
- A marketplace whose catalog has `SkippedPlugins` (unsupported source kinds, e.g. npm): out of
  scope to surface here (noted; today they're silently dropped) — keep current behavior.

---

## Part 2 — Manifest-less plugin support (honor the marketplace entry)

Backend/install change so a plugin whose source has no `plugin.json` installs and functions by
using its marketplace entry as the manifest — matching Claude Code's `strict:true` behavior.

### Root cause (confirmed)

`agent/plugin/plugin.go` `Load` (~lines 203-232) tries `.claude-plugin/plugin.json`, then
`.codex-plugin/plugin.json`, then hard-errors if neither exists. `internal/plugins/validate.go`
`validatePluginDir` (~14-17) treats any `Load` error as fatal; `internal/plugins/install.go`
`Install` (~109-114) propagates it and deletes the fetched cache dir. `internal/plugins/catalog.go`
`CatalogPlugin` (~17-24) only parses `Name/Description/Category/Homepage/Author/Source` — the
marketplace entry's manifest fields (`mcpServers`, `commands`, `agents`, `hooks`, `skills`,
`strict`) are silently dropped.

### Change

1. **Parse the entry's manifest fields.** Extend `CatalogPlugin` (and its JSON parse in
   `catalog.go`/`source.go`) to capture the Claude Code marketplace-entry manifest fields:
   `mcpServers`, `commands`, `agents`, `hooks`, `skills`, and `strict`. Verify the exact field
   names/shapes against the Claude Code marketplace schema (the same shapes as an in-plugin
   `plugin.json`'s component declarations) during the plan.
2. **Fallback in Load/install.** When the source dir has neither `.claude-plugin/plugin.json` nor
   `.codex-plugin/plugin.json`, synthesize the plugin's manifest from the catalog entry's fields
   (name from the entry, components from the entry's `mcpServers`/`commands`/`agents`/`hooks`/
   `skills`) instead of erroring. Plugins **with** a `plugin.json` are unchanged — the entry is
   only a fallback. The synthesized manifest must flow to the same place a parsed `plugin.json`
   does, so an entry-declared MCP server actually registers (the `private-journal-mcp` case).
   - Thread the catalog entry into the install/Load path so the fallback has the entry available
     (install knows the marketplace + plugin; `Load` currently takes only a dir — the plan decides
     whether to pass the entry into `Load` or synthesize a `plugin.json` into the cache dir before
     `Load`, so `Load` stays dir-only. Prefer the latter if it keeps `Load`'s contract clean.)
3. **Clear error when nothing's usable.** If there's no `plugin.json` **and** the entry declares
   no usable components, fail with an honest message ("<plugin>: source has no plugin manifest and
   the marketplace entry declares no components") — not the misleading `.codex-plugin` path — and
   avoid the confusing silent cache-delete-on-a-manifest-shaped error where feasible.

### Part 2 scope guard

- Honor only the component kinds serf already supports (mcpServers/commands/agents/hooks/skills as
  they map to the existing `agent/plugin` `Manifest`). Do not invent new component types.
- **`strict` semantics (corrected — verified against code.claude.com/docs/en/plugin-marketplaces):**
  `strict: true` (the default) means the source's `plugin.json` is the authority (entry only
  supplements); `strict: false` means the marketplace entry is the whole definition (no `plugin.json`
  needed). The earlier draft had this backwards. **The fallback here triggers purely on "the source
  has no `plugin.json`," independent of `Strict`'s value** — that's the actual failing condition.
  `Strict` is parsed and round-tripped for future `strict:false`-merge work, which is out of scope.
- **Reality check on the motivating example:** the live `superpowers-marketplace` entry for
  `private-journal-mcp` is `strict: true` with **no embedded manifest fields** — so there is nothing
  to synthesize from. This fix therefore turns its crash into the honest "no manifest, no usable
  entry components" error (§Change #3), and makes *other* manifest-less-with-embedded-fields plugins
  install and work. Auto-wrapping a bare npm/MCP package is explicitly out of scope.

---

## Testing

- **Part 1 (jstest, `cmd/serf-hub/jstest`):** tree render (marketplace nodes from `list`, expand →
  plugins from `browse`); lazy-load (browse called only on first expand, cached after); per-node
  loading + error states; install-state badge vs Install button (against a mocked `plugin/list`);
  inline install flips the row; the filter (matches across marketplaces, auto-expand, empty-result,
  clear restores). Mock the RPC layer as the existing plugins jstests do.
- **Part 2 (Go, agent + internal/plugins):** `Load`/install synthesizes a manifest from the catalog
  entry when the source has no `plugin.json` (with an `mcpServers` entry → the MCP server is
  declared/registered); a plugin **with** a `plugin.json` is unchanged (entry ignored); the clear
  error when neither source manifest nor usable entry fields exist; `CatalogPlugin` parses the new
  fields (round-trip test). If feasible, an install of a real manifest-less MCP plugin
  (`private-journal-mcp`-shaped fixture) end to end.
- Full gates: per-module `go test`, jstest, `golangci-lint cache clean && make lint` (0 issues).

## Out of scope

- Category- or component-type-grouped tree (Jesse chose flat marketplace→plugin).
- Folding Marketplaces/Installed management into the tree (they stay separate).
- Surfacing `SkippedPlugins` in the browse tree.
- `strict:false` entry-supplements-plugin.json merge (only manifest-absent → entry-authority).
- Per-plugin version/source in the browse row (browse payload doesn't carry them today).

## Estimate

Rough, in loc including tests: Part 1 (tree + lazy-load + inline install + filter, JS/template +
jstest) ~400–600; Part 2 (catalog field parse + Load/install fallback + clear error + Go tests)
~300–450. Total ~700–1,050. Two loosely-coupled parts (client UI vs backend install) that can be
built and reviewed independently.
