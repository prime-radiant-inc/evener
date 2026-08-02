# M7 Settings — Behavior-Parity Checklist

Scope: every Settings nav section + Providers→Credentials + per-project settings (`?cwd=`), as
currently implemented by the htmx/Go-template + vanilla-JS web UI. Written for the M7 milestone of
`docs/superpowers/specs/2026-07-20-webui-workspace-shell-rewrite-design.md` ("M7 settings: all
sections + credentials/OAuth + plugins/marketplaces manager", called out in that doc's §11 Risks as
"driven by per-section parity checklists"). Check an item once the new React/TS pane reproduces it
(or the team has made a deliberate, documented decision not to).

- **Source commit:** `3ad5a682cf0a2a2a03a6bbf6e1d088ba238f2a8b` (2026-07-20, worktree
  `webui-workspace-shell`). File:line references are exact as of this commit; re-verify line numbers
  after any further edits to the old UI before relying on them.
- **Read for this checklist:** `templates/partials/settings.html`, everything in
  `templates/partials/settings/`, `templates/partials/credentials.html`, `assets/settings-*.js`,
  `assets/launchconfig.js`, `assets/plugins.js`. Paths below are relative to `cmd/serf-hub/`.
- **Not read for this checklist** (out of the assigned scope — flag before assuming): the Go
  handlers that populate template data and implement the `serf/*` RPCs, `assets/sidebar.js`,
  `assets/notifications.js` (the runtime alerting engine, as opposed to the settings controls for
  it), `assets/spawn.js`, `assets/search.js`. Where those files are referenced below it's only
  because the read files name them directly (e.g. a `window.SerfSidebar.applySidebarMode` call) —
  their own internal behavior is not verified here. RPC **response field names** listed below are
  reverse-engineered from what the client JS reads off the response object, not confirmed against
  the Go-side struct definitions — treat them as "what the client currently depends on", not as an
  independently-verified wire contract.
- **Format:** one checkbox per discrete behavior, `file:line` pointing at the current
  implementation. Sections follow the left-nav order in `templates/partials/settings.html`.

## Section → route → appwire summary

| # | Section | Page route | Partial route | Partial file | Appwire (wire method) | Persistence |
|---|---|---|---|---|---|---|
| 1 | Shell/nav | `/settings` | — | `templates/partials/settings.html` | none | — |
| 2 | General | `/settings/general` | `/_partials/settings/general` | `settings/general.html` | none | server (read-only) |
| 3 | Theme | `/settings/theme` | `/_partials/settings/theme` | `settings/theme.html` | none | localStorage |
| 4 | Transcript | `/settings/transcript` | `/_partials/settings/transcript` | `settings/transcript.html` | none | localStorage |
| 5 | Display | `/settings/display` | `/_partials/settings/display` | `settings/display.html` | none | localStorage |
| 6 | Notifications | `/settings/notifications` | `/_partials/settings/notifications` | `settings/notifications.html` | none (+ browser `Notification` API) | localStorage |
| 7 | Providers → Credentials | `/credentials` (`/settings/providers` redirects) | `/_partials/settings/credentials` | `credentials.html` (+ `settings/providers.html` redirect) | `serf/instance/{list,create,edit,remove,setDefault}`, `serf/auth/{apiKey/set,login/start,login/complete,logout,device/start,device/poll}` | server |
| 8 | Agents | `/settings/agents` | `/_partials/settings/agents` | `settings/agents.html` | none | server (read-only) |
| 9 | Serf launch | `/settings/launch-serf` | `/_partials/settings/launch-serf` | `settings/launch-serf.html` | `serf/launch/{schema,getLayer,setLayer,resolve}` | server (global layer) |
| 10 | Codex launch | `/settings/launch-codex` | `/_partials/settings/launch-codex` | `settings/launch-codex.html` | none | server (read-only) |
| 11 | In-repo config | `/settings/inrepo` | `/_partials/settings/inrepo` | `settings/inrepo.html` | `serf/launch/{resolve,trustRepo}` | server |
| 12 | Marketplaces & Plugins | `/settings/plugins-manager` | `/_partials/settings/plugins-manager` | `settings/plugins-manager.html` | `serf/marketplace/{list,add,remove,refresh,browse}`, `serf/plugin/{list,install,upgrade,remove,enable,disable,setAutoUpgrade}` | server |
| 13 | Plugins (dirs) | `/settings/plugins` | `/_partials/settings/plugins` | `settings/plugins.html` | `serf/launch/{getLayer,setLayer}`, `serf/path/validate` | server (global layer) |
| 14 | Skills (dirs) | `/settings/skills` | `/_partials/settings/skills` | `settings/skills.html` | `serf/launch/{getLayer,setLayer}`, `serf/path/validate` | server (global layer) |
| 15 | MCP servers | `/settings/mcp` | `/_partials/settings/mcp` | `settings/mcp.html` | `serf/launch/{getLayer,setLayer}`, `serf/path/validate` (+ server-rendered probe data, no RPC) | server (global layer) |
| 16 | Hub | `/settings/hub` | `/_partials/settings/hub` | `settings/hub.html` | none | server (read-only) |
| 17 | Storage | `/settings/storage` | `/_partials/settings/storage` | `settings/storage.html` | none | server (read-only) |
| 18 | Per-project | `/settings/project?cwd=` | `/_partials/settings/project?cwd=` | `settings/project.html` | `serf/launch/{schema,getLayer,setLayer}` | server (project layer) |

Appendices A (`assets/settings-pickers.js`) and B (`assets/launchconfig.js`'s
`LaunchConfigControls` engine) are cross-cutting infrastructure several sections above depend on;
their behaviors are listed once, not repeated per section.

---

## 1. Settings shell & navigation

Files: `templates/partials/settings.html`, `assets/settings-shell.js`. Appwire: none.

- [ ] Left nav is grouped into 3 labeled clusters ("Agents & models", "Extensions", "Daemon") plus
      5 ungrouped top links (General/Theme/Transcript/Display/Notifications), in this fixed order —
      `templates/partials/settings.html:13-31`
- [ ] Each nav link is htmx-augmented: real `href` (no-JS/deep-link fallback), `hx-get` to
      `/_partials/settings/{section}` swapped via `innerHTML` into `#settings-content`,
      `hx-push-url` to the canonical page path — `templates/partials/settings.html:13-31`
- [ ] Active link gets `.active` via server-side `{{if eq .Active "section"}}` string compare —
      `templates/partials/settings.html:13-31`
- [ ] Credentials nav item is labeled "Providers & credentials" and is the one exception to the
      `/settings/{section}` path pattern: it routes to `/credentials` /
      `/_partials/settings/credentials` — `templates/partials/settings.html:19`
- [ ] Content-pane title (`.title[data-settings-section]`) is set server-side to `{{.Active}}` (used
      as a signal by the mobile pane-toggle logic below) — `templates/partials/settings.html:5`
- [ ] Nav filter (`data-settings-nav-filter` input) live-filters `.settings-nav-link` rows by
      case-insensitive substring match on link text, delegated on `document.body` (survives htmx
      swaps) — `assets/settings-shell.js:2-23`
- [ ] Nav filter also hides a `.settings-nav-section` header when every link between it and the next
      header is hidden — `assets/settings-shell.js:13-22`
- [ ] Mobile nav-as-page: `body.dataset.settingsPane` toggles `"content"`/`"nav"` based on whether
      the title element's text is non-empty — `assets/settings-shell.js:30-36`
- [ ] Back button (`.settings-nav-back`, starts `hidden`) is shown/hidden via explicit JS attribute
      toggling, not CSS alone, because HTML `hidden` overrides `display` — `assets/settings-shell.js:38-47`
- [ ] Back-button click resets pane to `"nav"`, re-hides itself, and does a **client-only**
      `history.pushState({}, "", "/settings")` — explicitly not an htmx fetch; the nav DOM already
      present is reused, nothing is re-fetched — `assets/settings-shell.js:51-61`
- [ ] `syncPane()` reruns on `DOMContentLoaded` and on every `htmx:afterSwap` whose target id is
      `workspace` or `settings-content` — `assets/settings-shell.js:65-74`

## 2. General

File: `templates/partials/settings/general.html`. Appwire: none — fully server-rendered, read-only.

- [ ] No inline `<script>`, no appwire/localStorage calls anywhere on this page —
      `templates/partials/settings/general.html:1-46`
- [ ] Fields rendered, in order: Hub address, Bearer token, Run dir, State dir, Past index (path +
      optional size), Spawn timeout, Past results per page, Hub config (static path text), Hub
      version (+ optional commit) — `templates/partials/settings/general.html:6-44`
- [ ] Bearer token value is never rendered — only a fixed `••••••••••••` mask plus an optional age;
      copy explains it's long-lived, persisted at `auth-token` in the state dir, reused across
      restarts, delete-the-file-to-invalidate — `templates/partials/settings/general.html:10-13`
- [ ] Every field's only "edit" path is external ("Edit `~/.serf/hub.toml`") — no in-UI control —
      `templates/partials/settings/general.html:3,39`
- [ ] Field set overlaps (byte-identical copy for shared fields) with Hub (§16) and Storage (§17) —
      confirm the rewrite intentionally dedupes rather than accidentally drops one —
      `templates/partials/settings/general.html` vs `settings/hub.html`, `settings/storage.html`

## 3. Theme

Files: `templates/partials/settings/theme.html`, `assets/settings-appearance.js`,
`assets/theme.js`. Appwire: **none** — every control here is localStorage-only, explicitly "Saved
per-browser" per the page copy.

- [ ] "Color theme" radio (system/dark/light) calls `window.serfHub.setTheme(v === "system" ? null : v)`
      — `assets/settings-appearance.js:12-17`, `templates/partials/settings/theme.html:8-13`
- [ ] `setTheme`: for `"light"`/`"dark"` sets `<html data-theme>` and localStorage key
      `serf-hub.theme`; for anything else (i.e. `null`/system) **removes** the attribute and the
      key entirely — "system" is represented by absence, never by the literal string `"system"` —
      `assets/theme.js:1-12`
- [ ] Theme change shows a success toast `"Theme: {value}"` — `assets/settings-appearance.js:15`
- [ ] "Phone density" radio (compact/comfortable), default **compact**; localStorage key
      `serf-hub.phone-density`; mirrored onto `document.body.dataset.phoneDensity` —
      `assets/settings-appearance.js:19-24,78-81`, `templates/partials/settings/theme.html:17-24`
- [ ] "Sidebar mode" radio (auto/pane/rail, rail labeled "Collapsed"): delegates to
      `window.SerfSidebar.applySidebarMode(v)` when present, else falls back to writing localStorage
      key `serf-hub.sidebar.rail` directly — `assets/settings-appearance.js:26-33`,
      `templates/partials/settings/theme.html:27-35`
- [ ] "Font size" radio (s/m/l/xl), default **m**. The current React frontend
      stores it under localStorage key `serf.prefs.fontSize` and mirrors it onto
      `document.body.dataset.fontSize` via `setFontSize` / `applyFontSize` —
      `cmd/serf-hub/frontend/src/stores/prefs.ts`. The retired legacy page used
      `serf-hub.appearance.fontSize` — `assets/settings-appearance.js:36-40,84-87`,
      `templates/partials/settings/theme.html:39-46`
- [ ] None of the 4 radio groups carry a server-rendered `checked` attribute — correctness depends
      entirely on `applyAppearanceState()` running after load/swap; if it doesn't run, no option
      appears selected even though a default is in effect — `templates/partials/settings/theme.html:8-46`
      (no `checked` present) vs `assets/settings-appearance.js:47-70`
- [ ] `applyAppearanceState()` re-checks the correct radio for all 4 groups (theme from
      `serf-hub.theme`/"system"; density/font-size/sidebar-mode as above) and reruns on
      `DOMContentLoaded` + every `htmx:afterSwap` — `assets/settings-appearance.js:44-74`
- [ ] Phone-density and font-size are *additionally* applied to `document.body.dataset` in two
      standalone IIFEs that run at script-parse time (not gated on `DOMContentLoaded`), so the CSS
      gates are correct before any settings pane has ever opened — `assets/settings-appearance.js:76-87`
      — theme and sidebar-mode get no equivalent early-apply in this file (presumed handled by
      `theme.js`/`sidebar.js` themselves, not verified here)

## 4. Transcript

Files: `templates/partials/settings/transcript.html`, `assets/settings-transcript.js`. Appwire:
none — localStorage key `serf-hub.transcript.systemStatus` (JSON).

- [ ] 4 checkboxes, all default **OFF**: Round timings (`roundTimings`), Hook exits — all
      (`hookExitsAll`), Hook exits — normal only (`hookExitsNormal`), Prompt Loaded
      (`promptLoaded`) — `templates/partials/settings/transcript.html:6-44`,
      `assets/settings-transcript.js:44-47` (missing/unparseable key ⇒ `{}` ⇒ every field falsy)
  - **Addition:** the React pane adds a fifth, also default off — Token counts (`tokenCounts`,
    `serf.prefs.transcriptTokenCounts`), gating the per-turn meta line's `↑in ↓out` segment. The old
    UI had no preference for it at all.
- [ ] Copy clarifies "Hook exits (all)" is a superset of "Hook exits (normal only)" — both can be
      independently on — `templates/partials/settings/transcript.html:23,33`
- [ ] Static server-rendered label spans are hardcoded `"OFF"` for all four, matching the
      JS-computed default (no flash-of-wrong-state) — `templates/partials/settings/transcript.html:10,19,29,39`
- [ ] On change: persists `{...prev, [key]: checked}`, updates the ON/OFF label, dispatches
      `document` `CustomEvent("serf-hub:transcript-system-status-changed", {detail:{key,value}})`,
      shows a "Settings saved" success toast — `assets/settings-transcript.js:14-25`
- [ ] `applyTranscriptState()` resyncs all 4 checkboxes + labels on `DOMContentLoaded` and every
      `htmx:afterSwap` — `assets/settings-transcript.js:31-37,52-53`

## 5. Display

Files: `templates/partials/settings/display.html`, `assets/settings-display.js`. Appwire: none —
localStorage key `serf-hub.composer` (JSON `{enterToSend, showCost}`).

- [ ] "Enter sends" toggle, default **OFF**: off ⇒ ⌘/Ctrl-Enter sends, Enter inserts newline; on ⇒
      Enter sends, Shift-Enter inserts newline, and "the steer keyboard shortcut is unavailable in
      this mode" (the steer *button* still works) —
      `templates/partials/settings/display.html:5-14`, `assets/settings-display.js:16`
- [ ] "Show estimated cost" toggle, default **ON**: shows an estimated `~$` figure from catalog
      pricing next to token counts, explicitly "an estimate, not a billing-exact figure" —
      `templates/partials/settings/display.html:15-24`, `assets/settings-display.js:17`
  - **Deliberate divergence:** the React pane defaults this **OFF**. The toggle now governs only the
    per-turn transcript meta line's cost segment, whose two siblings (Transcript → Round timings,
    Transcript → Token counts) are also opt-in; the footer status strip's session-total cost shows
    unconditionally. The `serf.prefs.showCost` key name and `"1"/"0"` encoding are unchanged, so a
    browser that has ever toggled the switch keeps its stored value.
- [ ] Static label spans match computed defaults pre-JS ("OFF" for enterToSend, "ON" for showCost) —
      `templates/partials/settings/display.html:10,20`
- [ ] enterToSend change: persist, sync label, call `applyComposerKeybindHints()`, toast "Settings
      saved" — `assets/settings-display.js:33-40`
- [ ] showCost change: persist, sync label, mirror onto `document.body.dataset.showCost` (CSS gate
      `body[data-show-cost="false"]`), toast "Settings saved" — `assets/settings-display.js:42-49`
- [ ] `applyComposerKeybindHints()` rewrites the send button's `kbd` glyph ("↵" on / "⌘↵" off) and
      the steer-trigger's `kbd` glyph ("" on / "⇧↵" off) — `assets/settings-display.js:70-77`
- [ ] `applyDisplayState()` + `applyComposerKeybindHints()` rerun on `DOMContentLoaded` and every
      `htmx:afterSwap`; `body.dataset.showCost` is additionally set once at script-parse time so the
      CSS gate is correct before any settings pane renders — `assets/settings-display.js:79-88`
- [ ] `window.SerfSettingsDisplay` exposes `{readComposerPrefs, writeComposerPrefs,
      syncToggleState, applyComposerKeybindHints}` for reuse elsewhere (e.g. the composer itself) —
      `assets/settings-display.js:28`

## 6. Notifications

Files: `templates/partials/settings/notifications.html`, `assets/settings-notifications.js`.
Appwire: none — localStorage key `serf-hub.notifications` (JSON); OS toggle also drives the browser
`Notification.requestPermission()` Web API.

- [ ] **Discrepancy to resolve/replicate deliberately:** page copy says "Title and favicon default
      on; OS notification and sound are opt-in" (`templates/partials/settings/notifications.html:3`),
      but both the static markup and the JS default (`!!prefs[key]` against an empty/missing
      object) put **all four** toggles at OFF, including title and favicon —
      `templates/partials/settings/notifications.html:6-44` (all spans say "OFF") vs
      `assets/settings-notifications.js:82-86`
- [ ] 4 checkboxes: Title bar count (`title`), Favicon dot (`favicon`), OS notification (`os`),
      Sound (`sound`) — `templates/partials/settings/notifications.html:5-44`
- [ ] OS toggle special-case: turning **ON** while `"Notification" in window &&
      Notification.permission === "default"` calls `Notification.requestPermission()` first; only
      `"granted"` commits (persist + label + event + toast) — `assets/settings-notifications.js:51-58`
- [ ] Denied permission force-reverts the checkbox to unchecked, persists `false`, shows a
      **warning** (not error) toast "Browser denied notification permission" —
      `assets/settings-notifications.js:42-49,55`
- [ ] If `requestPermission()` itself throws/rejects, the checkbox still reverts and persists
      `false`, but **no toast is shown at all** (empty-string reason suppresses the toast) —
      `assets/settings-notifications.js:57`
- [ ] Turning OS notifications **OFF**, or toggling when permission is already
      `"granted"`/`"denied"` (not `"default"`), skips the permission-request branch entirely and
      commits unconditionally — turning the setting off never revokes/rechecks the browser
      permission — `assets/settings-notifications.js:51,60`
- [ ] Every non-OS-gated commit + the loudScope radio change both dispatch `document`
      `CustomEvent("serf-hub:notifications-changed", {detail:{key,value}})` and toast "Settings
      saved" — `assets/settings-notifications.js:27-36,64-75`
- [ ] "Loud for" radio (`loudScope`): `"asks"` (Questions & errors, default) vs `"all"` (Everything
      needing me); governs which state-transitions get OS notification + sound — title/favicon
      count always reflects everything regardless — `templates/partials/settings/notifications.html:45-54`,
      `assets/settings-notifications.js:89`
- [ ] Neither the 4 checkboxes nor the loudScope radios carry server-rendered default state
      (`checked` absent throughout); correctness depends on `applyNotifState()` running on
      load/swap — `templates/partials/settings/notifications.html` (no `checked` anywhere) vs
      `assets/settings-notifications.js:81-92`
- [ ] File is explicitly distinct from `assets/notifications.js` (the runtime engine that reads
      these same prefs to actually drive alerts) — self-documented —
      `assets/settings-notifications.js:8-10`

## 7. Providers → Credentials

Files: `templates/partials/settings/providers.html` (legacy redirect),
`templates/partials/credentials.html` (the real UI, 556 lines, entirely inline `<script>`, no
separate `credentials*.js` file exists).

**Appwire:** `launchconfig.instanceList`→`serf/instance/list`,
`instanceCreate`→`serf/instance/create`, `instanceEdit`→`serf/instance/edit`,
`instanceRemove`→`serf/instance/remove`, `instanceSetDefault`→`serf/instance/setDefault`,
`authApiKeySet`→`serf/auth/apiKey/set`, `authLoginStart`→`serf/auth/login/start`,
`authLoginComplete`→`serf/auth/login/complete`, `authLogout`→`serf/auth/logout`,
`authDeviceStart`→`serf/auth/device/start`, `authDevicePoll`→`serf/auth/device/poll`.
(`launchconfig.authList`/`authStatus` exist but this page never calls them.)

### 7a. `providers.html` legacy alias

- [ ] Static copy + manual link to `/credentials` — `templates/partials/settings/providers.html:3-7`
- [ ] On load, if `window.location.pathname === "/settings/providers"` **exactly**, does a hard
      `window.location.replace("/credentials")` (real navigation, replaces history entry, not an
      htmx swap) — `templates/partials/settings/providers.html:9-12`
- [ ] Redirect condition is strict-equality on pathname — reachable only via direct URL/back-compat
      link now that the nav no longer points here — `templates/partials/settings/providers.html:10`

### 7b. Credentials — data model & layout

- [ ] Static header copy: storage at `~/.serf/credentials.toml` (chmod 600); "env vars in the hub
      process take precedence only when no file entry exists"; "The UI never displays stored
      values" — `templates/partials/credentials.html:4-8`
- [ ] Instances are grouped into `<li class="credentials-type-group">` by `.type`, in first-seen
      order from the RPC response (not re-sorted client-side) — `templates/partials/credentials.html:295-320`
- [ ] Empty state: "No provider instances configured." — `templates/partials/credentials.html:324`
- [ ] A single module-level `openEditor` variable (+ `devicePollTimer`) is the entire page's
      edit-state — only one row/add editor can be open anywhere at a time; opening a second editor
      silently discards unsaved input in the first, no dirty-check — `templates/partials/credentials.html:25-30`

### 7c. Instance row rendering

- [ ] Status dot: `idle` for file/oauth/env sources, `ended` for absent/none/anything-else —
      `templates/partials/credentials.html:124-130`
- [ ] Source label per `activeSource`: file→"Configured via stored API key", env→"Configured via
      environment variable", oauth→"Configured via OAuth", absent→"Not configured",
      none→"No credentials required"; unknown values fall back to the raw escaped string —
      `templates/partials/credentials.html:114-122`
- [ ] When >1 credential layer is present simultaneously, **all** are listed (not just the
      effective one), fixed precedence oauth > file > env, each tagged "effective" (idle badge) or
      "shadowed" (ended badge) — documented reason: "OpenAI can carry an OAuth sign-in shadowing a
      stored key" — `templates/partials/credentials.html:132-156`
- [ ] OAuth layer label appends the signed-in email in parens when `storedEmail` present —
      `templates/partials/credentials.html:138`
- [ ] `★ default` badge shown when `isDefault` — `templates/partials/credentials.html:173`
- [ ] `apiStyle`/`baseUrl` shown as dim trailing text (apiStyle preferred, then " · base {url}"; or
      "base {url}" alone if only baseUrl present) — `templates/partials/credentials.html:164-166`
- [ ] Row actions conditionally rendered: Set/Replace key only if `authModes` includes `"apiKey"`;
      Sign in…/Refresh OAuth only if `authModes` includes `"oauth"`; Clear only if `activeSource` is
      `"file"` or `"oauth"`; Edit and Remove always present; ★ make default only when not already
      default — `templates/partials/credentials.html:179-186`

### 7d. Set API key flow

- [ ] "Set key"/"Replace key" (label depends on `hasStoredFile`) opens an inline `password` input
      form under the row — `templates/partials/credentials.html:180,191-201,393-395`
- [ ] Submitting an empty (trimmed) value silently cancels — no RPC call, no error —
      `templates/partials/credentials.html:471-475`
- [ ] Non-empty submit calls `authApiKeySet(name, value)`; success closes editor + refresh + toast
      "API key saved for {name}"; failure shows an inline error paragraph *and* a "Save failed:
      {message}" toast — `templates/partials/credentials.html:476-484`
- [ ] Focus restored to the password input on every re-render while this editor is open —
      `templates/partials/credentials.html:349-350`

### 7e. Edit instance flow

- [ ] "Edit" form shows an API-style radio (responses/chat-completions), pre-checked to current
      value, **only when `inst.type === "openai"`**; Base URL input always, pre-filled —
      `templates/partials/credentials.html:232-253`
- [ ] Submit calls `instanceEdit({name, apiStyle, baseUrl})` (apiStyle `""` when the radio block
      isn't rendered); success closes editor + refresh + toast "Saved {name}"; failure shows inline
      error + "Edit failed" toast — `templates/partials/credentials.html:504-519`
- [ ] Focus restored to the Base URL input on re-render — `templates/partials/credentials.html:354`

### 7f. Add instance flow

- [ ] Triggered by the page-level "+ Add provider instance" button (outside any row) —
      `templates/partials/credentials.html:9,371-374`
- [ ] Add form appended at the end of the list; Type `<select>` populated from
      `data.availableTypes`; required Name input; API-style radio block starts `hidden`, un-hidden
      only when the Type select's value is `"openai"` (live on `change`); optional Base URL —
      `templates/partials/credentials.html:258-293,333-343`
- [ ] Client-side required checks before any RPC: Type non-empty ("Type is required."), Name
      non-empty after trim ("Name is required.") — `templates/partials/credentials.html:520-532`
- [ ] `apiStyle` is included in the payload **only** when `type === "openai"` — forced to `""` for
      every other type even if a stale radio selection exists in the hidden block (comment: backend
      rejects `api_style` on non-openai types) — `templates/partials/credentials.html:535-539`
- [ ] Submit calls `instanceCreate({type, name, apiStyle, baseUrl})`; success closes + refresh +
      toast "Created instance {name}"; failure shows inline error + "Create failed" toast —
      `templates/partials/credentials.html:540-548`
- [ ] Focus restored to the Name input while the add form is open —
      `templates/partials/credentials.html:356`
- [ ] Any "Cancel" (add form or row editor) discards `openEditor` and refreshes with **no** RPC
      call — `templates/partials/credentials.html:382-386,396-399`

### 7g. OAuth — browser-redirect fallback flow

- [ ] "Sign in…"/"Refresh OAuth" always starts with `authDeviceStart(name)` —
      `templates/partials/credentials.html:58-61,400-403`
- [ ] If the response has `fallback` truthy: calls `authLoginStart(name)`, opens the returned `url`
      in a new tab (`window.open(url, "_blank", "noopener")`), switches editor to
      `kind:"oauth-redirect"` holding `{flowId, authUrl}` —
      `templates/partials/credentials.html:62-68`
- [ ] Redirect editor shows a "Re-open authorize URL" link (popup-blocked recovery) and a text
      input for pasting the full redirect URL back — `templates/partials/credentials.html:203-215`
- [ ] Submit calls `authLoginComplete(name, flowId, redirectUrl)`; empty (trimmed) input silently
      cancels with no RPC; success closes + refresh + toast "Signed in to {name}"; failure shows
      inline error + "Sign-in failed" toast — `templates/partials/credentials.html:485-503`
- [ ] Focus restored to the redirect URL input on re-render —
      `templates/partials/credentials.html:351-352`

### 7h. OAuth — device-code flow

- [ ] Non-fallback path sets editor to `kind:"device"` with `{flowId, userCode, verificationUrl}`
      and starts polling at `max(1, intervalSeconds || 5) * 1000` ms —
      `templates/partials/credentials.html:69-71,77-108`
- [ ] A poll-tick error attaches its message to the still-open device editor and re-renders; polling
      is **not** stopped by a transient poll error — `templates/partials/credentials.html:79-89`
- [ ] Each tick first confirms the editor is still the same open device flow
      (`kind==="device" && flowId matches`) before acting — a stale poll from an abandoned flow is a
      no-op — `templates/partials/credentials.html:90`
- [ ] `state:"authorized"` stops polling, clears editor, refreshes, toasts "Signed in to {name}" —
      `templates/partials/credentials.html:91-97`
- [ ] `state:"expired"` stops polling, sets `expired:true` + "Code expired — start again.", editor
      stays open showing the error + a restart button — `templates/partials/credentials.html:98-104`
- [ ] Any other state just reschedules the next poll at the same interval —
      `templates/partials/credentials.html:105`
- [ ] Device editor shows the code in `.credentials-device-code`; status line is the error text, a
      "couldn't copy automatically" hint, or "Waiting for you to authorize…" —
      `templates/partials/credentials.html:217-223`
- [ ] "Copy code" uses async Clipboard API when available, else a hidden-textarea
      `execCommand("copy")` fallback for non-secure-context remote hubs —
      `templates/partials/credentials.html:32-56`
- [ ] "Send me to OpenAI" is `disabled` until the code has been copied at least once; click opens
      `verificationUrl` in a new tab — `templates/partials/credentials.html:225-227,411-414`
- [ ] Once `expired` or `error`, the action row swaps to a single "Start again" button instead of
      Copy/Send — `templates/partials/credentials.html:218-227`
- [ ] Cancel on an open editor calls `stopDevicePolling()` (clears the pending timeout) before
      discarding `openEditor` — `templates/partials/credentials.html:396-399`

### 7i. Clear / Remove / Set default

- [ ] "Clear" requires `confirm("Clear stored credentials for {name}?")`; on confirm: stop polling,
      `authLogout(name)`, close editor, refresh, toast "Credentials cleared for {name}"; failure
      toasts "Clear failed" — `templates/partials/credentials.html:415-425`
- [ ] "Remove" requires `confirm('Remove instance "{name}"? This will also clear its stored
      credentials.')`; on confirm: stop polling, `instanceRemove(name)`, close editor, refresh,
      toast "Removed instance {name}"; failure toasts "Remove failed" —
      `templates/partials/credentials.html:429-439`
- [ ] "★ make default" has **no** confirm; calls `instanceSetDefault(name)` directly; refresh on
      success with **no success toast** (silent success — only a failure toast exists) —
      `templates/partials/credentials.html:440-446`

### 7j. Cross-cutting

- [ ] Every dynamic string interpolated into `innerHTML` goes through the same `escapeHtml`
      (escapes `&<>"` only — **not** `'`) — `templates/partials/credentials.html:110-112` and every
      `render*` function
- [ ] All rendering is a pure re-render of `{instances, availableTypes, openEditor}` from scratch on
      every `refresh()` — no incremental DOM patching —
      `templates/partials/credentials.html:307-363`

## 8. Agents

File: `templates/partials/settings/agents.html`. Appwire: none — fully server-rendered, read-only.

- [ ] Server-rendered list of `.Agents`; each row shows `.Name` plus either an "open in editor ↗"
      link to `.EditPath` (`target="_blank" rel="noopener"`) or a dim "built-in" label when
      `.EditPath` is empty — `templates/partials/settings/agents.html:6-13`
- [ ] Empty state: "No agents discovered." — `templates/partials/settings/agents.html:16-18`
- [ ] No client JS; no add/remove/edit affordance in this view — editing happens externally in the
      linked editor — `templates/partials/settings/agents.html:1-19`

## 9. Serf launch

File: `templates/partials/settings/launch-serf.html`. Engine: `assets/launchconfig.js`
`LaunchConfigControls` (see Appendix B for the shared rendering/validation contract).

**Appwire:** `schema()`→`serf/launch/schema`, `getLayer("/","global")`→`serf/launch/getLayer`,
`resolve("/")`→`serf/launch/resolve`, `setLayer("/","global",…)`→`serf/launch/setLayer`.

- [ ] Form root: `data-launch-settings-root data-launch-settings-layer="global"`, starts
      `data-loaded="false"` — `templates/partials/settings/launch-serf.html:7`
- [ ] Load sequence is **sequential**, not parallel: `await schema()` then `await getLayer("/",
      "global")` — `templates/partials/settings/launch-serf.html:61-62`
- [ ] Renders via `LaunchConfigControls.render(form, {mode:"settings", layer:"global",
      options:schema.options, current, includeEnvFallbacks:false})` — env-fallback hints explicitly
      OFF on this page — `templates/partials/settings/launch-serf.html:64-70`
- [ ] Loading placeholder hides and `form.dataset.loaded` becomes `"true"` only on success; on
      failure it is left at `"false"` forever (no distinct error state, unlike §18 Project) —
      `templates/partials/settings/launch-serf.html:71-72` vs `91-94`
- [ ] After render, a best-effort `resolve("/")` populates the diagnostics panel, wrapped in its own
      try/catch that silently swallows failure ("non-fatal") —
      `templates/partials/settings/launch-serf.html:74-75`
- [ ] Submit: `preventDefault()`, then `LaunchConfigControls.validate(form)` — returns early (no
      save attempted) if invalid — `templates/partials/settings/launch-serf.html:78-79`
- [ ] On valid submit: `setLayer("/", "global", collect(form))`; diagnostics are re-rendered from
      the object **returned by `setLayer`**, not a fresh `resolve()` call —
      `templates/partials/settings/launch-serf.html:81-82`
- [ ] Success status text "Saved at {locale time}" self-clears after 5000ms unless it starts with
      "Error:" (persists indefinitely) — `templates/partials/settings/launch-serf.html:46-58,83`
- [ ] Success also shows toast "Launch defaults saved" —
      `templates/partials/settings/launch-serf.html:84`
- [ ] On save failure: `showBackendError(form, err)` tried first (only actually surfaces inline for
      the one env-credential message shape, see Appendix B); status line always gets "Error:
      {message}"; toast "Save failed" always shown regardless —
      `templates/partials/settings/launch-serf.html:85-89`
- [ ] On initial `schema()`/`getLayer()` failure: loading placeholder text becomes "Failed to load
      launch settings."; status shows "Error: {message}"; form never reaches
      `data-loaded="true"` — `templates/partials/settings/launch-serf.html:91-94`
- [ ] Diagnostics block (`#launch-diagnostics`, `role="status" aria-live="polite"`) starts `hidden`;
      shown only when `resolved.diagnostics` non-empty; renders a "Warnings" heading + `<ul>` of
      `"{field}: {message}"` (field segment omitted if falsy) —
      `templates/partials/settings/launch-serf.html:10,28-44`
- [ ] Page copy: values here apply "to every serf spawn unless overridden by a project layer or
      per-launch" — `templates/partials/settings/launch-serf.html:3-6`
- [ ] Field kinds/grouping/validation/collection follow the shared engine exactly — see Appendix B
      (not re-listed per field here)

## 10. Codex launch

File: `templates/partials/settings/launch-codex.html`. Appwire: none — fully server-rendered,
read-only.

- [ ] Server-rendered list of `.CodexLaunches`, one `<h3>`+`<dl>` block per entry keyed by `.ID` —
      `templates/partials/settings/launch-codex.html:9-42`
- [ ] Per-entry fallback defaults shown inline when the Go value is empty: Binary→`codex`, Working
      dir→"(inherited)", Listen→`ws://127.0.0.1:0`, Timeout→`30s` —
      `templates/partials/settings/launch-codex.html:13-31`
- [ ] `.Env` block only renders `{{if .Env}}`; values are **always** redacted to `KEY=…` regardless
      of actual content — `templates/partials/settings/launch-codex.html:32-40`
- [ ] Empty state shows a worked `[[codex_launches]]` TOML example plus "Restart the hub after
      editing hub.toml." — `templates/partials/settings/launch-codex.html:43-58`
- [ ] No client JS; no in-UI create/edit/delete — purely a projection of `hub.toml`, edits require
      external file edit + hub restart — `templates/partials/settings/launch-codex.html:1-59`

## 11. In-repo config

File: `templates/partials/settings/inrepo.html`. Appwire: `resolve(cwd)`→`serf/launch/resolve`,
`trustRepo(cwd,hash)`→`serf/launch/trustRepo`.

- [ ] cwd input pre-filled from `localStorage.getItem("lastCwd")` on load — this file only *reads*
      that key; some other part of the app is presumed to write it (not verified here) —
      `templates/partials/settings/inrepo.html:21`
- [ ] cwd input has **no** directory-picker wiring (`data-settings-dir-input` absent) — unlike every
      other free-text path input across §§13-15, this field gets no autocomplete/browse assist —
      `templates/partials/settings/inrepo.html:10`
- [ ] `change` (not `input`) on the cwd field triggers `refresh()` — re-resolution fires on
      blur-with-change/Enter, not every keystroke — `templates/partials/settings/inrepo.html:69`
- [ ] Empty cwd short-circuits to a static "Enter a working directory." message, no RPC call —
      `templates/partials/settings/inrepo.html:25`
- [ ] RPC failure replaces the status block with `.settings-error` "Failed to load: {message}" —
      `templates/partials/settings/inrepo.html:29-35`
- [ ] `repo.trust === "absent"` shows "No `.serf/launch.toml` in `{cwd}`." and stops — no preview,
      no trust button — `templates/partials/settings/inrepo.html:37-40`
- [ ] Otherwise shows the raw file `<pre>` preview (if `repo.preview` present) plus trust-state copy
      keyed by exactly 4 states: `trusted` (shows content hash), `untrusted`, `changed` (content
      changed since trusted), `rejected` (previously rejected) —
      `templates/partials/settings/inrepo.html:42-47`
- [ ] "Trust this file" button renders for every trust state except `trusted` —
      `templates/partials/settings/inrepo.html:48,52`
- [ ] Trust click calls `trustRepo(cwd, repo.hash)` then re-`refresh()`s; on failure an error
      paragraph is **appended** to the status block (preview/note stay visible alongside it, not
      replaced) — `templates/partials/settings/inrepo.html:55-66`
- [ ] Page copy: hub only applies in-repo config "after you confirm trust" —
      `templates/partials/settings/inrepo.html:4-6`

## 12. Marketplaces & Plugins manager

File: `templates/partials/settings/plugins-manager.html` (604 lines, entirely inline `<script>`).
Wrapper: `assets/plugins.js` (`window.pluginsAdmin`).

**Appwire:** `marketplaceList`→`serf/marketplace/list`, `marketplaceAdd`→`serf/marketplace/add`,
`marketplaceRemove`→`serf/marketplace/remove`, `marketplaceRefresh`→`serf/marketplace/refresh`,
`marketplaceBrowse`→`serf/marketplace/browse`, `pluginList`→`serf/plugin/list`,
`pluginInstall`→`serf/plugin/install`, `pluginUpgrade`→`serf/plugin/upgrade`,
`pluginRemove`→`serf/plugin/remove`, `pluginEnable`→`serf/plugin/enable`,
`pluginDisable`→`serf/plugin/disable`, `pluginSetAutoUpgrade`→`serf/plugin/setAutoUpgrade` —
all defined in `assets/plugins.js:8-23` as thin wrappers over `SerfAppwire.request`.

### 12a. Load & top-level state

- [ ] Initial load runs `refreshMarketplaces()` + `refreshInstalled()` in parallel
      (`Promise.all`), then renders once both settle —
      `templates/partials/settings/plugins-manager.html:362-365,595-600`
- [ ] If the initial load throws, the **entire root** is replaced with one "Failed to load:
      {message}" paragraph — no partial UI, no retry affordance —
      `templates/partials/settings/plugins-manager.html:326-329,596-599`
- [ ] All state lives in plain module-level JS vars (`marketplaces`, `installed`,
      `expandedMarketplaces` Set, `browseCatalogs` cache map, `filterQuery`, `filterLoading`,
      `addMarketplaceOpen`, `loadError`) — nothing persisted across a full page reload —
      `templates/partials/settings/plugins-manager.html:31-42`
- [ ] `render()` fully replaces `root.innerHTML` with all 3 sections concatenated on every state
      change — no incremental patching —
      `templates/partials/settings/plugins-manager.html:325-348`
- [ ] `render()` preserves the browse-filter input's focus + selection range across the innerHTML
      replacement (only if it had focus beforehand) —
      `templates/partials/settings/plugins-manager.html:330-333,341-347`
- [ ] `render()` re-focuses the marketplace-name input whenever the add-marketplace form is open —
      `templates/partials/settings/plugins-manager.html:337-340`
- [ ] `render()` reruns `SettingsPickers.init(root)` every time so the directory picker on the
      add-marketplace "Local path" field keeps working post-rerender —
      `templates/partials/settings/plugins-manager.html:336`

### 12b. Marketplaces list section

- [ ] Each row shows name, source-kind dim badge, and a human `sourceLabel` (`github: {repo}` /
      raw `{url}` / raw `{path}` / `{url} ({path})` for git-subdir / raw kind string as fallback) —
      `templates/partials/settings/plugins-manager.html:19-28,129-141`
- [ ] Empty state: "No marketplaces registered. Add one below." —
      `templates/partials/settings/plugins-manager.html:186`
- [ ] "Refresh": `marketplaceRefresh(name)`, re-fetch the list, drop the cached browse catalog for
      that marketplace; if its node is currently expanded, immediately reload the catalog (loading
      state shown); if collapsed, just re-render (next expand lazily refetches) —
      `templates/partials/settings/plugins-manager.html:485-503`
- [ ] "Remove" requires `confirm('Remove marketplace "{name}"? Installed plugins from it are
      unaffected.')`; on confirm: `marketplaceRemove(name)`, drop cache, collapse node, refetch
      list, toast "Removed marketplace {name}" —
      `templates/partials/settings/plugins-manager.html:505-520`
- [ ] Both buttons wrapped in `withBusy` (disabled for the RPC's duration) —
      `templates/partials/settings/plugins-manager.html:487,508`

### 12c. Add-marketplace form

- [ ] "+ Add marketplace" button and the form are mutually exclusive in the section footer —
      `templates/partials/settings/plugins-manager.html:197,444-450`
- [ ] Source-kind radios: `url` (Git URL, default-checked), `github` (owner/repo), `directory`
      (Local path, with a `.sp-dir-wrap` Browse button + inline autocomplete) —
      `templates/partials/settings/plugins-manager.html:150-170`
- [ ] Only the field block matching the checked radio is visible, toggled live on `change` —
      `templates/partials/settings/plugins-manager.html:415-420,438-442`
- [ ] Name is optional ("defaults to the marketplace's own name") —
      `templates/partials/settings/plugins-manager.html:172-174`
- [ ] Submit builds `source` strictly from the checked radio (github→`{kind,repo}`,
      directory→`{kind,path}`, else→`{kind:"url",url}`) — stale text in a hidden field is never
      sent — `templates/partials/settings/plugins-manager.html:422-427`
- [ ] Submit calls `marketplaceAdd({name, source})`; success closes form + refreshes list + toasts
      "Added marketplace" (name appended only if non-empty); failure toasts "Add marketplace
      failed: {message}" (no inline field error) —
      `templates/partials/settings/plugins-manager.html:576-593`
- [ ] Submit button disabled for the RPC's duration via `withBusy` —
      `templates/partials/settings/plugins-manager.html:582`

### 12d. Browse tree + filter

- [ ] Every registered marketplace always appears as a collapsible tree node, independent of the
      filter — `templates/partials/settings/plugins-manager.html:217-250`
- [ ] A node's catalog is fetched lazily on first expand only; `browseCatalogs[name]` is a
      permanent-until-invalidated cache — re-expanding never re-fetches —
      `templates/partials/settings/plugins-manager.html:33-37,380-392`
- [ ] Node header shows a `(count)` badge only once its catalog has successfully loaded —
      `templates/partials/settings/plugins-manager.html:221`
- [ ] Expanded states: loading→"Loading…", error→"Failed to browse: {message}", empty
      catalog→"This marketplace has no plugins." —
      `templates/partials/settings/plugins-manager.html:225-238`
- [ ] Each browse row: plugin name, description, `· {category}` if present; already-installed
      plugins (matched by plugin+marketplace pair) show a static "Installed" badge instead of an
      Install button — `templates/partials/settings/plugins-manager.html:201-215,44-46`
- [ ] "Install" calls `pluginInstall(plugin, marketplace)`, refreshes installed list, toasts
      "Installed {plugin}"; failure toasts "Install failed: {message}"; wrapped in `withBusy` —
      `templates/partials/settings/plugins-manager.html:467-482`
- [ ] Filter input is debounced 150ms (helper duplicated from `search.js`'s pattern, per the file's
      own comment) — `templates/partials/settings/plugins-manager.html:113-125`
- [ ] Clearing the filter (empty/whitespace) immediately collapses every node with no debounce —
      `templates/partials/settings/plugins-manager.html:81-86,122`
- [ ] A non-empty filter first fetches every not-yet-cached marketplace's catalog in parallel
      (`filterLoading=true` renders "Loading marketplaces…" tree-wide meanwhile); a fetch
      superseded by a newer query is **not** cancelled — it still completes and populates the cache
      — `templates/partials/settings/plugins-manager.html:94-111,123`
- [ ] After catalogs load, matching marketplaces auto-expand and non-matching ones auto-collapse
      (name/description substring match, case-insensitive) —
      `templates/partials/settings/plugins-manager.html:76-92`
- [ ] While filtering, a marketplace whose catalog is unresolved (never cached / still loading /
      errored) stays visible (its loading/error row still renders) even though its match status is
      unknown — only a successfully-loaded, zero-match catalog is hidden entirely —
      `templates/partials/settings/plugins-manager.html:56-74,260-271`
- [ ] Within a visible, expanded, matching marketplace, only the individual plugin rows that match
      are shown — non-matching siblings hidden even though the marketplace itself is expanded —
      `templates/partials/settings/plugins-manager.html:229-236`
- [ ] Zero matches anywhere shows `No plugins match "{query}".` —
      `templates/partials/settings/plugins-manager.html:269-270`
- [ ] Manual chevron expand/collapse is independent of and compatible with the filter mechanism —
      both converge on the same `expandedMarketplaces` Set —
      `templates/partials/settings/plugins-manager.html:380-392,456-459`

### 12e. Installed section

- [ ] Status dot: `warning` if `broken`, else `ended` if `!enabled`, else `idle`; badges for
      broken/disabled(`!enabled`)/auto-upgrade(`autoUpgrade`); version shown as
      `v{version||"unknown"}` — `templates/partials/settings/plugins-manager.html:285-309`
- [ ] Empty state: "No plugins installed yet. Install one from Browse above." —
      `templates/partials/settings/plugins-manager.html:314`
- [ ] Enable/Disable button label reflects current state; click calls `pluginDisable` or
      `pluginEnable` as appropriate, refreshes, **no success toast** (failure only: "Toggle enable
      failed") — `templates/partials/settings/plugins-manager.html:303,529-539`
- [ ] "Auto-upgrade: on/off" toggles via `pluginSetAutoUpgrade(plugin, marketplace,
      !current.autoUpgrade)` — no success toast, failure toasts "Toggle auto-upgrade failed" —
      `templates/partials/settings/plugins-manager.html:304,540-549`
- [ ] "Upgrade" calls `pluginUpgrade`, refreshes, toasts "Checked {plugin} for upgrades" on
      success — the toast confirms only that an upgrade check ran, not that a new version actually
      installed — `templates/partials/settings/plugins-manager.html:305,550-559`
- [ ] "Remove" requires `confirm('Remove plugin "{plugin}"?')`; on confirm calls `pluginRemove`,
      refreshes, toasts "Removed {plugin}"; failure toasts "Remove failed" —
      `templates/partials/settings/plugins-manager.html:306,561-572`
- [ ] All 4 row actions wrapped in `withBusy` —
      `templates/partials/settings/plugins-manager.html:530,541,551,563`

### 12f. Cross-cutting

- [ ] `withBusy(btn, fn)` disables the triggering button for the duration of `fn()`, re-enabling in
      a `finally` even if `fn()` re-renders and detaches the original node (documented as a
      harmless no-op) — `templates/partials/settings/plugins-manager.html:399-411`
- [ ] `toastError(action, err)` is the single shared failure-toast formatter: `"{action} failed:
      {message}"`, error severity — `templates/partials/settings/plugins-manager.html:394-397`
- [ ] All click/submit/input/change handlers are attached once via delegation on `root` at IIFE
      init; `render()` only ever replaces `innerHTML`, never re-attaches listeners —
      `templates/partials/settings/plugins-manager.html:429-593`

## 13. Plugins (directories)

File: `templates/partials/settings/plugins.html`. Appwire:
`getLayer("/","global")`→`serf/launch/getLayer`, `setLayer("/","global",{...current,pluginDirs})`→`serf/launch/setLayer`,
`validatePath(v,"dir")`→`serf/path/validate`.

- [ ] Current global layer is fetched once on IIFE start and kept as the `current` closure var;
      every mutation spreads `{...current, pluginDirs: dirs}` back through `setLayer` —
      `templates/partials/settings/plugins.html:13-15,92,108`
- [ ] Full list is destroyed and rebuilt (`root.replaceChildren()`) on every `render()` —
      `templates/partials/settings/plugins.html:16-17`
- [ ] Empty state: "No plugin directories. Add one below." —
      `templates/partials/settings/plugins.html:35-37`
- [ ] Count header pluralizes ("1 entry" vs "N entries") —
      `templates/partials/settings/plugins.html:26-27`
- [ ] Remove (×) per row has **no** confirm dialog — immediate splice + save —
      `templates/partials/settings/plugins.html:89-95`
- [ ] Add requires non-empty trimmed input; validates via `validatePath(v,"dir")` first; on
      invalid, shows inline `.row-error` (server `error` field, else literal "path does not
      exist"), does **not** save or clear the input — `templates/partials/settings/plugins.html:97-105`
- [ ] On valid add, uses the server-canonicalized `valid.path` if present else the raw trimmed
      input, pushes, saves, clears input, re-renders —
      `templates/partials/settings/plugins.html:106-110`
- [ ] Add row wires both an explicit "Browse" button (`data-settings-dir-picker`) and inline
      typeahead (`data-settings-dir-input`) via `SettingsPickers.init(form)` —
      `templates/partials/settings/plugins.html:71-76,88`
- [ ] Top-level load failure replaces the whole root with `.settings-error` "Failed to load:
      {message}" — `templates/partials/settings/plugins.html:115-119`
- [ ] Page copy: "Directories serf scans for plugins at launch. Applied to every spawn." —
      `templates/partials/settings/plugins.html:3-5`

## 14. Skills (directories)

File: `templates/partials/settings/skills.html`. Structurally byte-for-byte identical to §13 except
the wire field (`skillsDirs` vs `pluginDirs`) and copy ("Skill directories" / "Directories serf
scans for skills at launch."). Same appwire calls (`getLayer`/`setLayer`/`validatePath(...,"dir")`).

- [ ] All behaviors of §13 apply verbatim, with `current.skillsDirs` as the wire field —
      `templates/partials/settings/skills.html:14-15,92,108`
- [ ] This file and `plugins.html` are maintained as parallel copy-pasted files, not a shared
      component — the rewrite should collapse them into one parameterized `DirListSetting` widget
      (simplification opportunity, not a required behavior to preserve as-is) —
      `templates/partials/settings/skills.html` vs `settings/plugins.html` (whole-file diff)

## 15. MCP servers

File: `templates/partials/settings/mcp.html`. Appwire: the "Discovered servers" block is 100%
server-rendered (no client RPC); the editable lists use
`getLayer("/","global")`/`setLayer("/","global",…)`→`serf/launch/{getLayer,setLayer}` and
`validatePath`→`serf/path/validate`.

- [ ] "Discovered servers" section is server-rendered at partial-load time from `.Mcps` (name,
      transport, `status-{{.Status}}` badge), per-row `.Error` when present, top-level `.McpsError`
      replacing the whole list on a probe failure, or "No MCP servers configured." when empty —
      reflects **live reachability as probed by the hub at page-render time**; does not refresh
      again without a full reload/re-swap of this partial (no client polling) —
      `templates/partials/settings/mcp.html:7-29`
- [ ] Below that, a second, independently-loaded, purely client-rendered editor (`#mcps-form`)
      fetches the current global layer once and renders two separate collections from it: "MCP
      config files" (`mcpConfigs`, string paths) and "Inline MCP servers" (`mcps`,
      `{name,command,args}`) — `templates/partials/settings/mcp.html:31,72-76`
- [ ] Config-file rows display the raw path; remove (×) has no confirm; add validates via
      `validatePath(v,"file")`, inline error on failure, else pushes `valid.path||v` and saves —
      `templates/partials/settings/mcp.html:80-103,182-197`
- [ ] Inline-server rows display `"{name} → {command} {args...}"`; add form takes 3 inputs
      (name/command/args, args parsed via `split(/\s+/).filter(Boolean)`); command is validated via
      `validatePath(command,"command")` (not `"file"`/`"dir"`) before the row is accepted —
      `templates/partials/settings/mcp.html:126-148,199-215`
- [ ] Both add-forms independently call `setLayer("/","global",{...current, mcpConfigs})` /
      `{...current, mcps}` — each save round-trips the **entire** global layer object with only its
      own array field mutated, no targeted patch endpoint —
      `templates/partials/settings/mcp.html:97,212`
- [ ] Only the config-file input gets `data-settings-dir-input` (inline autocomplete, no Browse
      button); the inline-server name/command/args inputs get no picker assistance —
      `templates/partials/settings/mcp.html:113,152-168`
- [ ] Both lists rebuild via a shared local `render()` closure that clears and repopulates `root`
      from scratch on every mutation — `templates/partials/settings/mcp.html:77-217`
- [ ] Top-level `getLayer` failure replaces the whole `#mcps-form` root with one error paragraph —
      `templates/partials/settings/mcp.html:219-224`
- [ ] Page copy: "Stored in the global launch layer" — this hand-rolled editor is **not**
      layer-aware for project overrides (unlike the `mcpServerList`/`pathList` schema kinds
      Appendix B's engine can render, which is layer-parameterized) — `mcp.html` always writes the
      global layer only — `templates/partials/settings/mcp.html:3-5`

## 16. Hub

File: `templates/partials/settings/hub.html`. Appwire: none — read-only.

- [ ] 3 read-only fields: Listen address, Run dir, Spawn timeout — each with help text naming the
      `hub.toml` key or `--addr` flag that controls it —
      `templates/partials/settings/hub.html:4-18`
- [ ] No client JS, no edit affordances — `templates/partials/settings/hub.html:1-21`
- [ ] Field set is a strict subset of General's (§2) fields with matching copy — confirm the
      rewrite intentionally dedupes rather than drops one —
      `templates/partials/settings/hub.html` vs `settings/general.html`

## 17. Storage

File: `templates/partials/settings/storage.html`. Appwire: none — read-only.

- [ ] 4 read-only fields: State dir, Run dir, Hub config (static literal path, not a template var),
      Past index (path + optional size + a **live** session count `.PastCount`) —
      `templates/partials/settings/storage.html:5-24`
- [ ] `.PastCount` pluralization ("session"/"sessions") is a Go template conditional
      (`{{if ne .PastCount 1}}s{{end}}`), the only non-trivial logic on this page — not JS —
      `templates/partials/settings/storage.html:23`
- [ ] No client JS — `templates/partials/settings/storage.html:1-27`

## 18. Per-project settings (`?cwd=`)

File: `templates/partials/settings/project.html`. Engine: `LaunchConfigControls` (Appendix B).

**Appwire:** `schema()`→`serf/launch/schema`, `getLayer(cwd,"project")`→`serf/launch/getLayer`,
`getLayer(cwd,"global")`→`serf/launch/getLayer` (second call, different `layer` arg),
`setLayer(cwd,"project",…)`→`serf/launch/setLayer`.

- [ ] This is a **standalone top-level template** (`{{define "project_settings"}}`), not
      `{{define "settings-content"}}` — it renders its own `<header class="workspace-header">` and
      has **no settings-nav sidebar** at all; reached only via a project's ⚙ gear icon in the
      sidebar or a direct `/settings/project?cwd=` link, never through the Settings nav list —
      `templates/partials/settings/project.html:1-8` vs `templates/partials/settings.html`'s
      `settings-content` pages
- [ ] When `.ProjectCWD` is empty, shows a project picker instead of the form: `<ul>` of
      `.AvailableProjects` (name + cwd), each linking to `/settings/project?cwd={cwd}` via htmx
      targeting **`#workspace`** (the whole workspace pane, not `#settings-content`) — consistent
      with this being a standalone page — `templates/partials/settings/project.html:91-105`
- [ ] Zero-projects empty state: "No known projects yet. Spawn a session to register a project." —
      `templates/partials/settings/project.html:107`
- [ ] With `.ProjectCWD` present: root div carries `data-cwd`, `hx-disable hx-disinherit="*"` —
      explicitly opts this subtree **out** of htmx processing; its fetches are hand-rolled appwire
      calls — `templates/partials/settings/project.html:10`
- [ ] Load fetches `schema()`, `getLayer(cwd,"project")`, and `getLayer(cwd,"global")` — the global
      layer is fetched **only** to compute "default: {value}" inline hints, never written —
      `templates/partials/settings/project.html:48-51`
- [ ] Renders via `LaunchConfigControls.render(form, {mode:"settings", layer:"project", options,
      current, includeEnvFallbacks:false, globalDefaults: globalLayer})` —
      `templates/partials/settings/project.html:53-60`
- [ ] `root.dataset.loaded` is `"false"` initially, `"true"` on success, or the string `"error"` on
      failure — a **3-state** contract, distinct from §9 Serf launch's 2-state
      (`"false"`→`"true"`-only) `form.dataset.loaded` —
      `templates/partials/settings/project.html:10,62,82` vs `settings/launch-serf.html:7,72`
- [ ] Submit validates via `LaunchConfigControls.validate(form)` (return early if invalid), then
      `setLayer(cwd,"project",collect(form))` — unlike §9, **no diagnostics are fetched or rendered
      anywhere on this page**, neither on load nor after save (no `renderDiagnostics`, no
      `#launch-diagnostics` container in this file at all) —
      `templates/partials/settings/project.html:64-76`
- [ ] Status-line self-clear-after-5000ms / persist-on-"Error:" logic (`setStatus`) is duplicated
      verbatim from §9 — `templates/partials/settings/project.html:34-46`
- [ ] Success toast "Project launch settings saved"; failure toast "Save failed" plus
      `LaunchConfigControls.showBackendError(form, err)` —
      `templates/partials/settings/project.html:70,73-74`
- [ ] Section copy: "Layered on top of the global Serf and Codex launch settings. Only fields set
      here override the global defaults." — `templates/partials/settings/project.html:14`
- [ ] On initial-load failure, the status paragraph is written to twice (`.textContent =` then
      immediately `replaceChildren(document.createTextNode(...))` with the same string) — a
      redundant double-write, harmless but not worth carrying forward —
      `templates/partials/settings/project.html:79-81`

---

## Appendix A — Shared: `assets/settings-pickers.js`

Used by: MCP config-file input (inline only, §15), Skills/Plugins directory inputs (inline +
Browse button, §§13-14), Marketplaces "Local path" field (inline + Browse button, §12c), and every
`pathKind`/`modelPicker`/`modelList` field the shared `LaunchConfigControls` engine renders
(§9, §18 — Appendix B).

- [ ] Model list fetched from a plain REST endpoint `GET /api/models?diagnostics=1`
      (`credentials:"same-origin"`) — **not** an appwire RPC — cached forever in-memory after first
      success (`_modelsCache`), pre-fetched once at script-load time regardless of whether any
      picker is ever opened — `assets/settings-pickers.js:12-18,287`
- [ ] Fetch failure (network error or non-OK response) silently resolves to `{models:[],
      diagnostics:[], recent:[]}` — no error surfaced; the picker just opens with an empty,
      Recent-less list — `assets/settings-pickers.js:14-16`
- [ ] Models grouped by `m.provider`; a pinned "Recent" pseudo-provider is prepended (only if any
      recent models exist) ahead of the remaining providers, which are otherwise alphabetically
      sorted — `assets/settings-pickers.js:29-36`
- [ ] Clicking a `data-settings-model-picker` trigger a second time while a picker is open just
      removes it — the removal check is an **unscoped** `document.querySelector(".sp-picker")`, so
      at most one model picker can ever be open anywhere on the page at once —
      `assets/settings-pickers.js:21-23`
- [ ] Search box filters providers (any model in that provider matches name+display_name
      substring) and, within the active provider, its model rows; if the active provider has zero
      matches for a new query, auto-jumps to the first other provider that does —
      `assets/settings-pickers.js:148-161`
- [ ] Each model row shows badges (`tools`/`vision`/`reasoning`[+levels]/`web search`) plus a meta
      line combining formatted context window (`K`/`M`, 1 decimal, trailing `.0` stripped) and
      `$X.XX/M in` input cost when present — `assets/settings-pickers.js:67-77,126-131`
- [ ] Selecting a model writes `"{provider}/{model}"` to the hidden input, dispatches a bubbling
      `change`, updates the display span via `SerfSpawn.abbreviateModel` if present else the raw
      value, removes the picker — `assets/settings-pickers.js:133-143`
- [ ] Picker positions absolutely just below-left of its trigger; search input auto-focused on
      open — `assets/settings-pickers.js:166-171`
- [ ] Dismissal: click-outside (`composedPath()`-based, tolerant of the picker's internal DOM being
      replaced mid-interaction) or `Escape`; both handlers attach only after a `setTimeout(…,0)` so
      the opening click can't also close it — `assets/settings-pickers.js:180-200`
- [ ] Dir picker (explicit button): `data-settings-dir-picker` opens `SerfDirPicker.open(...)`
      anchored to itself, targeting the sibling `input[type=text]` in its `.sp-dir-wrap` —
      `assets/settings-pickers.js:266-278`
- [ ] Dir picker (inline autocomplete): any `input[data-settings-dir-input]` opens the same shared
      picker on its own `input` event once non-empty, and on `ArrowDown` if no `.chip-picker-dir`
      is already open — input is both anchor and target —
      `assets/settings-pickers.js:226-244`
- [ ] A one-shot `__spDirSuppressNextInput` flag prevents the picker reopening itself immediately
      after it programmatically writes the chosen value back (`writeDirInput` sets the flag +
      dispatches `input`+`change`; the input's own listener consumes-and-clears it) —
      `assets/settings-pickers.js:204-209,230-234`
- [ ] If `window.SerfDirPicker` isn't loaded/lacks `.open`, both entry points silently no-op — no
      error, no fallback UI — `assets/settings-pickers.js:213`
- [ ] Both init routines are idempotent per element (`__spInit`/`__spDirInit` guards), rerun on
      every `htmx:afterSwap` (scoped to the swap target) plus once on `DOMContentLoaded`/immediately
      if already ready — safe for every settings section to call `SettingsPickers.init(root)`
      redundantly after its own re-renders — `assets/settings-pickers.js:248-249,292-304`

## Appendix B — Shared: `LaunchConfigControls` engine (`assets/launchconfig.js`)

Used by §9 (Serf launch, layer=`"global"`, cwd=`"/"`) and §18 (Project settings, layer=`"project"`,
cwd=project cwd) only. **Not** used by MCP/Skills/Plugins/Marketplaces/Credentials, which each
hand-roll their own simpler collection editor directly against `launchconfig.getLayer`/`setLayer`
(or, for Marketplaces, against `pluginsAdmin`).

- [ ] `launchconfig` wraps 16 RPCs 1:1 through one `request(method, params)` →
      `SerfAppwire.request`: `schema`→`serf/launch/schema`, `resolve`→`serf/launch/resolve`,
      `getLayer`→`serf/launch/getLayer`, `setLayer`→`serf/launch/setLayer`,
      `trustRepo`→`serf/launch/trustRepo`, `validatePath`→`serf/path/validate` (via
      `SerfAppwire.validatePath` when present, else a raw `fetch("/api/path/validate?...")` GET
      fallback); `authList`→`serf/auth/list`, `authStatus`→`serf/auth/status`,
      `authApiKeySet`→`serf/auth/apiKey/set`, `authLoginStart`→`serf/auth/login/start`,
      `authLoginComplete`→`serf/auth/login/complete`, `authLogout`→`serf/auth/logout`,
      `authDeviceStart`→`serf/auth/device/start`, `authDevicePoll`→`serf/auth/device/poll`;
      `instanceList`→`serf/instance/list`, `instanceCreate`→`serf/instance/create`,
      `instanceEdit`→`serf/instance/edit`, `instanceRemove`→`serf/instance/remove`,
      `instanceSetDefault`→`serf/instance/setDefault` — `assets/launchconfig.js:8-39`
- [ ] `authList`/`authStatus` are defined but not called by any settings partial read for this
      checklist — confirm at rewrite time whether another surface (spawn dialog?) uses them, or
      they're dead — `assets/launchconfig.js:24-25`
- [ ] `render(root, options)` infers `mode` from `options.mode` or, failing that, whether
      `root.dataset.launchAdvancedRoot` is defined (spawn dialog) vs settings; infers `layer` from
      `options.layer` or `root.dataset.launchSettingsLayer` — `assets/launchconfig.js:774-782`
- [ ] Options are filtered to those supporting the current layer (`optionSupportsLayer`: layer must
      be in `opt.defaultableLayers`) in settings mode, or spawn (`optionSupportsSpawn`:
      `opt.perLaunch` true and `driverSupport.serf !== false`) in spawn mode —
      `assets/launchconfig.js:45-53,779-782`
- [ ] In spawn mode, `agent`/`model`/`reasoningEffort` are additionally excluded (handled
      elsewhere, out of scope) — not applicable to either settings page in this checklist, both are
      always `"settings"` mode, documented since it's the same shared function —
      `assets/launchconfig.js:55,787-788`
- [ ] The 4 prompt-dependent leaf wire fields (`systemPromptFile`/`systemPromptText`/
      `systemPromptAppendFile`/`systemPromptAppendText`) never render as their own row — folded into
      2 composite radio controls (`systemPromptMode`, `systemPromptAppendMode`) —
      `assets/launchconfig.js:70-75,789`
- [ ] Options render grouped by `opt.group` in schema list order (not re-sorted), a section-header
      row inserted every time the group value changes — `assets/launchconfig.js:801-816`
- [ ] Field kinds: `select`/`radio` get a leading empty "(default)"/"(use global default)" option;
      `boolean` renders as a 3-state `<select>` (""/"true"/"false") — the wire payload correctly
      omits the unset case (`collect()` only sets the key for "true"/"false") —
      `assets/launchconfig.js:324-417,982-985`
- [ ] `modelPicker` renders hidden input + `SettingsPickers` button + display span + a dedicated
      "✕" clear button (blanks the hidden input, dispatches `change`) —
      `assets/launchconfig.js:161-194,419-434`
- [ ] `pathList`/`modelList` render as a collection (ul + add-row); `modelList` reuses the
      model-picker control as its "add" input instead of a plain text box —
      `assets/launchconfig.js:480-531,756-764`
- [ ] `pathList` items are validated against `schemaPathKind(opt.pathKind)` at add-time (blocks the
      add on failure, shows a field-level error) — `assets/launchconfig.js:512-519`
- [ ] The `modelFallbacks` wire field only, and only in settings mode, additionally renders a "No
      model fallbacks" explicit-empty checkbox; checking it clears existing rows client-side — this
      is the **only** way `collect()` ever emits an explicit `[]` for a list field; every other list
      field either sends its populated array or omits the key when empty (can't distinguish
      "never set" from "cleared") — `assets/launchconfig.js:90-94,535-557,1012-1016`
- [ ] `envMap` renders paired name/value inputs building `"NAME=value"` rows; no validation on
      either sub-field (any string accepted, including empty value) —
      `assets/launchconfig.js:589-664`
- [ ] `mcpServerList` renders name/command/args inputs; command is validated as `"command"` kind on
      **change** (blur) *and* again defensively before the Add button accepts it —
      `assets/launchconfig.js:666-737,908-927`
- [ ] Every collection kind (`pathList`/`modelList`/`envMap`/`mcpServerList`) renders as 2 sibling
      `.row` elements (a `.row.section-header` label/help row, then a `.row` holding the collection)
      appended directly to the table root — explicitly not nested inside one `<div class="row">`,
      to preserve a CSS adjacency selector (`.row.section-header + .row`) —
      `assets/launchconfig.js:560-587,637-663,710-736`
- [ ] `populate(root, current)` walks every `[data-launch-wire-field]` and sets value/checked from
      `current`; for `modelPicker` fields with a truthy value, also rewrites the display span via
      `SerfSpawn.abbreviateModel` if present — `assets/launchconfig.js:821-845`
- [ ] `populate()` specifically detects the modelFallbacks explicit-empty case: if `current` has
      the key, it's an array, and it's empty, checks the explicit-empty toggle (avoids an ambiguous
      empty list with an unchecked toggle) — `assets/launchconfig.js:874-881`
- [ ] `validate(root)` runs, in order, stopping at first failure (scrolls the offender into view):
      (1) every path-kind scalar input not currently inactive due to prompt-mode, (2) every
      path-kind `pathList`'s rows (re-validating and rewriting each row's stored value + displayed
      text to the server-canonicalized path), (3) every MCP command input —
      `assets/launchconfig.js:929-961`
- [ ] `inactivePromptDependent(root, wire)`: a field is skipped by both `validate()` and `collect()`
      when its owning composite radio isn't set to the mode that activates it (e.g.
      `systemPromptText` skipped unless `systemPromptMode==="inline"`) — a user can type anything
      into the inactive box and it's silently never validated or sent —
      `assets/launchconfig.js:244-250,933,981`
- [ ] `collect(root)` builds the payload from **live DOM state only** (never a cached copy of
      `current`): skips unchecked radios, skips inactive prompt-dependents, coerces `integer` via
      `Number()`, skips fields flagged `data-launch-invalid="true"`, omits empty-after-trim scalar
      values entirely (not sent as `""`) — `assets/launchconfig.js:974-991`
- [ ] `showBackendError(root, err)` only ever attaches an inline error for one message shape —
      case-insensitive match of both `/\benv key\b/` and `/credential/` in the same message,
      routed onto the `env` envMap field; every other backend error string is **not** shown inline
      by this engine — callers rely on their own status-line/toast —
      `assets/launchconfig.js:963-972`
- [ ] `validatePathInput`/`validateMCPCommandInput` use the native Constraint Validation API
      (`setCustomValidity` + `reportValidity()`) in addition to the custom
      `data-launch-validation-error` div, so the browser-native tooltip and the custom error text
      appear together — `assets/launchconfig.js:887-927`

---

## Open questions for the M7 implementer (not behaviors to replicate, but decisions to make explicitly)

- [ ] The Notifications-defaults discrepancy (§6, item 1) — replicate the buggy all-OFF default, or
      fix it to match the stated "title/favicon default on" copy? Either is a legitimate parity
      decision; silence is not.
- [ ] `mcp.html`'s hand-rolled editor (§15) writes only the global launch layer and has no
      project-layer equivalent, while the schema-driven engine (Appendix B) *does* support a
      `mcpServerList` kind per-layer — decide whether the rewrite keeps MCP servers global-only or
      folds them into the generic per-layer schema form.
- [ ] `skills.html`/`plugins.html` (§§13-14) are copy-paste twins — collapse to one parameterized
      widget in the rewrite (`DirListSetting(wireField, label, copy)`), not two components.
- [ ] Confirm with the Go-side RPC handlers (out of scope for this read) the exact response shapes
      for `serf/instance/list`, `serf/marketplace/{list,browse}`, and `serf/plugin/list` before
      generating `types.gen.ts` — this checklist only documents the fields the *current* client
      happens to read, which is a lower bound on the real response shape, not the full contract.
