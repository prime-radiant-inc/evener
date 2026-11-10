# Plugins UI unification — design

Status: decided with Jesse, 2026-11-08. Visual mockups: `/dev/plugins-mockups`
(dev route, branch `plugins-ui-mockups`, commit `99cb522` and successors).
This doc records what the mockups show, the decisions behind them, and what
the implementation must build.

## Intent

The hub's plugins screens answer three questions with three different row
shapes and no component detail anywhere: what do I have installed, what could
I install, and what goes into a new session. This change makes one plugin
row and one plugin detail sheet serve all three surfaces, and makes the
expanded view show what a plugin actually contains — skills, agents,
commands, hooks, MCP servers — for installed plugins.

Jesse's asks, in his words:

1. "the expanded view of a plugin should show what's in the plugin — skills,
   mcps, agents, whatever"
2. "the 'to-install' and currently installed plugins should render from the
   same component, so they have shared ui, even if options like install,
   uninstall, auto-upgrade, and enabled are displayed contextually"
3. "use the same components for the session launcher's plugin list"

A later phase (explicitly out of scope here) unifies the Browse and Sources
screens into one marketplace surface. This design keeps the shared row and
sheet shaped so that step inherits everything.

## Goals

- One shared plugin row composition rendered by the Installed list, the
  Browse tree, and the session launcher's plugin list.
- One detail sheet with a Contents block (per-kind names + counts) between
  the description and the metadata, with contextual switches and footer
  actions per mode: installed, catalog (to-install), launcher.
- Counts visible in list rows, in the launcher's existing vocabulary
  ("7 skills · 9 MCP servers").
- The same hub read feeds the settings sheet and the launcher sheet.

## Non-goals

- No new product screens or navigation; the Installed/Browse/Sources
  segments, filters, help text, empty states, and confirm dialogs stay as
  they are.
- No marketplace merge (phase 2, above).
- No mobile-native work in this pass; the web sheet geometry (right side on
  desktop, bottom on mobile via `useIsMobile`) is unchanged.

## Anti-goals

- Breaking the design system to hand-roll rows: the unified row extends
  `ListRow`; it does not fork it.
- Changing any current guarantee: enabled/auto-upgrade semantics, upgrade
  availability, install/remove confirmations, launcher selection state and
  preview diagnostics.

## Decisions (Jesse, 2026-11-08)

1. **Install placement: both.** A browse row keeps an inline Install button
   and is also a tap target that opens the detail sheet. This requires
   `ListRow` to grow an interactive variant with a trailing control —
   approved by Jesse here, since the widget API is otherwise locked.
2. **Pre-install contents: deferred.** The to-install sheet shows catalog
   metadata only (description, author, category, marketplace, source). The
   Contents block is installed-only. The catalog index
   (`MarketplaceCatalogPlugin`) carries no component data today; a
   fetch-and-inspect read is the natural follow-up if wanted later.
3. **Counts in list rows: included.** Installed row meta reads
   `@ marketplace · v1.2.3 · 7 skills · 9 MCP servers`.
4. **Launcher rows: Switch rows with the shared vocabulary.** One-tap
   toggling unchanged; the row description speaks the shared
   source · counts language, and rows open the same detail sheet for
   contents. The drill-in-row alternative (toggle in the sheet) was rejected.

## Approaches considered

- **Install placement.** (a) Sheet-only install: one row shape everywhere,
  but installing costs two taps. (b) Today's read-only browse row with a
  trailing button: one-tap install, but a second row shape and no preview
  for uninstalled plugins. (c) Both — chosen. The cost is a `ListRow`
  extension; the win is one row shape AND fast install AND preview.
- **Pre-install contents.** (a) Build the fetch-and-inspect hub read now:
  best browsing experience, extra hub surface and cache management.
  (b) Defer — chosen: the sheet unification lands without new fetching
  machinery, and catalog metadata still answers "what is this".
- **Counts in rows.** Identity-only rows (today) vs counts in meta — chosen:
  counts in meta. The vocabulary already exists in the launcher's
  `componentCounts`; rows become self-describing.
- **Launcher rows.** Switch rows (today's shape, shared vocabulary) vs
  drill-in rows with the toggle in the sheet — chosen: Switch rows; one-tap
  toggling is the launcher's job, and All/None stays the bulk path.

## Constraints (what exists today)

- `PluginEntry` (installed list RPC): plugin, marketplace, version, enabled,
  autoUpgrade, broken, installPath, gitCommitSha, installedAt, lastUpdated,
  updateAvailable. No component data.
- `MarketplaceCatalogPlugin` (browse): name, description, category,
  homepage, author. No component data.
- `PluginLaunchCandidate` (launcher preview): counts only — skillCount,
  agentCount, commandCount, hookCount, mcpCount.
- The Go loader (`agent/plugin`) already produces everything the Contents
  block needs for a plugin that loads: `Skills`, `Agents`, `Commands`,
  `Hooks` (by event), `MCPConfigs`, `MCPConfigWarnings`,
  `SkippedComponents`, `UnsupportedHooks`, `UnknownHooks`. One correction
  from the builder check: the hub's plugin list is built from the install
  registry WITHOUT loading `Instance`s, so the components read performs the
  disk loads itself (fail-soft, the same skip semantics as
  `LoadAllFailSoft`); whether and how it caches is left to the builder.
  A `broken` registry entry is a directory that fails load/validation, so
  it has no components at all — see below.
- Widget law (docs/developing-evener/design-system.md): compose from
  widgets; a row is one tap target; changing a widget's API is a design
  decision — made here for `ListRow` (decision 1).

## Resolutions added after the builder check (my calls, 2026-11-08)

- **The components read covers every source kind.** It takes a plugin
  identity that is either an installed `{plugin, marketplace}` pair or a
  launch-directory `{path}` (which also covers bundled candidates, since
  both resolve to a directory the loader can read). `PluginLaunchCandidate`
  stays counts-only — launch previews stay cheap — and the launcher sheet
  lazy-loads the contents through this same read. No candidate is fattened
  and no source kind is left without a sheet.
- **A broken plugin shows why it is broken.** The read returns a per-plugin
  error entry (fail-soft, never failing the whole batch) when a plugin's
  directory fails to load. In the installed sheet, a danger `InlineNotice`
  with the load reason replaces the Contents block; the chips (including
  "broken"), the switches, and the Remove action stay, exactly as the
  current sheet already offers them on broken entries. "Manifest only" is
  reserved for a plugin that loads and genuinely ships nothing.
- **Hooks vocabulary.** The read returns hook events, each with its
  registered-hook count. List rows keep the registered-hook total ("5
  hooks"); the sheet's Badge shows that same total and the names list
  events with their counts ("PreToolUse (3), PostToolUse (2)"). The row and
  the sheet can no longer disagree.

- **Launcher sheet-open (Jesse, 2026-11-09): a quiet Details action in the
  row description.** The launcher Switch row and its one-tap toggling are
  untouched; a small quiet "Details" action at the end of the description
  line (source · counts · description) opens the same sheet. Two keyboard
  targets (the Switch, then Details), no new widget API — the description
  slot already renders caller-supplied content and is not the toggle
  target.

## The how, as decided

- **`ListRow` extension (widget change).** The interactive variant becomes a
  container: the main area (leading, label, chips, meta) is the tap target,
  and a new optional `trailingAction` renders in the trailing slot beside
  the drill-in chevron. No nested buttons: the row container is not itself a
  button; the main area is. Two tab stops per row that carries a control
  (main, control) is the accepted keyboard shape. Every existing ListRow
  call site renders unchanged.
- **Shared row composition (per surface).** A plugin row component built on
  `ListRow` composes: leading `StatusDot` (installed: broken → failed,
  disabled → ended, else idle), label, status chips (installed: broken /
  off by default / auto-upgrade / update available; catalog: "installed"
  chip when already installed), and meta — installed:
  `@ marketplace · vX · counts`; catalog: `description · category`. Browse
  rows add the trailing Install button (not-installed only).
- **Shared detail sheet.** One `PluginDetailSheet` with a mode:
  - installed: chips, description, Contents block, PropertyList
    (Version, Marketplace, Source), Enabled/Auto-upgrade switches, footer
    Upgrade (when updateAvailable) + Remove, with the existing confirm
    dialogs and focus-rehome behavior.
  - catalog: chips (installed-only), description, catalog PropertyList
    (Marketplace, Source, Author when present), footer Install with the
    existing confirm dialog. No switches, no Contents (decision 2).
  - launcher: Contents block, PropertyList (Version, Marketplace, Source),
    one switch — Included in this session. No install/remove/enable.
- **Contents block.** An `Eyebrow` "Contents", then one row per kind
  present: kind label, `Badge` count, mono names (first eight, then a
  "+N more" inline action that expands). Load diagnostics render inside the
  block as an `InlineNotice` (danger) when the plugin has MCP config
  warnings or skipped components. A plugin with no components reads
  "Manifest only".
- **New hub read, plugin components.** For installed plugins: names per kind
  (skills, agents, commands namespaced as loaded; hooks as event names;
  MCP server names) plus the diagnostics strings. Serves the settings
  sheet, the launcher sheet, and the counts in both lists' rows (installed
  rows already have the data in-process; only the wire surface is new).
- **Launcher preview.** `PluginLaunchCandidate` gains the same component
  names (or the sheet composes from the same read) so launcher rows keep
  counts in the Switch description and the sheet shows names. The
  All/None/diagnostics behavior is untouched.
- **View model.** One plugin view type in the client unifies what each
  surface renders, parameterized by mode; the three wire types feed it.

## What's left to the builder

- The RPC method shape for the components read (batch-per-host vs
  per-plugin; likely extend the existing plugins list response or add a
  per-plugin detail read — decide by hub convention).
- Whether `trailingAction` needs a disabled state and what it does to
  ListRow's existing read-only `action` prop (they should converge).
- The gallery section updates the widget change requires (design-system
  doc: every widget change lands with a gallery section and a §3 row
  update).
- Test plan per docs/developing-evener/testing.md: behavior tests through
  the accessible names (rows open the right sheet mode; install button
  installs; switches toggle; contents expand), no assertions on copy.
