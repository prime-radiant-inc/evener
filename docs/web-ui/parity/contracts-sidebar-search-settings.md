# Behavior contracts: sidebar, search/palette, settings, credentials/device-flow, notifications, panes

Mined from `cmd/serf-hub/jstest/`. Each line is a behavior a new Vitest suite must
re-cover, tagged with the jstest file it currently comes from. CSS/token-only files
(no runtime/DOM-interaction behavior, just regex assertions over `style.css`) are
listed by name only, per instructions, in the final section.

**Cross-checked against sibling docs already in this repo root**
(`contracts-composer-queue-pending.md`, `contracts-transcript-scroll-liveness.md`)
to avoid duplicate coverage — see "Deliberately excluded / overlap notes" at the
bottom of each group where relevant.

## Sidebar

### test-sidebar-archived-sessions.js
- archived-tier sessions render only inside the top-level "Archived sessions" disclosure, grouped under their project's sub-heading — never inline under their active project (test-sidebar-archived-sessions.js)
- active-tier sessions render exactly as before archived-tier sessions were pulled out of the project list (test-sidebar-archived-sessions.js)

### test-sidebar-aria.js
- a project with `default_expanded` omitted/false renders `aria-expanded="false"` — never the literal string `"undefined"` — on both the first build and a later patch/reconcile of the same header node (test-sidebar-aria.js)

### test-sidebar-children.js
- current (non-terminal) subagent children render directly beneath their parent row; terminal/ended descendants live behind that parent's own independent "inactive" disclosure, nested per parent (test-sidebar-children.js)
- the nested current/inactive projection survives keyed reconciliation across resyncs without collapsing or duplicating rows (test-sidebar-children.js)
- deep-linking to a session auto-reveals it by expanding every collapsed project/inactive-disclosure ancestor along the path, even through nested chains, without looping (test-sidebar-children.js)
- deep-linking to a nonexistent session id does not throw or infinite-loop the auto-reveal walk (test-sidebar-children.js)

### test-sidebar-clusters.js
- a server-clustered `kind:"cluster"` node (repeated same-titled runs folded server-side) renders as a non-navigable fold row (button, no href) showing title + member count, collapsed by default; expanding it reveals its `children` as normal session rows (test-sidebar-clusters.js)
- auto-reveal deep-linking through a collapsed section → project → cluster chain expands all three levels to surface the target session (test-sidebar-clusters.js)

### test-sidebar-fetch-coalesce.js
- two concurrent `/api/tree` consumers (e.g. the startup fetch and an immediately-scheduled resync) share one in-flight request instead of each issuing their own transfer (test-sidebar-fetch-coalesce.js)

### test-sidebar-fold.js
- clicking a server-default-expanded project's header collapses it — an explicit user collapse overrides `default_expanded` (test-sidebar-fold.js)
- the explicit collapse persists to localStorage as `"false"` and survives a resync re-render of the same tree (test-sidebar-fold.js)
- a fresh page load restores a persisted collapse, keeping a default-expanded project collapsed with its sessions hidden (test-sidebar-fold.js)
- clicking a user-collapsed project again re-expands it and flips the persisted preference back (test-sidebar-fold.js)

### test-sidebar-header.js
- the sidebar's static header chrome (rail-toggle, close, new-session, search, settings actions) lives in `app.html` as server-rendered chrome and is not lost when sidebar.js's client-rendered `.sb-tree` rows take over `#sidebar` — present immediately at skeleton paint, never duplicated across repeated `renderTree()` calls (test-sidebar-header.js)
- the new-session action links to `/new` and hx-gets `/_partials/workspace/spawn` targeting `#workspace`; the settings action links to `/settings` and hx-gets `/_partials/settings/general` (test-sidebar-header.js)
- the rail-toggle starts disabled (auto/desktop resolves to pane) and clicking it cycles auto → rail; the close button shares the same `data-sidebar-toggle` wiring as the hamburger and opens/closes the drawer, which starts closed (test-sidebar-header.js)

### test-sidebar-icons.js
- each session row renders an SVG status icon (not a plain status dot) inside `.status-icon`, with a `title` attribute carrying the unified state word (e.g. "Working", "Question waiting", "Your move") as the hover tooltip (test-sidebar-icons.js)
- the project rollup badge renders its count via an SVG icon, not a text glyph, with the numeric count still present as plain text (test-sidebar-icons.js)
- an ask-pending row carries `data-ask="true"`; a your-move row does not carry `data-ask` (test-sidebar-icons.js)

### test-sidebar-lazy-archived.js
- archived projects arrive as session-less stubs (`{sessions: null, session_count: N}`) and render collapsed showing that count (test-sidebar-lazy-archived.js)
- expanding an archived project lazy-loads its sessions from `/api/tree/project?key=...` on demand (test-sidebar-lazy-archived.js)
- a project the user had previously expanded (persisted in localStorage) auto-restores expanded, lazy-loading its sessions, on a fresh page load (test-sidebar-lazy-archived.js)

### test-sidebar-menu-mobile.js
- under a phone viewport (max-width:767px), opening a row/project's ⋯ menu renders it as a full-width modal sheet inside a tap-to-dismiss backdrop scrim instead of the desktop anchored mini-popover, keeping `role=menu`/`menuitem` semantics, focusing the first item on open, and marking the rest of the page inert while open (test-sidebar-menu-mobile.js)
- in the mobile menu, ArrowDown/ArrowUp cycle focus among items; Escape closes the menu, removes the scrim, refocuses the ⋯ anchor, and lifts the inert background; tapping the scrim itself dismisses the menu the same way (test-sidebar-menu-mobile.js)
- removing the anchor row from the DOM while its mobile menu is open tears down both the menu and its scrim (test-sidebar-menu-mobile.js)
- on desktop (no phone media match) the menu keeps its pre-existing behavior unchanged: popover styling, no scrim, appended to `<body>`, anchored under the button, background stays interactive, Escape closes and refocuses the anchor (test-sidebar-menu-mobile.js)

### test-sidebar-menu.js
- the ⋯ menu shows "Rename" only when the row's node carries `node.rename`; choosing "Favorite" calls `SerfSidebar.favorite`; Escape and removing the anchor row both close the menu (test-sidebar-menu.js)
- an archived test-run project's menu offers "Unarchive" (not "Archive") (test-sidebar-menu.js)

### test-sidebar-migration.js
- sidebar expansion-state keys are the server's project IDs only — legacy basename-keyed localStorage entries are ignored outright, with no migration or synthesis of a project key from a basename (test-sidebar-migration.js)

### test-sidebar-model.js
- first paint renders a skeleton immediately; once `/api/tree` resolves, the built rows carry the htmx workspace-swap attributes, get passed to `htmx.process()`, and the active session's row is marked active (test-sidebar-model.js)

### test-sidebar-overlay.js
- a favorite (mutation-type) optimistic op stays applied across resyncs until a resync's payload actually reflects the field, and is not rolled back merely because no qualifying event has fired yet (confirmed via a post-POST resync) (test-sidebar-overlay.js)
- an archive (disappearance-type) optimistic op completes as soon as its POST returns 2xx; the 30s eviction timer is only a safety net, not the primary completion path (test-sidebar-overlay.js)

### test-sidebar-project-chevron.js
- every project header (active and archived stub alike) renders a `.project-chevron` ("›") before the project name, on both the initial build and the patch/reconcile path (same DOM node reused), and toggling expansion flips the header's `aria-expanded`, which the CSS rotation rule keys off (test-sidebar-project-chevron.js)

### test-sidebar-reconcile.js
- keyed reconcile preserves DOM node identity for unchanged row IDs across re-renders; the same session appearing in Needs-you, its project, and Pinned simultaneously yields three separate, stable DOM nodes (one per tier) (test-sidebar-reconcile.js)

### test-sidebar-resizer-markup.js
- the app shell renders a dedicated full-height `#sidebar-resizer` element (separator role) next to `#sidebar`, sitting between the sidebar and workspace, instead of relying on a browser corner-drag handle (test-sidebar-resizer-markup.js)

### test-sidebar-resync-project-keys.js
- sidebar disclosure keys and project keys share one expansion set, but a resync only re-fetches keys that are actual `/api/tree/project`-addressable projects — non-project disclosure keys (e.g. section headers) are never sent as resync targets (test-sidebar-resync-project-keys.js)

### test-sidebar-rollup.js
- the project header's rollup badge shows only the needs-you segment when there's attention but no live work, only the working segment when there's live work but no attention, and both segments (needs-you ranked first) when both apply; it renders nothing, with no stray separator, when both counts are zero (test-sidebar-rollup.js)
- the reconcile/patch path updates an existing rollup badge's counts and tint in place rather than rebuilding the header node (test-sidebar-rollup.js)

### test-sidebar-row-layout.js
- a session row is a 3-track grid (icon / growing title / row-end) directly on `.sb-row` — no `.text-col` title wrapper and no meta/branch column — with the title carrying the full name as a hover tooltip and single-line ellipsis (no 2-line clamp), and the age pinned inside row-end alongside the ⋯ menu button (test-sidebar-row-layout.js)
- hovering/focusing a row fades the age out and fades the ⋯ menu in — they share the same grid cell via overlap (test-sidebar-row-layout.js)
- rail-collapsed mode does not restyle `.sb-row` itself — the whole sidebar is hidden rather than re-laid-out (test-sidebar-row-layout.js)

### test-sidebar-sections.js
- the "Archived sessions (N)" section is a keyed divider, collapsed by default, that reuses the normal project-header/row-building machinery for its contents once expanded, and offers "Unarchive" (not "Archive") on the projects inside it (test-sidebar-sections.js)

### test-sidebar-session-routes.js
- local, Codex-sourced, and fallback-ID sessions each generate distinct row IDs/hrefs/hx-partial-swap targets that preserve their original source identity — a Codex thread ID is never conflated with a bare/local session ID (test-sidebar-session-routes.js)
- a Codex row's menu offers "Open"; a qualified Codex URL correctly marks its row active, while a bare external thread ID alone does not spuriously match/activate that row (test-sidebar-session-routes.js)

### test-sidebar-survivors.js
- contracts carried over from the pre-rewrite sidebar and still required: rail-toggle mode + its persistence, the mobile drawer's close API, and a subagent row's open-beside delegate action (test-sidebar-survivors.js)

### test-sidebar-testruns.js
- the "Test runs (N)" section is a keyed divider collapsed by default, rendered below "Archived", reusing the same project-header/row machinery; the client renders `tree.test_runs`/`tree.archived_projects` as two independent server-supplied buckets without re-deriving or merging membership itself (test-sidebar-testruns.js)
- the Archive/Unarchive menu verb follows the server-supplied `is_archived` flag on the node, not which section bucket the client happened to render it under (test-sidebar-testruns.js)

### test-sidebar-tristate.js
- sidebar mode persists as one of `auto|rail|pane` under the legacy storage key; old binary prefs migrate (`true`→rail, `false`→pane, absent→auto) into that scheme (test-sidebar-tristate.js)
- on desktop (≥1200px) "auto" resolves to pane (no rail attribute); below 1200px "auto" resolves to rail; an explicit "rail" pin applies even on desktop (test-sidebar-tristate.js)
- ⌘B cycles rail→pane→auto and persists each step; on phone, rail/pane/auto all resolve to "no rail attribute" (the drawer governs instead) even though the underlying preference is still recorded and survives back to a larger viewport (test-sidebar-tristate.js)

### test-sidebar-workingdir-escape.js
- the project menu's "New session" and "Settings" links `encodeURIComponent()` the project's `working_dir` before embedding it in the `dir=`/`cwd=` query param, so a directory containing spaces/&/# doesn't corrupt or truncate the query string (test-sidebar-workingdir-escape.js)

### test-nav-toggle.js
- the mobile sidebar-open toggle (`.app-nav-toggle`, wired to `data-sidebar-toggle`) lives once in the persistent app shell, not per-page — every page rendered into `#workspace` (session, new, settings, welcome) can reach it, and no page header renders its own redundant hamburger (test-nav-toggle.js)
- clicking the toggle opens the off-canvas drawer (starts closed); clicking it again, or clicking outside the open drawer, closes it (test-nav-toggle.js)
- the same app-shell markup pins PWA metadata: `web-app-capable`, a linked manifest, and `viewport-fit=cover` (test-nav-toggle.js)

### test-live-title-updates.js (sidebar half — the file also covers the workspace header, which belongs to the renderer area)
- a daemon-driven title-change notification (generated/renamed title) schedules a sidebar `/api/tree` resync so the sidebar tree's title updates without a page reload (test-live-title-updates.js)

### test-sidebar-collapsed-css.js (behavioral half — see CSS-only section below for its regex assertions)
- on desktop, a pinned "rail" (collapsed) preference sets `body[data-sidebar-rail]` and the drawer starts closed; clicking the floating nav-toggle chip opens the collapsed sidebar as an overlay drawer (test-sidebar-collapsed-css.js)
- navigating to a session from inside the open collapsed-mode drawer closes the drawer (via the `htmx:beforeRequest` hook); ⌘B cycles collapsed rail mode back to pane mode and persists "pane" as the read mode (test-sidebar-collapsed-css.js)

### Naming trap — excluded
- **`test-sidebar.js` is NOT about the session-navigation sidebar** despite its name. It loads `renderer.js` (not `sidebar.js`) and pins the *tasks panel*'s expandable `<details>` rows, task-detail `<dl>`, badge count, and status text — the same "tasks/details slide-over panel" system covered by `test-panels.js`/`test-panel-history-teardown.js`/`test-tasks-panel.js`/`test-task-updated-subscription.js`. None of that cluster is in this document; it belongs with whichever area owns the renderer's tasks/details panels.

## Search / Command Palette

### test-search-aside.js
- typing `/asi` (or any fuzzy match) selects the Aside command, which calls `asideThread` with the current session id and, on success, navigates to the new child session, triggers a sidebar refresh, and closes the palette dialog (test-search-aside.js)
- when AppWire is unavailable, the Aside command falls back to POSTing `/s/<id>/aside` directly and navigates to the child session on success (test-search-aside.js)
- if the Aside command fails, the dialog stays open and shows the failure inline in the palette instead of navigating away (test-search-aside.js)

### test-search-commands.js
- typing a leading `/` switches the palette into Commands mode and lists the command set locally without ever calling `/api/search` (test-search-commands.js)
- command names fuzzy-match the typed filter (e.g. "/comp" and "/cm" both match "Compact transcript"); non-matching commands are hidden (test-search-commands.js)
- command visibility is state-gated: e.g. "Compact transcript" is hidden on the home page and once the session has ended; live-session-only commands (like "Switch model") only show on a live session; ended-only commands (copy-id, tasks) only show once the session has ended (test-search-commands.js)
- an argless command (e.g. Compact) runs via a plain POST to `/s/<id>/compact` and closes the dialog on success (test-search-commands.js)
- a command with no AppWire method falls back to its REST endpoint (e.g. Upgrade POSTs `/api/upgrade` with the current channel target) and closes the dialog on success; when AppWire is present, it is used instead and the REST fallback is never called (test-search-commands.js)
- selecting an "args mode" command (e.g. Switch model) shows an args-mode pill and renders its option list; Esc from args mode returns to command-filter, restoring the prior filter text, rather than closing the dialog; Esc from top-level command-filter closes the dialog (test-search-commands.js)
- a model-list fetch failure surfaces an inline error row (not a "No matches" empty state); the model command never falls back to a REST model-list endpoint when AppWire is missing — it surfaces an options error instead (test-search-commands.js)
- selecting a model calls AppWire's `setModel` with the session id and provider/model id (no REST fallback), and closes the dialog (test-search-commands.js)
- the palette exposes listbox semantics: `role=listbox`/`combobox` on the container, `role=option` on rows, `aria-selected` on the active row, and the input's `aria-activedescendant` tracks it; ArrowDown moves `aria-activedescendant` to the next row (test-search-commands.js)
- running an argless command adds/promotes it into a "Recent" section shown above the Commands section on the next open, and the choice persists across opens (test-search-commands.js)
- commands requiring an active turn (e.g. `/steer`) refuse to POST when there is no active turn: the dialog stays open, shows an inline "no active turn" palette error, and preserves the user's typed args text (test-search-commands.js)
- `openWith(query)` opens the dialog pre-seeded with the given query, and a query starting with `/` opens it directly into command-filter mode (test-search-commands.js)
- global navigation commands (New session, Settings, Search-clear, Home) navigate via `SerfRenderer.navigateTo` rather than a hard link; a "/new `<prompt>`" variant navigates to `/new` with the prompt encoded in the query string (test-search-commands.js)
- the theme command flips `body`'s light/dark theme class and persists the choice to localStorage (test-search-commands.js)
- the Help command's rendered content includes a "Keyboard shortcuts" section mentioning ⌘K (test-search-commands.js)
- Interrupt/Clear/Shutdown/Queue/Drain-as-steer commands each POST their own REST endpoint with the session id; a REST failure for any of them keeps the error inline in the palette rather than throwing or closing the dialog; Shutdown does not go through a native `confirm()` dialog (test-search-commands.js)
- reasoning-effort commands (medium/high/xhigh) POST `/api/sessions/<id>/reasoning-effort` with the chosen effort level in the body (test-search-commands.js)
- the "copy session ID" command writes the session ID to the clipboard; "tasks"/"details" commands click the corresponding trigger button; a "/project" command un-collapses and scrolls the project's sidebar section into view (test-search-commands.js)

### test-search.js
- typing a plain (non-`/`) query while a conversation is open adds an "In session" results section that highlights the matched substring in `<mark>` and shows the turn's position (test-search.js)
- pressing Enter (or Shift+Enter) on an in-session result scrolls the matched transcript element into view and marks it with `.search-hit` (test-search.js)
- the in-session section is omitted entirely when there's no `#conversation` on the page (e.g. the home/new-session view) (test-search.js)
- an empty query shows the palette's empty-state copy and renders no results, without calling `/api/search` (test-search.js)

## Settings Tabs

### test-settings-appearance.js
- setting theme=dark applies `data-theme` on `<body>`, persists to localStorage, and toasts the new value; theme=system removes `data-theme` and clears the stored preference (test-settings-appearance.js)
- phone-density and sidebar-mode choices persist and apply immediately: phone-density writes directly to the body dataset; sidebar-mode="rail"/"auto" delegate to `SerfSidebar.applySidebarMode` rather than writing localStorage directly (sidebar.js owns that key), while "pane" falls back to writing localStorage only when `SerfSidebar` isn't present (test-settings-appearance.js)
- on restore (page load) the theme/phone-density/sidebar-mode radios reflect the stored preference, defaulting sidebar-mode to "auto" when nothing is stored (test-settings-appearance.js)
- phone-density applies on script load, before `DOMContentLoaded` fires; sidebar-mode no longer writes the rail attribute itself on load (ownership moved to sidebar.js) (test-settings-appearance.js)

### test-settings-dir-picker.js
- the inline working-directory input never gets a native `datalist` (no datalist elements are created for settings dir inputs); focusing or typing in it keeps focus on the original input itself rather than opening a secondary picker input (test-settings-dir-picker.js)
- typing in the inline dir input opens the shared directory-suggestions picker, browsing by the final path component of what's typed, without accumulating duplicate input listeners across repeated opens (test-settings-dir-picker.js)
- clicking a suggested directory row browses into that directory in place (no input/change events fired) and keeps the inline picker open; only Accept dispatches exactly one input event and one change event on the original field (test-settings-dir-picker.js)
- the dedicated dir-picker button (as opposed to the inline input) opens the shared `.chip-picker-dir` popover listing children of its sibling text input's current value; browsing rows there doesn't touch the sibling input until Accept, which writes the browsed path and fires exactly one input and one change event (test-settings-dir-picker.js)

### test-settings-loudscope.js
- the loudScope setting is a 2-option radio (asks default / all) wired through the same `data-notif-form` change delegate as the notification checkboxes; selecting "all" persists `loudScope=all`, dispatches `serf-hub:notifications-changed`, and toasts success (test-settings-loudscope.js)
- on restore, the stored loudScope radio is checked and the other unchecked; an unset/default preference reflects to the "asks" radio (test-settings-loudscope.js)

### test-settings-mcp-status.js
- the server-rendered MCP settings partial lists every probed server from `settingsData.Mcps` (name, transport, status via the shared status-badge convention, and a conditional last-error), with an empty-state message when no servers are configured (test-settings-mcp-status.js)
- the probed-status list coexists with the pre-existing editable `#mcps-form`/launchconfig wiring/MCP-config-files/inline-MCP-servers sections rather than replacing them (test-settings-mcp-status.js)

### test-settings-model-picker.js
- the settings model picker fetches the diagnostics-envelope endpoint (`diagnostics=1`); its "Recent" tab is pinned first among providers, is active by default, and shows the most recent model with the same prettified-name+badge treatment as other provider tabs (test-settings-model-picker.js)

### test-settings-notifications.js
- toggling the title-count preference persists it, flips its ON/OFF label, dispatches `serf-hub:notifications-changed`, and a committed change toasts success (test-settings-notifications.js)
- toggling OS notifications on requests permission; once granted the checkbox stays checked and its label reflects "granted"; if permission is denied instead, the checkbox reverts to unchecked, its label reverts to OFF, it is not persisted as on, and a warning toast explains why (test-settings-notifications.js)
- on restore, a previously-saved toggle is checked and an unset one stays unchecked (test-settings-notifications.js)

### test-settings-shell.js
- typing in the settings nav filter hides links whose label doesn't match (e.g. filtering "agents" hides "General" but keeps "Agents"); a section header stays visible only while at least one child link is still visible, and hides once every child is filtered out; clearing the filter re-shows every link (test-settings-shell.js)
- on load, the phone back-button is visible whenever an Active section title is present; clicking it hides the back button and flips `settingsPane` from detail back to nav (test-settings-shell.js)

### test-settings.js
- the transcript-status "prompt loaded" toggle defaults off (state label OFF); toggling it on updates the label to ON, persists the preference, and shows a success toast on save (test-settings.js)
- on restore, a missing `promptLoaded` preference restores to off while a saved `hookExitsNormal=true` preference restores its label to ON (test-settings.js)

## Credentials / Device Flow

### test-credentials-device.js
- starting device-flow sign-in for an instance renders the device editor showing the user code without auto-opening the provider's (OpenAI) verification page (test-credentials-device.js)
- the "Send me to OpenAI" button stays disabled until the user has copied the code; copying enables it, and clicking it opens the verification URL (test-credentials-device.js)
- once polling reports "authorized", the device editor closes and the instance row switches to the signed-in "Refresh OAuth" state (test-credentials-device.js)
- if the device-code response indicates the fallback path, the UI opens the paste-back (oauth-redirect) editor instead of the copy-code device editor (test-credentials-device.js)

### test-credentials.js
- provider instances render grouped by type (e.g. openai/anthropic/google/kimi), each inside its type group's instance list, with a header showing the type name (test-credentials.js)
- an instance with multiple credential sources shows all its layers (e.g. oauth+file, or file+env) with the higher-precedence source labeled "effective" and lower ones "shadowed" beneath it (oauth > file > env), plus its configured api-style (test-credentials.js)
- the default instance shows a default badge and does not offer "make default"; a non-default instance does offer it (test-credentials.js)
- the Set/Replace button reads "Replace key" whenever a file-sourced key already exists (even if a different source is currently effective) and "Set key" when no key exists at all; "Clear" is offered only when an active stored layer exists (test-credentials.js)
- an instance with no configured layers at all shows a "Not configured" label instead of a layered display, and does not offer Clear (test-credentials.js)
- clicking "Add" opens the global add-provider-instance form, which lists the available types and shows the api-style field only when the selected type is openai (test-credentials.js)

## Notifications

### test-notifications-attention.js
- on boot, the baseline `/api/tree` summary populates the document title's leading `"(N) "` count from `attentionSummary.needsYou` immediately (test-notifications-attention.js)
- a `serf/attention/changed` broadcast updates the title count from its own summary and fires exactly one OS notification when a session transitions INTO needs_you/error — not merely because it reports that level (test-notifications-attention.js)
- when the tab is focused (`document.hasFocus()` true), an into-transition still updates counts but suppresses the OS notification (test-notifications-attention.js)
- a broadcast that arrives before the baseline fetch has resolved must not fire an OS notification (no baseline yet → no edge firing) (test-notifications-attention.js)
- the sound preference plays a tone (via `AudioContext`) on the same into-transition that would trigger an OS notification (test-notifications-attention.js)
- dispatching `serf-hub:notifications-changed` re-reads prefs and re-applies the title immediately in both directions (title off strips the `"(N)"` prefix, title on restores it from the current summary); an `htmx:afterSettle` on body re-applies the title if a swap clobbered it (test-notifications-attention.js)

### test-notifications-loudscope.js
- an existing v2 prefs blob with no `loudScope` key backfills `loudScope` to `"asks"` (not "off") on load and bumps the stored version to `"3"` so the migration doesn't re-run; a fresh install with no stored blob at all also defaults straight to `loudScope="asks"` (test-notifications-loudscope.js)
- under the default `loudScope="asks"`, a generic needs_you transition (no error, no `askPending`) does not fire the OS notification or tone; an `askPending` transition fires both; an error transition fires the OS notification regardless of `askPending` (test-notifications-loudscope.js)

### test-notifications-migration.js
- a fresh install with no stored prefs blob defaults to `title:true, favicon:true, os:false, sound:false` and stamps the storage version to `"3"` (test-notifications-migration.js)
- an existing partial prefs blob (e.g. only `{os:true}`) backfills every other absent key to explicit `false` rather than leaving them undefined, while keeping the values that were already present (test-notifications-migration.js)

### test-notifications-summary-endpoint.js
- `fetchBaseline` always hits the lightweight `/api/tree?summary=1` endpoint, never the full tree, and the returned `attentionSummary` still drives the title count (test-notifications-summary-endpoint.js)

### test-notifications-palette.js
- `notifications.js`'s `STATE_COLORS` constants match the design system: working is blue (dark `--state-working`/`--accent`), needs_you is amber (dark `--state-awaiting`/`--attention`), and error is left unchanged (test-notifications-palette.js)

### test-toast.js
- `window.SerfToast.show(message, kind)` inserts a `.toast` element (carrying the kind class) into `#toast-region` and returns a handle; `#toast-region` itself carries `aria-live="polite"` (test-toast.js)
- a shown toast auto-dismisses after its configured timeout, and the returned handle can dismiss it early on demand (test-toast.js)
- an unrecognized kind falls back to the `toast-info` styling class (test-toast.js)
- calling `show()` when `#toast-region` doesn't exist in the DOM returns `null` and appends nothing, without throwing (test-toast.js)

### test-favicon-language.js
- the favicon's base circle is pinned neutral (`#7e8593`) in both the `PLAIN_FAVICON` constant and `buildFaviconDataURI`'s output — not blue, since post-recolor blue now specifically means "working" (test-favicon-language.js)
- the favicon's per-state dot colors are pinned: needs_you=amber, working=blue, error=red, regardless of the page's own light/dark theme — the icon always renders for dark browser chrome (test-favicon-language.js)
- `thread.html`'s inline favicon markup is likewise neutral at rest, not blue (test-favicon-language.js)

### Deliberately excluded / overlap notes
- `test-notifications-ask-awaiting-broadcast.js` is **not** re-covered here — it is already fully mined under "Ask User" in `contracts-composer-queue-pending.md` (ask_user's awaiting→needs_you normalization + the loudScope `askPending` gate). Re-reading it here would just duplicate that entry.

## Panes

### test-panes.js
- `openAfter(href, title, afterHref)` inserts a new pane immediately after the pane matching `afterHref` (or at the front when `afterHref` is null); reopening an already-open href focuses its existing iframe and leaves pane order untouched instead of duplicating or reordering it; requesting a parent href that isn't currently open fails closed (no pane created) (test-panes.js)
- opening a pane keeps DOM order, the persisted localStorage order, and the URL's `pane=` param order all in sync with each other (test-panes.js)
- `open(href, title)` is idempotent per href; the side-panes region auto-hides once the last pane closes and auto-shows on first open; the pane count is capped at `MAX_SIDE_PANES`, and opens beyond the cap silently add nothing more (test-panes.js)
- closed/opened panes persist to localStorage; a simulated reload (DOM cleared, side-panes hidden) followed by `restore()` reopens exactly the persisted panes (test-panes.js)
- `setSidePanesWidth` clamps the side-panes width between a minimum (280) and a viewport-relative maximum (`min(1200, innerWidth-360)`) (test-panes.js)
- opening N panes enforces a minimum total side-region width of N×`PANE_MIN` (capped by the viewport max); closing/reopening a pane after the stored width was dragged below that floor re-applies the N×`PANE_MIN` minimum, and `restore()` after reload re-applies the minimum for however many panes it restored (test-panes.js)
- dragging the pane splitter marks `body[data-pane-dragging="true"]` for the drag's duration (so paned iframes stop stealing mousemove) and live-resizes the stored width as the mouse moves; releasing the mouse button (or a mousemove with no button pressed) stops the resize, clears `data-pane-dragging`, and further hover/mousemove after release does not keep resizing (test-panes.js)
- a child iframe posting an open request through the host bridge (`openFromChild`) is inserted at the normalized `afterHref` position, or prepended when `afterHref` is null; a cross-origin or otherwise untrusted/unknown `afterHref` for the bridge path fails closed (test-panes.js)

### test-panes-error.js
- a newly opened pane starts in a "loading" state (`dataset.state`) and flips to "ready" once its iframe fires a `load` event (test-panes-error.js)
- `markError(href, message)` puts the matching pane into an "error" state that renders a `.pane-error` block with a `[data-pane-retry]` control (test-panes-error.js)

### test-panes-url.js
- `open()` appends a `pane=<path>` query param to the current URL for each open pane, preserving the page's own path; opening two panes adds two `pane=` params in order (test-panes-url.js)
- `close()` removes only the matching `pane=` param, leaving the others and any pre-existing non-pane query params untouched (test-panes-url.js)
- on load, `restore()` prefers `pane=` params found in the URL over whatever is stored in localStorage, and a URL-specified pane opens even if that href is in the user's locally-suppressed set (test-panes-url.js)
- a doc-pane href carrying its own query string round-trips correctly through the `pane=` URL encoding; legacy `/s`-style pane hrefs found in the URL are normalized to `/thread/` form on restore; `MAX_SIDE_PANES` is enforced when restoring from a URL with more `pane=` params than the cap (test-panes-url.js)

### test-doc-pane-open-beside.js
- an IMAGE transcript card whose src is a sha-addressed `/s/<id>/images/<sha>` URL gains an ⇲ button that calls `SerfPanes.open()` with that URL and the filename, without also opening the image's lightbox; a `data:` URL image (no stable, iframe-safe URL) gets no ⇲ button at all (test-doc-pane-open-beside.js)
- a file-referencing TOOL card (`read_file`/`edit_file`/`write_file`) gains an ⇲ button, positioned right of the filename, that opens `/doc/file?session=..&path=..` with the path relativized against the session's cwd; clicking the filename itself opens the same doc pane; a non-file tool (e.g. grep) gets no ⇲ button (test-doc-pane-open-beside.js)
- both the image and file ⇲ buttons are omitted entirely when `window.SerfPanes` is not present (framed/iframe guard) (test-doc-pane-open-beside.js)

### test-observer-autoopen.js
- when `#conversation` carries `data-observers="<ref>[,...]"`, the renderer's init auto-opens each listed LIVE observer beside the worker via `SerfPanes.open("/thread/<ref>")`, for one or many observer refs; with no `data-observers` attribute, nothing is auto-opened (test-observer-autoopen.js)
- an observer the user has manually closed stays closed across a later re-init (suppression is remembered, keyed by the normalized `/thread/` href); a subsequent explicit manual open of that same observer clears its suppression so future re-inits will auto-open it again (test-observer-autoopen.js)
- when `window.SerfPanes` is absent (already inside a pane iframe), auto-open silently does nothing rather than erroring (test-observer-autoopen.js)

### test-thread-document-bridge.js
- a host window's `SerfPanes` exposes a bridge so a known child (paned) frame can request opening another pane via `postMessage`, inserted at the requested (normalized) `afterHref` position (test-thread-document-bridge.js)
- the host bridge rejects a request whose href is cross-origin, and rejects a request whose source window isn't a currently-open child pane's `contentWindow` (unknown/untrusted source) (test-thread-document-bridge.js)
- a framed child with no local pane host (itself running inside a pane) reports no local `SerfPanes` and instead posts its `open()` calls up to the host bridge; a thread breadcrumb "back to parent" click is likewise canceled and re-routed through the host bridge when framed, but proceeds as normal href navigation when standalone (test-thread-document-bridge.js)

### Deliberately excluded / overlap notes
- `test-renderer-open-beside.js` and `test-renderer-pane-compact.js` are **not** re-covered here even though they exercise `SerfPanes` — `contracts-transcript-scroll-liveness.md` claims "all 49 `test-renderer*.js` files, in full" and already mines both (subagent-row open-beside affordance; `pane-compact` body class + `[data-compact]` cheap-tool clustering when framed). Re-reading them here would just duplicate those entries.
- `test-panels.js`, `test-panel-history-teardown.js`, `test-tasks-panel.js`, `test-task-updated-subscription.js` are a *different* system (the tasks/details slide-over panel, implemented in `renderer.js`) from `SerfPanes`/`panes.js` (side iframe document panes). None of that cluster is "panes" in this document's sense — see the `test-sidebar.js` naming-trap note above. Neither sibling doc claims this cluster either; it appears to be a genuine gap in the overall split, not something to silently absorb here.

## CSS-token-only tests (name only, no runtime behavior to port)

Pure regex/text assertions over `assets/style.css` (or, for two, over `assets/notifications.js`'s color constants) — no JSDOM, no DOM interaction. Global/cross-cutting design-system contracts plus the sidebar/pane-specific ones landed here since none of the other split-out areas claims them:

- `test-breakpoint-ladder-css.js` — phone/tablet/desktop/wide breakpoint widths
- `test-color-system-css.js` — canonical color-token vocabulary, no legacy names
- `test-contrast-css.js` — `--ink-4` never colors text, only fills/borders
- `test-layout-scale-css.js` — measure/machine-bleed width tokens
- `test-mobile-css.js` — rules scoped inside `@media (max-width: 767px)`
- `test-retired-treatments-css.js` — no ALL-CAPS labels, two documented radius tokens
- `test-style-colorblind-shapes.js` — warning no longer shares the diamond transform with awaiting
- `test-style-palette.js` — no leftover references to the old "processing" name
- `test-pane-and-sidebar-css.js` — compact side-panes + full-border sidebar resizing
- `test-sidebar-collapsed-css.js` — collapsed-sidebar CSS rules (its behavioral half is mined above under Sidebar)
- `test-sidebar-density-css.js` — menu/reveal control tap-target floor (≥24px)
- `test-sidebar-menu-modal-css.js` — mobile ⋯ menu modal sheet + scrim rules
- `test-sidebar-polish-css.js` — v3 row polish: disclosure hover-reveal, age typography, no meta column, header type treatment, hamburger rail toggle
- `test-errored-light-tint.js` — sidebar row background washes removed, border-left accent remains for errored state
- `test-show-cost-gating.js` — `body[data-show-cost="false"]` hides all three cost-bearing surfaces
- `test-font-size-presets.js` — S/M/L/XL `--text-*` token presets stay internally ascending and scale up across presets

Two further CSS-suffixed files touch adjacent visual areas (composer input dock, composer context-pressure indicator) but are not part of this document's feature set: `test-composer-layout-css.js` and `test-context-pressure-css.js` are already claimed by `contracts-composer-queue-pending.md`'s own CSS-token-only list (the first one; the second wasn't found in either sibling doc but is squarely a composer/input-status concern, not sidebar/search/settings/credentials/notifications/panes).

## Other boundary calls (files considered and excluded)

- `test-model-switch.js`, `test-thread-state.js`, `test-focus-trap.js`, `test-icons.js`, `test-skeleton-data-loading.js` — shared infrastructure (model chip, thread-busy signal, focus-trap helper, icon registry, htmx loading-skeleton) exercised standalone, not specific to any one of this document's features; each is a dependency of multiple areas (composer, renderer, settings, sidebar) rather than owned by one.
- `test-dir-picker.js`, `test-dir-picker-recent.js`, `test-picker-clamp.js`, `test-mobile-picker.js` — generic chip-picker widget (`dir-picker.js`) tests not scoped to Settings specifically; the settings-specific integration is `test-settings-dir-picker.js`, which *is* covered above. The generic widget is exercised at least as heavily from the spawn/new-session form.
- `test-plugins-manager-browse-filter.js`, `test-plugins-manager-browse-filter-lazyload.js`, `test-plugins-manager-browse-tree.js` — hosted under a Settings partial but a distinct Plugin-Manager feature, not the generic settings-tab mechanics this document covers.
- `test-subagents.js`, `test-subagent-nav.js` — subagent aggregation/navigation, a distinct feature area (also explicitly excluded by `contracts-transcript-scroll-liveness.md`).
