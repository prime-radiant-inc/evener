# M6 behavior-parity checklist — spawn, palette/search, notifications, theme

- Source: `cmd/serf-hub` in this worktree, branch `worktree-webui-workspace-shell`, commit `3ad5a682cf0a2a2a03a6bbf6e1d088ba238f2a8b` (2026-07-20).
- Purpose: enumerate every observable behavior of the legacy hand-rolled JS surfaces so a rewrite (workspace-shell) can be checked off item-by-item instead of eyeballed.
- Every item cites `path:line` (or a line range) in the CURRENT implementation. Check a box only once you've confirmed the replacement does the same thing (or you've gotten explicit sign-off that the behavior is intentionally dropped/changed).

## Files read

Primary (as requested):
- `cmd/serf-hub/assets/spawn.js` (1949 lines, read in full)
- `cmd/serf-hub/templates/partials/spawn.html` (126 lines, read in full)
- `cmd/serf-hub/assets/search.js` (1094 lines, read in full)
- `cmd/serf-hub/assets/notifications.js` (436 lines, read in full)
- `cmd/serf-hub/assets/theme.js` (12 lines, read in full)
- `cmd/serf-hub/assets/settings-appearance.js` (87 lines, read in full)
- `cmd/serf-hub/assets/settings-display.js` (89 lines, read in full)

Pulled in because they're load-bearing for the requested behaviors (not optional reading — the requested files call straight into them):
- `cmd/serf-hub/assets/dir-picker.js` (347 lines, read in full) — the working-dir chip in spawn.js is a thin wrapper around this; "recent-projects" behavior lives here, not in spawn.js.
- `cmd/serf-hub/assets/settings-notifications.js` (110 lines, read in full) — the "settings coupling" half of notifications.js's contract.
- Two 6-line inline `<script>` blocks in `cmd/serf-hub/templates/app.html:17-24` and `cmd/serf-hub/templates/thread.html:9-15` — the pre-paint FOUC-avoidance logic that `theme.js` itself does not contain.
- `cmd/serf-hub/assets/style.css` (grepped, not read in full) — to confirm which theme-selector mechanism the CSS actually binds to (§4.2).
- `cmd/serf-hub/templates/partials/settings/theme.html`, `.../settings/notifications.html`, `.../settings/display.html` (grepped for `name=`/`value=` pairs, not read in full) — to pin down the exact radio/checkbox value vocabularies the JS in scope reads and writes.

Not read (out of scope, referenced only where a call crosses into them): `composer-attachments.js`, `renderer.js`, `appwire.js`, `settings-pickers.js`, `launchconfig`/`LaunchConfigControls` client.

## Cross-cutting hazards found during this audit

These are the findings most likely to bite a rewrite that "looks equivalent." Each also appears as a checkbox below in its proper section.

1. **The palette's `/theme` command is a visual no-op.** It sets `body.light-theme`/`body.dark-theme` classes (search.js:551-552), but the CSS has zero rules bound to those classes — only `:root[data-theme]` is styled (style.css:1-2,178,204,229-237; verified 0 hits for `.light-theme`/`.dark-theme` selectors repo-wide). The settings-page radio (settings-appearance.js) and the FOUC script both use the `data-theme` attribute correctly. See §4.4.
2. **No BroadcastChannel exists anywhere in this codebase.** Single-tab notification leader election is Web-Locks-only (notifications.js:235-246); a repo-wide grep for `BroadcastChannel` returns nothing. See §3.7.
3. **Three independent "click outside to dismiss" implementations** with different event types and semantics: `attachPickerDismiss` (spawn.js:1703-1722, pointerdown-based, shared by 5 of 6 chip pickers), `openTextPicker`'s inline dismiss (spawn.js:1426-1434, click-based, branch chip only), and `dismissOnOutsideClick` (dir-picker.js:71-88, click-based with an anchor-exclusion list, working-dir chip only). See §1.2.
4. **The working-dir chip doesn't toggle closed on re-click** the way every other chip picker does — it closes and immediately rebuilds a fresh picker, discarding unaccepted typed text. See §1.2.
5. **"none" vs "(default)" reasoning-effort semantics differ between the two surfaces that expose it**: the spawn advanced picker treats them as distinct wire values (spawn.js:1815-1820), the live in-session palette command normalizes "none" away entirely so only default+explicit-levels are offered (search.js ~408-411). See §1.5 and §2.5.

---

## 1. Spawn (new-session composer)

### 1.1 Server-side layout & prefill (`templates/partials/spawn.html`)

- [ ] Chip bar renders exactly 6 chips in order: harness, model, reasoning_effort, working_dir, branch, access_mode (`spawn.html:11-39`)
- [ ] Each desktop chip has a mirrored mobile row button carrying the same `data-chip` key, in the same order (`spawn.html:47-76`)
- [ ] Prompt `<textarea name="prompt">` has `autofocus` and is prefilled from `.DefaultPrompt` (`spawn.html:44`)
- [ ] Static subtitle "Leave blank to start a dormant session." always renders under the heading (`spawn.html:42`)
- [ ] Wire-value hidden inputs exist for harness/model/working_dir/branch/access_mode/agent(=`default`)/reasoning_effort(=`""`) (`spawn.html:87-94`)
- [ ] Harness options are serialized as one hidden `<input data-harness-option value=".ID" data-label=".Label">` per configured harness (`spawn.html:88`)
- [ ] `SafeEnv` fallback values are serialized as hidden `[data-launch-env-fallback data-env-name=... value=...]` elements, one per env var (`spawn.html:103`)
- [ ] "recent prompts" section renders only when `.RecentPrompts` is non-empty; each row is an `<a data-recent-prompt="...">` carrying the literal prompt text (`spawn.html:117-124`)
- [ ] Attach button label reads "attach ⌘V to paste" next to a hidden multi-file `accept="image/*"` file input (`spawn.html:79-80`)
- [ ] Advanced-options section starts collapsed (`aria-expanded="false"`) and links out to `/settings/launch` for persistent defaults (`spawn.html:96-101`)

### 1.2 Chip-picker shared chrome (`spawn.js`, `dir-picker.js`)

- [ ] `openPicker` dispatches on `chip.dataset.chip` to exactly one of 6 picker openers; unrecognized kinds no-op (`spawn.js:1349-1360`)
- [ ] `setChipValue`/`setModelValue` write to every element matching `[data-chip-value-<name>]` in a single pass — this is the entire mechanism keeping the desktop chip bar and the mobile row list in sync, since both markups share the same data attribute (`spawn.js:341-343,345-350,360-362`; dual markup at `spawn.html:13` and `spawn.html:49`)
- [ ] Harness / access-mode / model / effort pickers all check `document.querySelector(".chip-picker")` FIRST and, if one exists, just remove it and return — i.e. re-clicking ANY chip while a picker is open closes it (toggle-off), it does not swap to a different picker in the same gesture (`spawn.js:1362-1364, 1438-1439, 1466-1467, 1772-1773`)
- [ ] Branch/worktree text picker has the same toggle-off guard (`spawn.js:1387-1389`)
- [ ] **Divergence**: the working-dir picker has NO such guard — `openDirPicker(chip)` unconditionally calls `SerfDirPicker.open(...)`, whose `removeExisting()` closes any picker already open (including a stale dir picker of its own) and then unconditionally builds a brand-new one seeded from the chip's current display text — so a second click on the working-dir chip while its own picker is open discards whatever the user had typed but not yet accepted, rather than simply toggling closed (`spawn.js:1907-1924`; `dir-picker.js:57-69, 130-148, 175-187`)
- [ ] Desktop picker position: absolutely anchored just below its chip, left-clamped so a fixed ~520/480px panel never overflows the right edge of the viewport (`spawn.js:485-491`; same logic duplicated at `dir-picker.js:21-27`)
- [ ] At `max-width: 767px`, every picker instead becomes a full-width bottom sheet: re-parented onto `<body>`, inline position/top/left cleared, `z-index:901`, with a dimming scrim inserted behind it that self-removes via a `MutationObserver` once the picker leaves the DOM (`spawn.js:469-483, 497-506`; identical duplicate in `dir-picker.js:5-20, 32-41`)
- [ ] Shared pointerdown+Escape dismiss (`attachPickerDismiss`) is used by the harness, access-mode, model, effort, and harness-default-model pickers; it listens on `pointerdown` (not `click`) specifically so an in-picker tap that re-renders its own target element is never mistaken for an outside click (`spawn.js:1697-1722`, code comment at 1697-1702)
- [ ] Branch/worktree text picker instead uses its own bespoke `click`+`setTimeout(0)` outside-dismiss, with Enter/Escape handled separately inline on the input's own `keydown` (`spawn.js:1409-1417, 1426-1434`)
- [ ] Working-dir picker uses a THIRD independent dismiss implementation (`dismissOnOutsideClick`) that also accepts an `insideEls` exclusion list so a click back on the anchor chip is never treated as "outside" (`dir-picker.js:71-88`, wired at `dir-picker.js:338`)

### 1.3 Harness chip

- [ ] Picker lists every `[data-harness-option]` hidden input as a row; if none are present at all, falls back to a single synthetic `serf` row (`spawn.js:1442-1446`)
- [ ] Picking a harness both sets the chip value AND immediately re-applies the harness/model policy in the same click handler (`spawn.js:1451-1455`)

### 1.4 Model chip

- [ ] Fetches `{models, diagnostics, recent}` via `listModelsWithDiagnosticsForHarness`, which always goes through REST `GET /api/models` — deliberately NOT the appwire `listModelsWithDiagnostics` RPC, because only the REST response carries `display_name` and capability badges (`spawn.js:1470, 1866-1881`, code comment at 1854-1865)
- [ ] For a non-serf-model harness with zero models AND zero diagnostics, falls back to the single-row harness-default picker instead of showing an empty list (`spawn.js:1475-1478, 1889-1905`)
- [ ] Models are grouped by provider (alphabetically sorted); a "Recent" group is prepended above the provider groups whenever there are recent models matching the current filter (`spawn.js:1481-1486, 1628-1653`)
- [ ] Free-text search box filters provider + model + display_name (case-insensitive substring) on every keystroke (`spawn.js:1492-1494, 1554-1561, 1662`)
- [ ] Search-box Enter selects the first currently-visible model row (prevents the enclosing `<form>`'s implicit submit); Escape dismisses the picker (`spawn.js:1664-1678`, code comment at 1664-1668)
- [ ] Configured providers whose listing failed render as inline diagnostic rows (warning icon + "`<provider>` unavailable: `<message>`" + optional title-attribute hint) instead of silently vanishing (`spawn.js:1504-1520`)
- [ ] Each model row shows: display name; model id as a second line ONLY if it differs from the display name; capability badges (tools / vision / "reasoning (`levels`)" / web search); and a metadata line (context window, max output, $/M input, $/M output) built from whichever fields are present (`spawn.js:1522-1607`)
- [ ] The currently-selected model's row gets `.is-current`, `aria-current="true"`, and a trailing check icon (`SerfIcons.ended` or a literal `✓` HTML entity fallback) (`spawn.js:1540-1552, 1567-1571, 1608-1614`)
- [ ] Selecting a model: for a serf-model harness, stores `"<provider>/<model>"`; for any other harness, stores the bare model id with a synthesized `provider/model`-shaped display label via `modelOptionLabel` (`spawn.js:1619-1626, 1883-1887`)
- [ ] Empty-state copy differs by whether a filter is active: "No models match." vs "No models available." (`spawn.js:1654-1659`)
- [ ] On an outright fetch rejection, falls back to the harness-default single-row picker, but ONLY for non-serf-model harnesses (`spawn.js:1690-1694`)

### 1.5 Reasoning-effort chip

- [ ] For a non-serf-model harness, shows a single inert note row — "(reasoning effort applies to the serf harness only)" — and offers no levels at all (`spawn.js:1775-1790`)
- [ ] For the serf harness, fetches enriched `/api/models` via REST specifically because only REST carries `reasoning_effort_levels` (the appwire model list doesn't) (`spawn.js:1791-1796`, code comment at 1755-1756)
- [ ] A model explicitly reporting `supports_reasoning === false` is a KNOWN-empty ladder — renders "(this model does not support reasoning effort)" — distinct from a model simply missing level metadata (`spawn.js:1742-1743, 1800-1814`, code comment at 1729-1735)
- [ ] Otherwise offers `(default)` + each level + `none` as separate, distinct options — launch-time context treats "inherit the configured default" and "explicitly clear to nothing" as two different values (`spawn.js:1815-1820`, code comment at 1815-1817)
- [ ] Missing per-model level data (model known, but no `reasoning_effort_levels`/`reasoningEffortLevels` field) falls back to `DEFAULT_EFFORT_LEVELS = ["minimal","low","medium","high"]` (`spawn.js:1727, 1736-1753`)

### 1.6 Working-directory chip + `dir-picker.js`

- [ ] Entirely delegates to `window.SerfDirPicker.open(...)`; a no-op (button does nothing) if that global isn't loaded (`spawn.js:1907-1908`)
- [ ] Seeds the picker's starting value from the chip's own current display text, falling back to `localStorage["serf-hub.spawn-defaults.global.last-working-dir"]` when the chip still shows the placeholder (`spawn.js:1910-1913`)
- [ ] Accepting a directory both updates the chip AND persists it as the new global "last working dir" (`spawn.js:1919-1922`)
- [ ] "Recent projects" (server-capped at 15 per the code comment, via `SerfAppwire.recentProjects()`) prepopulate the list ONLY on the picker's very first listing; any subsequent typing or "browse into a folder" action drops the recent section for the rest of that picker's lifetime (`dir-picker.js:47-55, 170-173, 230-252, 274-284`)
- [ ] Recent-project rows show the basename as the primary label and the full path as a secondary line (basenames collide across projects, so the full path disambiguates) (`dir-picker.js:227-251`)
- [ ] Recent-project rows ACCEPT immediately on click; ordinary listed directory rows instead BROWSE INTO the folder on click (`dir-picker.js:249` vs `dir-picker.js:270`)
- [ ] Typing in the input debounces 150ms before firing a new completion request (`dir-picker.js:303-307`)
- [ ] Enter commits whatever literal path is typed (even if it matches no listed row); Escape closes the picker (`dir-picker.js:311-319`)
- [ ] A `..` parent-directory row is appended whenever the current directory isn't `/` (`dir-picker.js:214-225`)
- [ ] A checkmark "use current directory" button next to the input accepts the raw input value (or the tracked `currentDir` if the input is empty), independent of whatever the results list currently shows (`dir-picker.js:155-161, 301`)
- [ ] The "No directories here" empty state is suppressed when a recent-projects section is also being shown, so the two don't stack redundantly (`dir-picker.js:288-296`)
- [ ] Stale/out-of-order completion responses are dropped via a monotonically incrementing `requestID` compared at resolution time (`dir-picker.js:274-275, 281`)
- [ ] Older hub clients without the `recentProjects` RPC degrade silently to "no recent section" rather than erroring (`dir-picker.js:50-55`)

### 1.7 Branch/worktree chip

- [ ] Free-text picker is seeded from the chip's current display text, blanked out if that text is literally the placeholder string `"(default)"` (`spawn.js:1391-1392`)
- [ ] Setting `working_dir` (whether via chip pick or sticky-default replay) triggers `resolveAndSetHeadBranch`, which fetches `GET /api/git/head?cwd=` and fills the branch chip's DISPLAY text (not its hidden wire value) with the resolved HEAD ref, stashing the raw value in `display.dataset.resolvedHead` so "(default)" can later be explained (`spawn.js:365-367, 372-393`)
- [ ] Branch auto-resolution is skipped once an explicit branch value exists — checked once before firing the fetch, and checked AGAIN after the fetch resolves, so a value the user picked while the request was in flight can't be clobbered by a late response (`spawn.js:375, 383-384`)
- [ ] Branch auto-resolution is REST-only: when `window.SerfAppwire` is present the function no-ops entirely, because appwire has no `git/head` RPC yet (`spawn.js:376-379`)

### 1.8 Access-mode chip

- [ ] Exactly four fixed rows, in this order: `full`, `read-only`, `workspace-write`, `restricted` — sourced from the static `ACCESS_MODE_OPTIONS` array, not fetched (`spawn.js:4-9, 1369-1378`)
- [ ] Each access mode maps 1:1 to a `sandbox` value (`off`/`read-only`/`workspace-write`/`restricted`); at submit time this is merged into `launch_overrides.sandbox`, but ONLY if the advanced-options schema hasn't already set `sandbox` explicitly (`spawn.js:4-9, 60-75, 1271`)

### 1.9 Sticky defaults / prefill layering (localStorage)

- [ ] Per-project sticky-defaults key is `serf-hub.spawn-defaults.<workingDir|"global">`, a JSON blob (`spawn.js:51-53`)
- [ ] On load, `harness`/`branch`/`access_mode` defaults are applied BEFORE `working_dir`, specifically so the HEAD-branch auto-resolution that `working_dir` triggers can already see whether an explicit branch default won (`spawn.js:1132-1138`, code comment at 1133-1134)
- [ ] Model sticky-default is applied only when the current harness uses serf models AND a stored default value exists; otherwise `applyHarnessModelPolicy` runs instead to decide whether the chip should be blanked (`spawn.js:1145-1149`)
- [ ] If the server didn't prefill `working_dir` (no `?dir=` etc.) and no stored default supplies one either, the GLOBAL `serf-hub.spawn-defaults.global.working_dir` key is consulted as a last resort (`spawn.js:84-88`)
- [ ] The global model default (`serf-hub.spawn-defaults.global.model`) layers UNDER a more specific per-project model default when both exist (`spawn.js:81-83`)
- [ ] If the server DID prefill `working_dir` (e.g. via `?dir=`) and no stored default overrides it, HEAD-branch resolution still runs for that server-supplied value (`spawn.js:1141-1144`)
- [ ] Saving defaults on submit drops the `model` field entirely for non-serf-model harnesses, so a stale model id is never persisted for a harness that ignores it (`spawn.js:92-96`)
- [ ] Saving defaults writes the model GLOBALLY (cross-project) only when the harness uses serf models AND a model was actually chosen (`spawn.js:98`)
- [ ] Saving defaults writes `working_dir` GLOBALLY on every submit that has one, independent of harness (`spawn.js:100`)

### 1.10 Stale-model detection & cleanup

- [ ] A stored model value with no `/` separator (legacy bare model name) is dropped SYNCHRONOUSLY, before the model list even resolves, so the user can never submit it (`spawn.js:266-270`)
- [ ] `modelValidityAgainstList` classifies a value as `malformed` (no `/`) / `stale` (provider IS enumerated, model gone) / `unknown` (provider not enumerated at all — e.g. OAuth-only anthropic, openrouter-anthropic) / `valid` (`spawn.js:154-175`)
- [ ] Only `malformed` and `stale` verdicts clear the chip; `unknown` is deliberately left untouched because the hub has no way to prove it's actually wrong (`spawn.js:249-253, 292-294`)
- [ ] Before validating the CURRENT chip's value, `validatePrefilledModel` sweeps EVERY `serf-hub.spawn-defaults.*` per-project blob in localStorage (not just the active project) plus the standalone global-model key, stripping stale/malformed `.model` fields and deleting an emptied blob outright (`spawn.js:177-246, 254-256, 275-278`)
- [ ] After the sweep, the code re-reads the chip's LIVE value and bails out if it no longer matches what was being validated, so an async response can't clobber a newer in-flight user choice (`spawn.js:285-290`)
- [ ] A cleared stale/malformed model surfaces an inline, dismissible notice: `` Discarded last-used model `<value>` — no longer offered by this hub. `` (`spawn.js:130-152`)
- [ ] The notice is anchored just above the prompt textarea (falling back to just above `.spawn-actions` if the textarea is absent), and is auto-cleared the instant the user picks ANY new non-empty model (`spawn.js:148-151, 355-357`)
- [ ] A network/parse failure while fetching the model list leaves the prefill completely untouched — the server's own 503 path on submit is the fallback source of truth (`spawn.js:298-302`)
- [ ] Switching TO a non-serf harness always blanks the model chip; switching to a serf-model harness only blanks it if the current value doesn't already look like `provider/model` (`spawn.js:395-402`)

### 1.11 Advanced options (schema-driven)

- [ ] Advanced panel renders exactly once per root, guarded by a `root.__schemaRendered` marker (`spawn.js:960-963`)
- [ ] Options are filtered to schema entries with `perLaunch: true` whose `driverSupport.serf` isn't explicitly `false` (`spawn.js:618-626, 973`)
- [ ] Prefers delegating to `window.LaunchConfigControls.render` when present; otherwise falls back to a built-in fieldset-per-group renderer (`spawn.js:966-1006`)
- [ ] Supported control kinds and their shape: `select`, `radio` (first choice pre-checked), `boolean` (tri-state `(default)`/`true`/`false` select), `modelPicker` (hidden+display+"pick" button trio), plain/multiline `text`, `integer` (min `0`, step `1`), `pathList`, `modelList`, `envMap` (name/value pairs), `mcpServerList` (name/command/args triples) (`spawn.js:628-649, 690-956`)
- [ ] Path-kind scalar/list inputs get live validation via `window.launchconfig.validatePath` on `change`/on "add", surfaced through native `setCustomValidity` + `reportValidity` (`spawn.js:639-643, 809-822, 1009-1026`)
- [ ] MCP server command inputs are validated as path-kind `"command"` both on `change` and again immediately before "add" commits the row (`spawn.js:903-907, 914-917, 1039-1056`)
- [ ] An env-fallback hint (`env <NAME>: <value>`) renders next to a control only when: the option declares a non-secret `envFallback`, AND the current safe-env snapshot actually has a value for that name (`spawn.js:651-661`)
- [ ] "show resolved config" button re-runs path-scalar AND MCP-command validation first (aborting on failure), then calls `launchconfig.resolve(cwd, overrides)` and dumps pretty-printed JSON into `#ovr-resolved-out` (`spawn.js:1069-1075, 1174-1183`)
- [ ] Toggling the advanced `<details>`-like section scrolls it into view (`block:"nearest", behavior:"smooth"`) only on the OPEN transition, and mirrors `aria-expanded` on the toggle button either way (`spawn.js:1160-1171`)
- [ ] `collectAdvancedOverrides` skips unchecked radios, drops boolean fields left at `(default)`, drops any field flagged `data-launch-invalid="true"`, and only includes list/env/mcp collections when non-empty (`spawn.js:1077-1120`)
- [ ] Advanced-schema `model`/`agent`/`reasoningEffort` overrides take precedence over the plain chip form values at submit time — note the wire field is camelCase `reasoningEffort` even though the form field is snake_case `reasoning_effort` (`spawn.js:1122-1125, 1277, 1281-1282`)

### 1.12 Prompt textarea & attachments

- [ ] ⌘/Ctrl+Enter inside the prompt textarea submits the form (`preventDefault` + `form.requestSubmit()`) (`spawn.js:1204-1211`)
- [ ] Textarea auto-expands to `scrollHeight` on every `input` event, but ONLY under the `max-width: 767px` media-query gate; desktop relies on CSS `max-height` for growth instead (`spawn.js:40-49, 1213-1219`)
- [ ] Clicking a recent-prompt row fills the textarea with the EXACT stored text, focuses it, and auto-expands (mobile only) (`spawn.js:1193-1201`)
- [ ] Paste / drag-drop / file-picker attachment handling is wired through shared `window.SerfComposerAttachments` helpers keyed on a per-`<form>` `pendingState.items` array (`form.__composerPasteState`); if that global isn't loaded, none of the attachment wiring runs at all (`spawn.js:1221-1240`)
- [ ] Submit is blocked with an inline error — "Image attachment is still processing." — if ANY pending attachment is still flagged `.pending` (`spawn.js:1257-1260`)
- [x] ~~Submit is blocked with "Prompt is empty. Type something before spawning." only when BOTH the trimmed prompt text AND the attachment list are empty~~ — **INTENTIONALLY DROPPED (kata `ytpa`)**. The legacy guard (`spawn.js:1246-1265`) contradicted the form's own placeholder, which promises "Leave blank to start it dormant", and the daemon has always honoured that: `hubThreadStart` calls `StartTurn` only when `len(params.Input) > 0` (`cmd/serf-hub/app_threadlifecycle.go:183`). The rewrite lets a blank prompt through and starts a dormant session. The guard was originally added (commit `7743e7f`) to absorb an *accidental* empty submit — Enter bubbling out of the model-picker search into the form's implicit submit — whose root cause was fixed separately (kata `t13x`); the React pane has no `<form>` and no implicit submit at all, so the accident it guarded against can no longer happen. An attachment-only submission (no text) is still allowed through, unchanged.
- [ ] The prompt is trimmed only for that emptiness CHECK — the raw untrimmed text (newlines and leading whitespace intact) is what's actually sent in the payload (`spawn.js:1251, 1273-1275`)

### 1.13 Working-directory preflight

- [ ] Before spawning, `GET /api/path/validate?path=&kind=dir` is checked; a failure of the CHECK ITSELF fails OPEN (spawn proceeds anyway) so a flaky validator never blocks a real spawn (`spawn.js:573-580`, code comment at 568-572)
- [ ] Deterministic "not fixable by creating a directory" errors — literal strings `path is not a directory`, `absolute path required`, `path is required` — render an inline error and abort instead of offering to create (`spawn.js:582-588`)
- [ ] Any other invalid-path reason offers an IN-FORM (not native `confirm()`) dialog: `` The directory `<path>` doesn't exist yet. Create it and start the session? `` with Cancel / "Create & start" buttons; "Create & start" receives initial focus (`spawn.js:527-566, 589-591`)
- [ ] Declining aborts the submit with NO error shown (`spawn.js:559-561, 591`)
- [ ] Accepting POSTs `/api/dirs/create`; on a non-OK response the inline error uses the response body's `.error` field when present, else falls back to `HTTP <status>` (`spawn.js:592-606`)

### 1.14 Submission & result handling

- [ ] Submit handler always calls `e.preventDefault()` and rebuilds the entire payload from `FormData` — there is no native form POST path (`spawn.js:1243-1245`)
- [ ] Payload shape: `{launch_overrides, prompt, harness, model, working_dir, branch, access_mode, agent, reasoning_effort, attachments}` (`spawn.js:1270-1288`)
- [ ] Prefers `window.SerfAppwire.startThread(body)`; REST fallback POSTs `/api/spawn` with `attachments` re-encoded into `items: [{type:"image", mediaType, data(base64), name}]` and the raw `attachments` key stripped from the body first (`spawn.js:1305-1330`)
- [ ] `spawnEncodeAttachmentData` base64-encodes in `0x8000`-byte chunks (avoids `String.fromCharCode.apply` argument-count blowups on large images) and duck-types ArrayBuffer/typed-array/cross-realm buffers rather than using `instanceof`, specifically to survive JSDOM-originated buffers in tests (`spawn.js:11-38`, code comment at 11-15)
- [ ] REST failure path parses the response body as JSON looking for an `.error` field, falling back to the raw text for older plain-text error responses (`spawn.js:419-427, 1327`)
- [ ] Spawn button is disabled and relabeled `spawning…` for the duration of the request, restored to the literal HTML `spawn <kbd>⌘↵</kbd>` on failure (`spawn.js:1303-1304, 1339`)
- [ ] On success, the pending-attachment bag is cleared AND the paste marker-counter reset (`SerfComposerAttachments.resetMarkerCounter`) BEFORE navigating away, so a back-button return can't resend the same images (`spawn.js:1331-1336`)
- [ ] Success navigates via `window.location.href = "/s/" + encodeURIComponent(routeID)`; `routeID` strips a leading `local:` prefix from whichever of `ref` / `session_id` / `sessionId` is present, preferring a non-`local:` `ref` first (`spawn.js:404-417, 1337`)
- [ ] Failure renders the error prefixed with `spawn failed: ` UNLESS the underlying message already starts with that phrase (case-insensitive check) (`spawn.js:434-437, 1338-1340`)

### 1.15 Re-init on htmx swap

- [ ] `tryInit` is idempotent per `<form>` via a `form.__spawnInitialized` marker, so htmx swapping the spawn partial back in never double-binds listeners (`spawn.js:1928-1932`)
- [ ] Wired to run on `DOMContentLoaded` (or immediately if the document already finished loading) AND on every subsequent `htmx:afterSwap` on `document.body` (`spawn.js:1934-1942`)

---

## 2. Palette / Search (⌘K)

### 2.1 Open/close & global wiring (`search.js`)

- [ ] Global `⌘K` / `Ctrl+K` keydown opens the dialog from anywhere in the app (`search.js:27-28`)
- [ ] Any `[data-search-trigger]` click opens it (`e.preventDefault()`'d) (`search.js:39-44`)
- [ ] `Esc` while a command is selected (args mode) backs OUT to command-filter mode instead of closing the dialog; `Esc` in any other mode closes it (`search.js:29-37`)
- [ ] Opening first closes the mobile sidebar drawer via `window.SerfSidebar.close()` (`search.js:133-135`)
- [ ] Opening deactivates any active focus traps on `#tasks-panel`/`#details-panel` (so the dialog isn't rendered inert behind them), remembering exactly which ones were suspended (`search.js:139-149`)
- [ ] On the dialog's native `close` event, every suspended panel trap is reactivated — but only if that panel element is still connected to the DOM and doesn't already have a trap handle (`search.js:89-99`)
- [ ] `open()` resets state atomically: `selectedCommand=null`, pill hidden, input cleared with placeholder `search live + past sessions`, input focused, `items=[]`/`active=-1`, results cleared, AND any lingering `.palette-error` strip cleared (`search.js:150-161`)
- [ ] `openWith(initialQuery)` opens then seeds the input value and synthesizes a bubbling `input` event so the query renders immediately — this is the entry point `renderer.js:6914-6916` (out of scope) uses to open the palette pre-seeded with `/` when the composer textarea sees a leading `/` on an empty message (`search.js:164-169`)
- [ ] Clicking the `<dialog>` backdrop itself (`e.target === dialog`, i.e. NOT its content) closes it (`search.js:83-85`)

### 2.2 Mode model

- [ ] Mode is recomputed from state on every keystroke, in this priority: `command-args` if a command is selected, else `command-filter` if the input starts with `/`, else `search` (`search.js:123-127`)
- [ ] The shared input handler branches per mode: debounced `search()` / `renderCommands()` / `renderArgsEnum()` or `renderArgsFree()` (chosen by the selected command's `args.kind`) (`search.js:47-61`)

### 2.3 Search mode

- [ ] Queries are debounced 150ms before hitting the backend (`search.js:46, 903-917`)
- [ ] An empty (trimmed) query clears results locally without any backend call (`search.js:904-909`)
- [ ] Prefers `window.SerfAppwire.search(query)`; REST fallback is `GET /api/search?q=` (`search.js:911-913`)
- [ ] Results render up to three sections, in this fixed order: "Live", `"Past · N"`, `"In session · N"` (`search.js:919-946`)
- [ ] In-session matches are computed entirely CLIENT-SIDE by scanning `.user-message, .assistant-message, .system-line` under `#conversation` for a case-insensitive substring match, tracking a 1-based turn counter as it walks (`search.js:961-982`)
- [ ] In-session snippet building shows ~40 characters of context on each side of the match, with a leading/trailing `…` only when truncated, and `<mark>` around the exact matched substring (`search.js:984-992`)
- [ ] Live/past row titles and project names get `<mark>` around the first case-insensitive match of the query (`search.js:994-1003, 1005-1013`)
- [ ] Live rows show a pulsing status dot (`data-pulse`) only when state is `active`/`awaiting`/`errored` (`search.js:1007-1009`)
- [ ] Zero results across ALL THREE sections renders "No matches / Nothing in live, past, or this session." (`search.js:947-949`)
- [ ] A backend search failure renders a distinct "Search failed" empty state (`search.js:916`)
- [ ] `↑`/`↓` cycle `active` with WRAPAROUND over whatever `items` the current mode populated; `updateActive()` toggles `.active`/`aria-selected` on rows, scrolls the active row into view (`block: "nearest"`), and sets/clears `aria-activedescendant` on the input (`search.js:1030-1052`)
- [ ] Plain `Enter` dispatches to `enterPressed()` (mode-dependent); `⌘/Ctrl+Enter` opens the active result in a NEW TAB but only in search mode (no-op in the other two modes); `⇧Enter` jumps to the active in-session hit, also search-mode only (`search.js:62-81`)
- [ ] Opening a live/past result closes the dialog then navigates same-tab (`window.location.href`); opening an in-session hit instead scrolls the transcript element into view (`block:"center", behavior:"smooth"`) and flashes a `.search-hit` class for exactly 2000ms (`search.js:1053-1080`)
- [ ] The "new tab" path (`⌘Enter`) uses `window.open(url, "_blank")` for live/past results but still resolves in-session hits via the SAME `activateInSession` same-tab scroll behavior (there's no "new tab" concept for an in-page scroll target) (`search.js:1063-1072`)

### 2.4 Command-filter mode (input starts with `/`)

- [ ] The command registry (`commands()`) is rebuilt fresh on every call — 22 commands total (`search.js:326-518`)
- [ ] Scope gating: `global` commands always visible; `ended-ok` commands visible on any session page (live OR ended); bare `session` commands visible ONLY on a LIVE session page (`ctx.sessionState !== "ended"`) (`search.js:581-588`)
- [ ] `buildCtx()` derives `sessionId`/`sessionState` from `#conversation`'s `data-session-id`/`data-state` attributes, and `onPage` from the URL path (`home` / `session` / `settings` / `spawn` / `other`) (`search.js:568-579`)
- [ ] With an EMPTY filter, a "Recent" section (up to 5 remembered command ids, most-recent-first, from `localStorage["serf.search.recentCommands"]`) renders ABOVE "Commands", and those same ids are excluded from the main "Commands" list below to avoid duplication (`search.js:619-627, 641-661`)
- [ ] With a NON-empty filter, results are scored by `commandScore` — best of a substring match (worth `200 - matchIndex`) or a fuzzy subsequence match (rewards matches starting at a word boundary and consecutive-character streaks) across `id`+`title`+`keywords` — sorted descending by score with original registry order as a stable tiebreak; any command scoring negative is excluded entirely (`search.js:590-617, 645-649`)
- [ ] `rememberCommand` pushes an id to the FRONT of the recency list, de-duplicates, and caps at 5 entries (`search.js:629-633`)
- [ ] A successful ARGLESS command remembers itself in recents UNLESS it's flagged `stayOpen` — `stayOpen` commands (`/search`, `/help`) never record recency and never auto-close (`search.js:825-829, 862-865`)
- [ ] Each command row shows a `/` glyph, the title, the literal `/<id>`, and the hint text, in that visual order (`search.js:682-689`)

### 2.5 Individual commands and their actions

All from `search.js:326-517` unless noted:

- [ ] `/new` (global, argless) → `Nav.go("/new")` (330)
- [ ] `/spawn <text>` (global, free arg) → `Nav.go("/new?prompt=" + encodeURIComponent(text))` (331-333)
- [ ] `/settings` (global, argless) → `Nav.go("/settings")` (334-335)
- [ ] `/theme <dark|light>` (global, enum arg — static 2-item source, no "system" choice) → `applyTheme(item.id)`; **see §4.4 for why this doesn't visibly change the current page** (336-339, 550-554)
- [ ] `/dashboard` (global, argless) → `Nav.go("/")` (340-341)
- [ ] `/search` (global, argless, `stayOpen`) → clears the input, refocuses, re-fires the `input` event (342-344)
- [ ] `/help` (global, argless, `stayOpen`) → renders the keyboard-shortcuts panel (345-347)
- [ ] `/upgrade` (global, argless) → `upgradeSerf("")`; success toasts `"Serf upgraded to <channel>"` and, if `restartMessage` is present, also appends it as an info banner; failure toasts "Upgrade failed" (348-349, 290-318)
- [ ] `/compact` (session, argless) → `postSession(ctx, "compact")` (352-353)
- [ ] `/interrupt` (session, argless) → `postSession(ctx, "interrupt")`; blocked inline with "interrupt failed: no active turn" when there's no active turn id (354-355, 236-243)
- [ ] `/clear` (session, argless) → `postSession(ctx, "clear")` (356-357)
- [ ] `/aside` (session, argless) → forks the session's tip into a side thread (same permissions/config) via appwire `asideThread` or `POST /s/<id>/aside`; on success refreshes the sidebar (`sidebar:refresh` htmx trigger on `document.body`) and navigates to the new child session; throws if the response carries no child session id (358-362, 209-234)
- [ ] `/shutdown` (session, argless) → `postSession(ctx, "shutdown")`; success toasts "Session shut down", failure toasts "Shutdown failed" (363-376)
- [ ] `/model <enum>` (session) → source is `fetchModels()` (appwire `listModels`, mapped to `{id:"<provider>/<model>", label:display_name||model, hint:provider}`); blocked inline with "model change failed: turn in progress" while the thread is busy (per `SerfThreadState.isBusy`); success toasts `"Model: <id>"`, failure toasts "Model change failed" (377-400, 261-264, 272-288)
- [ ] `/reasoning-effort <enum>` (session) → source is `window.SerfModelSwitch.effortLevels()` — a SNAPSHOT-based source (reasoningEffortLevels/supportsReasoning from the live thread state), explicitly NOT `/api/models` — prefixed with a `(default)` option; an empty ladder (non-reasoning model) yields ZERO options rather than just `(default)`; "none" is deliberately omitted here because it would normalize to the same wire value as default, unlike the spawn-time picker (§1.5) where they're kept distinct; toasts `"Effort: <value|default>"` / "Effort change failed" (401-433, code comment at 403-411)
- [ ] `/steer <text>` (session, free arg) → blocked inline with "steer failed: no active turn" when idle; otherwise appwire `steer` or `POST /s/<id>/steer` (434-448)
- [ ] `/queue <text>` (session, free arg) → blocked inline with "queue failed: no active turn" when idle; otherwise appwire `queueTurn` or `POST /s/<id>/queue`; relies entirely on the daemon's `thread/queueChanged` broadcast to update the UI afterward — no local state mirroring (453-471, code comment at 468-470)
- [ ] `/goal <text>` (session, free arg, empty text CLEARS the goal) → `SerfAppwire.request("goal/set", {ref, objective: text.trim()})` (474-477)
- [ ] `/drain-as-steer` (session, argless) → blocked inline with "drain failed: no active turn" when idle; otherwise appwire `drainAsSteer` or `POST /s/<id>/drain-as-steer`; relies on the daemon's `thread/queueChanged` (depth=0) broadcast to clear the UI preview (480-496)
- [ ] `/fork` is intentionally OMITTED from the palette entirely — forking requires an edited message the palette has no UI to collect; the transcript row's own edit affordance is the only entry point (497-499, code comment)
- [ ] `/copy-id` (ended-ok, argless) → copies the session id via the async Clipboard API with an `execCommand("copy")` fallback for non-secure contexts; banners an error only if BOTH paths fail (502-510, 520-548)
- [ ] `/tasks` (ended-ok, argless) → synthesizes a click on `[data-tasks-trigger]` (511-512)
- [ ] `/status` (ended-ok, argless) → synthesizes a click on `[data-details-trigger]` (513-514)
- [ ] `/project` (ended-ok, argless) → finds the session's sidebar link, un-collapses its `[data-project-key]` ancestor section, scrolls it into view (`block:"center", behavior:"smooth"`) (515-516, 556-566)

### 2.6 Command-args mode

- [ ] Entering args mode remembers the pre-args filter text (defaulting to `"/"` if it wasn't already a `/`-prefixed string) so `Esc` can restore it later; shows a header pill with the command's title + a `×` back button; clears the input; sets the arg-kind-specific placeholder (`search.js:718-734, 746-750`)
- [ ] The pill's back button AND `Esc` both call `exitArgsMode()`, which clears `selectedCommand`, hides the pill, restores the remembered pre-args filter text, refocuses, and re-renders the command list for that restored text (`search.js:736-744, 118`)
- [ ] `enum`-kind args: `source(ctx)` may return a plain array OR a thenable; while pending shows "Loading…"; resolved-but-empty shows "No matches" ONLY after the load has completed at least once (an `argsEnumLoaded` gate prevents a premature "No matches" flash); a REJECTED promise shows a distinct "Couldn't load options / Close and reopen to retry" state (`search.js:757-805`)
- [ ] Enum filtering matches case-insensitively against EITHER `label` OR `id` (`search.js:763-766`)
- [ ] `free`-kind args show a static hint: "type a value and press ↵" when the input is empty, or `` press ↵ to run with: `<code>` `` echoing the live typed text once non-empty (`search.js:807-814`)
- [ ] Enter in enum mode requires an active selected row and no-ops otherwise; Enter in free mode always runs with whatever the current raw input value is, including an empty string (`search.js:184-190`)

### 2.7 Command execution / error surfacing

- [ ] Both `runArgless` and `runWithArg` swallow a SYNCHRONOUS throw from the command's own `run` function (empty `catch`) before handing the (possibly `undefined`) result to `handleCommandResult` (`search.js:825-837`)
- [ ] A `blocked(message)` sentinel object (`{paletteBlocked:true, message}`), whether returned directly or via a resolved Promise, keeps the palette OPEN and shows an inline `.palette-error` strip instead of closing (`search.js:198, 839-846, 883-893`)
- [ ] `blockedFromResponse(prefix, resp)` converts a non-ok `fetch` Response into a `blocked()` sentinel carrying `"<prefix>: <response-body-or-'HTTP <status>'>"` (`search.js:200-207`)
- [ ] A REJECTED command Promise's error message prefers `err.message`, then the stringified error, then falls back to the literal `"command failed"` (`search.js:867-871`)
- [ ] The `.palette-error` strip is inserted as the FIRST child of the results pane and persists across re-renders within the same interaction — it's only explicitly cleared by the next `open()` call (`search.js:883-893, 161`)
- [ ] Successful (non-blocked) command completion closes the dialog UNLESS the command is `stayOpen` (`/search`, `/help`) (`search.js:829, 836, 862-865`)

### 2.8 Help panel

- [ ] `/help` renders exactly 7 fixed shortcut rows: `⌘K / Ctrl-K`, `/` (at the start of an empty composer), `↑ ↓`, `↵`, `⌘↵`, `⇧↵`, `Esc`, each with fixed descriptive text (`search.js:694-712`)
- [ ] Rendering the help panel clears `items`/`active`, so arrow-key nav and Enter are inert until the user types something new that re-renders a real result list (`search.js:695-696, 713`)

---

## 3. Notifications

### 3.1 Preferences & storage (`notifications.js`)

- [ ] Prefs live at `localStorage["serf-hub.notifications"]`; schema version tracked separately at `localStorage["serf-hub.notifications.v"]` (`notifications.js:29-30`)
- [ ] Defaults: `{title:true, favicon:true, os:false, sound:false, loudScope:"asks"}` (`notifications.js:31`)
- [ ] `migratePrefs` is a no-op once version `"3"` is recorded. A brand-new install writes the full default blob. An EXISTING (pre-v3) blob backfills any non-boolean `title`/`favicon`/`os`/`sound` key to `false` — preserving whatever the user already explicitly set — and backfills `loudScope` to `"asks"` unless it's already `"asks"` or `"all"` (`notifications.js:74-89`)
- [ ] Migration runs unconditionally at module load, before the baseline fetch (`notifications.js:279-282`)

### 3.2 Title-bar count

- [ ] Title is `"<section> · serf hub"` (or bare `"serf hub"` with no active section) whenever the `title` pref is off (`notifications.js:115-124`)
- [ ] When `title` is on, prefixes `"(<needsYou + error>) "` but ONLY when that sum is greater than 0 (`notifications.js:122-123`)
- [ ] Section-name resolution: prefers the settings-page URL-derived section (via a `SECTION_LABELS` lookup on `/settings/<section>`) when a settings header element is present in the DOM; otherwise falls back to the visible workspace-header title text (`notifications.js:104-113`)
- [ ] `SECTION_LABELS` is exposed globally as `window.SerfSectionLabels` specifically so `renderer.js` (loaded later) can reuse the exact same map instead of maintaining a parallel copy (`notifications.js:94-102`, code comment)

### 3.3 Favicon dot

- [ ] `favicon` pref OFF ⇒ plain neutral-gray dot, no state indicator at all (`notifications.js:32-33, 151-155`)
- [ ] `favicon` pref ON ⇒ base neutral circle plus a small corner dot colored by the HIGHEST-priority active attention level, checked in this strict order: `error > needs_you > working`; no dot at all if none apply (idle is never drawn) (`notifications.js:35-44, 156-161`)
- [ ] Dot colors are PINNED dark-theme constants regardless of the page's own active light/dark theme, because the favicon always renders against dark browser chrome: `error=#f7768e`, `needs_you=#e0af68`, `working=#7aa2f7` (`notifications.js:35-44`, code comment)
- [ ] Favicon is an inline `data:image/svg+xml` URI rebuilt fresh on every apply, targeting (or creating, if missing) a single `link[rel="icon"]` element (`notifications.js:126-149`)

### 3.4 OS notification

- [ ] Requires ALL of: `"Notification" in window`, `Notification.permission === "granted"`, AND the document currently NOT focused (`notifications.js:171-173`)
- [ ] Notification title is `"serf · <entry.title || entry.threadId>"`; construction failures are silently swallowed (`notifications.js:174-179`)
- [ ] Clicking the OS notification focuses the window (best-effort, wrapped in try/catch) and navigates to `/s/<threadId>` (`notifications.js:180-185`)

### 3.5 Sound

- [ ] Requires the document currently NOT focused — a SEPARATE re-check of the same condition already gated at the call site (`notifications.js:189`, cf. 262)
- [ ] Uses `AudioContext`/`webkitAudioContext`; a missing constructor, or any construction/graph-wiring error, is silently swallowed (no sound, no throw) (`notifications.js:190-215`)
- [ ] Fixed 800Hz oscillator tone; gain `0.1` when `createGain` is available (falls straight to destination otherwise); stopped and the context closed after exactly 120ms (`notifications.js:199-213`)

### 3.6 Baseline fetch & edge-fire gating

- [ ] Baseline comes from `GET /api/tree?summary=1` — deliberately summary-only, never the full tree, since this client only needs the badge counts (`notifications.js:222-227`, code comment at 220-221)
- [ ] Baseline is (re-)fetched on init AND on every appwire reconnect (a dropped connection can miss broadcasts, so reconnect re-syncs rather than trusting the gap stayed empty) (`notifications.js:222-227, 288-289`)
- [ ] `summary` stays `null` until that first baseline resolves; ALL edge-firing (OS + sound) is suppressed until a baseline exists — specifically so reloading the hub can never re-alert on attention that was ALREADY true before the page opened (`notifications.js:46-50, 217-221, 258, 261`)
- [ ] Title/favicon counts (`applyCounts`) update UNCONDITIONALLY on every `serf/attention/changed` broadcast — even before a baseline exists, even while unfocused, even on a non-leader tab. Only the OS/sound edge-fire is gated on those conditions (`notifications.js:256-260`)
- [ ] Edge-fire additionally requires the document be unfocused (checked AGAIN here, on top of the per-channel checks in §3.4/§3.5) and this tab be the elected leader (`notifications.js:261-263`)
- [ ] Only entries that just transitioned INTO `needs_you`/`error` FROM something else fire at all — a level that was already alarming before the broadcast stays silent (`notifications.js:264-267`)
- [ ] Within a qualifying transition, `loudScope` narrows further: `"asks"` (default) fires ONLY for an `askPending` transition or an `error`; `"all"` fires for every qualifying transition (`notifications.js:268-269`)
- [ ] `os` and `sound` prefs are checked independently of each other even after the transition/loudScope gate passes — either, both, or neither can fire per event (`notifications.js:270-271`)
- [ ] `renderer.js` dispatches a `serf-hub:thread-status` DOM event on a live `THREAD_STATUS_CHANGED` frame for the currently-open thread; this module listens and re-fetches the baseline immediately, ahead of the next hub-side attention tick (`notifications.js:291-294`, out-of-scope caller)

### 3.7 Single-tab election

- [ ] Election uses the Web Locks API: `navigator.locks.request("serf-hub-os-leader", {ifAvailable:true}, cb)`. The FIRST tab to acquire the lock holds it forever (its callback returns a Promise that never resolves), so every subsequent tab's `ifAvailable` request fails immediately and self-identifies as a follower (`notifications.js:235-244`)
- [ ] Environments without `navigator.locks` (or where the request itself throws/rejects) fall back to treating EVERY tab as leader — a duplicate alert is judged the lesser evil versus a silent one (`notifications.js:236, 241, 244`)
- [ ] **No BroadcastChannel-based election exists anywhere in this codebase** — verified via a repo-wide grep for `BroadcastChannel` across `cmd/serf-hub/assets` and `cmd/serf-hub/templates` (zero hits, outside `node_modules`). Web Locks is the sole mechanism; do not expect (or feel obligated to reproduce) a BroadcastChannel counterpart

### 3.8 Settings coupling (`settings-notifications.js` ↔ `notifications.js`)

- [ ] Any `input[type=checkbox][data-notif]` change updates `localStorage["serf-hub.notifications"][key]`, syncs the visible ON/OFF `.state` label, dispatches `serf-hub:notifications-changed` with `{key, value}` in `detail`, and toasts "Settings saved" (`settings-notifications.js:18-36, 60-61`)
- [ ] Turning the `os` checkbox ON while `Notification.permission === "default"` DEFERS the commit: it calls `Notification.requestPermission()` first, commits only on `"granted"`, and on any other outcome reverts the checkbox to unchecked, writes `os:false`, re-syncs the label, and toasts a "Browser denied notification permission" WARNING (no success toast on that path) (`settings-notifications.js:42-58`)
- [ ] Any `input[type=radio][data-notif-radio]` change (currently only `loudScope`) writes the value, dispatches the same `serf-hub:notifications-changed` event, and toasts "Settings saved" (`settings-notifications.js:64-75`)
- [ ] `applyNotifState` re-checks every notif checkbox and the `loudScope` radio group from storage whenever a settings pane loads (`DOMContentLoaded`) or is htmx-swapped in (`htmx:afterSwap`) (`settings-notifications.js:78-92, 107-108`)
- [ ] `notifications.js`'s OWN `serf-hub:notifications-changed` listener (`onPrefsChanged`) is a SECOND, independent safety net: if `os` is on but permission is still `"default"`, it ALSO requests permission and reverts checkbox+prefs+label on denial — explicitly documented as defensive/redundant, covering only the case where something else dispatches the event without going through `settings-notifications.js`'s own gate (`notifications.js:297-328`, code comment at 297-303)
- [ ] `onPrefsChanged` always re-applies title/favicon immediately using the CURRENT (unchanged) summary, regardless of which specific pref just changed (`notifications.js:326-327`)
- [ ] Title/favicon are ALSO re-applied after every `htmx:afterSettle` on `document.body` (any workspace/settings navigation), which additionally resyncs the settings header's visible section label and the settings-nav active-link highlighting — this whole handler is gated on an `initialized` flag so it can't fire before `init()` has run once (`notifications.js:332-357, 338`)

### 3.9 Cross-cutting hub notifications (second, independent IIFE at the bottom of `notifications.js`)

- [ ] `serf/auth/updated` → reloads the credentials/instances panel in place if `#instances-root` is currently loaded (`data-loaded="true"`); separately re-fetches the `/settings/providers` partial via `htmx.ajax` if that's the currently active settings pane (`notifications.js:396-411`)
- [ ] `serf/launch/updated` → re-fetches whatever `/settings/*` partial is currently open, preserving the existing query string (e.g. `?cwd=`) (`notifications.js:412-417`)
- [ ] `serf/marketplace/updated` / `serf/plugin/updated` → re-fetches the plugins-manager partial, but ONLY if it's the currently active settings pane — explicitly to avoid a stale "Install" button state or a stale tab silently clobbering another tab's `AutoUpgrade` setting on reinstall (`notifications.js:418-434`, code comment)
- [ ] This entire second IIFE no-ops immediately if `window.SerfAppwire`/`onNotification` isn't present (`notifications.js:391-393`)

---

## 4. Theme, density, font & display preferences

### 4.1 Theme — core primitive (`theme.js`)

- [ ] `window.serfHub.setTheme(theme)`: for `"light"`/`"dark"`, sets `<html data-theme="…">` AND `localStorage["serf-hub.theme"]`; for anything else (including `null`/`"system"`), REMOVES both the attribute and the storage key, letting `prefers-color-scheme` take over (`theme.js:3-11`)

### 4.2 Theme — FOUC avoidance (NOT in theme.js — inline per-document)

- [ ] An inline `<script>` in `<head>`, placed BEFORE any stylesheet/font `<link>`, synchronously reads `localStorage["serf-hub.theme"]` and sets `data-theme` pre-paint. The identical 6-line block is duplicated verbatim in both the main app shell and the standalone thread-embed document (`templates/app.html:17-24`, `templates/thread.html:9-15`)
- [ ] CSS is bound EXCLUSIVELY to `:root[data-theme="light"|"dark"]`; there is no CSS rule anywhere keyed off a `.light-theme`/`.dark-theme` class (`cmd/serf-hub/assets/style.css:1-2, 178, 204, 229-237`; confirmed via a repo-wide grep returning 0 hits for `.light-theme`/`.dark-theme` selectors)

### 4.3 Theme — settings-page coupling (`settings-appearance.js`)

- [ ] `input[name="theme"]` change: value `"system"` calls `setTheme(null)`; any other value calls `setTheme(v)` directly; toasts `"Theme: <v>"` (`settings-appearance.js:12-17`)
- [ ] Radio values are exactly `system` / `dark` / `light` (`templates/partials/settings/theme.html:9-11`)
- [ ] `applyAppearanceState` re-checks the correct theme radio from `localStorage["serf-hub.theme"] || "system"` on `DOMContentLoaded` and every `htmx:afterSwap` (`settings-appearance.js:47-52, 72-73`)

### 4.4 Theme — palette command (`search.js`) — **divergent implementation, not the same code path as §4.1/§4.3**

- [ ] `/theme` command only offers two enum choices, `dark`/`light` — there is no `system`/"unset" option here, unlike the settings page's 3-way radio (`search.js:336-339`)
- [ ] Its handler, `applyTheme(name)`, does NOT touch `data-theme` at all — it toggles `.light-theme`/`.dark-theme` CLASSES on `<body>` and separately writes `localStorage["serf-hub.theme"]` (`search.js:550-554`)
- [ ] Net effect (see hazard #1 at the top of this document): because the CSS has no rule bound to those body classes, running `/theme` from the palette has **no visible effect on the currently-open page**. The `localStorage` write only takes effect retroactively on the NEXT full page load, when the FOUC script (§4.2) reads it back and sets `data-theme` correctly that time. A parity rewrite must either reproduce this same "changes nothing until reload" behavior faithfully, or make an explicit, called-out decision to fix it — don't silently normalize it to match `theme.js`'s immediate-effect behavior without flagging the change

### 4.5 Phone density (`settings-appearance.js`)

- [ ] `input[name="phone-density"]` change writes `localStorage["serf-hub.phone-density"]` and sets `document.body.dataset.phoneDensity` immediately — no toast shown (unlike theme/font/sidebar changes) (`settings-appearance.js:19-24`)
- [ ] Values are `compact` / `comfortable` (`templates/partials/settings/theme.html:20-21`); default when unset is `"compact"` (`settings-appearance.js:55, 79`)
- [ ] Applied to `<body>` on EVERY page load (not just when a settings pane is visible), via a standalone parse-time IIFE (`settings-appearance.js:76-81`)
- [ ] `applyAppearanceState` also re-checks the density radio group on swap/load (`settings-appearance.js:53-57`)

### 4.6 Sidebar mode (`settings-appearance.js`) — bundled in the same file though not itself theme/density/font

- [ ] `input[name="sidebar-mode"]` change (`auto`/`rail`/`pane`) prefers `window.SerfSidebar.applySidebarMode(v)` when that global is available, else falls back to writing `localStorage["serf-hub.sidebar.rail"]` directly (`settings-appearance.js:26-33`)
- [ ] `applyAppearanceState` prefers `window.SerfSidebar.readSidebarMode()` when available, else reads that same fallback key, defaulting to `"auto"` (`settings-appearance.js:58-64`)
- [ ] Radio values per template: `auto` / `pane` / `rail`, labeled "Auto" / "Pane" / "Collapsed" — note the radio VALUE for the "Collapsed" label is literally `rail`, and the fallback storage key is `serf-hub.sidebar.rail` (NOT `...sidebar-mode`) (`templates/partials/settings/theme.html:30-32`)

### 4.7 Font size (`settings-appearance.js`)

- [ ] `input[name="font-size"]` change writes `localStorage["serf-hub.appearance.fontSize"]` and sets `document.body.dataset.fontSize` immediately (`settings-appearance.js:36-41`)
- [ ] Values are `s` / `m` / `l` / `xl` (`templates/partials/settings/theme.html:41-44`); default when unset is `"m"` (`settings-appearance.js:67, 86`)
- [ ] Applied to `<body>` on every page load via a standalone parse-time IIFE, same pattern as density (`settings-appearance.js:83-87`)
- [ ] `applyAppearanceState` also re-checks the font-size radio group on swap/load (`settings-appearance.js:65-69`)

### 4.8 Composer display prefs (`settings-display.js`) — grouped with theme/density/font in scope, functionally a separate "composer" prefs blob

- [ ] Single JSON blob at `localStorage["serf-hub.composer"]`; a parse failure or absent key degrades to `{}` (`settings-display.js:11-13`)
- [ ] Defaults are baked into the READER, not written eagerly: `enterToSend` defaults to `false` (only `true` when the stored value is `=== true`); `showCost` defaults to `true` (only `false` when the stored value is `=== false`) (`settings-display.js:14-18`)
- [ ] `enterToSend` checkbox change persists, syncs the ON/OFF label, re-applies composer keybind hints, and toasts "Settings saved" (`settings-display.js:33-40`)
- [ ] `showCost` checkbox change persists, syncs the label, sets `document.body.dataset.showCost`, and toasts "Settings saved" (`settings-display.js:42-49`)
- [ ] Checkbox targets in the DOM are `input[type=checkbox][data-composer="enterToSend"]` and `[data-composer="showCost"]` (`templates/partials/settings/display.html:9, 19`)
- [ ] `applyComposerKeybindHints` swaps the visible send-button `<kbd>` hint between `↵` (enterToSend ON) and `⌘↵` (OFF), and the steer-button `<kbd>` hint between an empty string (ON) and `⇧↵` (OFF) (`settings-display.js:70-77`)
- [ ] Keybind hints are re-applied on both `DOMContentLoaded` and `htmx:afterSwap`, same lifecycle as the toggle-state reflection (`settings-display.js:79-82`)
- [ ] `document.body.dataset.showCost` is set from stored prefs at PARSE TIME — a bare top-level IIFE, not gated on `DOMContentLoaded` — specifically so the `body[data-show-cost="false"]` CSS gate is already correct before any settings pane has even opened (`settings-display.js:84-88`, code comment)
- [ ] Module exposes `window.SerfSettingsDisplay = {readComposerPrefs, writeComposerPrefs, syncToggleState, applyComposerKeybindHints}` for reuse by other modules (`settings-display.js:28`)

---

## Item count

250 checkbox items across 4 sections (109 in §1 Spawn incl. dir-picker.js, 71 in §2 Palette/Search, 41 in §3 Notifications, 29 in §4 Theme/Density/Font/Display), plus 5 non-checkbox cross-cutting hazard notes at the top.
