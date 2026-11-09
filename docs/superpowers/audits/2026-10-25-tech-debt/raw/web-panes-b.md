# Tech-debt audit: web-panes-b

Slice: `cmd/evener-hub/frontend/src/panes/` second half by name: `spawn/`, `transcript/`, `welcome/`, `zoom/` (non-test). The first half (`doc`, `session`, `sessionPanels`, `settings`) belongs to another slice. I read into it only to find duplicates.

## Inventory

| Dir | Non-test files | Non-test LOC | Test files | Notes |
|---|---|---|---|---|
| spawn | 20 | 6,544 | 18 (15,742 LOC) | `Spawn.tsx` 3,279; `Spawn.test.tsx` 11,489 |
| transcript | 9 | 1,190 | 5 | Only a job-log pane plus a legacy recovery shim |
| welcome | 4 | 188 | 3 | |
| zoom | 11 | 1,527 | 5 | The agent cascade ("sessionZoom" pane) |

`Spawn.tsx` numbers:
- `SpawnForm` is one function body from line 281 to 3262, about 2,980 lines.
- It holds 24 `useState`, 22 `useEffect`/`useLayoutEffect`, 25 `useRef`, 17 `useCallback` and 0 `useMemo`.
- 1,171 of its 3,279 lines (36%) are comments.
- 64 comment lines carry history markers: "round nine", "component 07b review", "kata xgk8", "floor §1.x", "spawn.js:NNN".
- `git log` shows 100 commits touching the file, 58 of them in the last 60 days.

Overall health: the transcript, welcome and zoom directories are in decent shape. The debt is concentrated in `spawn/`:
- A god component whose correctness depends on seven hand-rolled "host changed" refs.
- A second implementation of the launch-config form and host-routed path RPCs that the settings pane and the shared client already own. Parts of it have drifted (MCP add syntax, default labels).
- A copy of the composer's slash-completion machine, with at least one behavioural drift.
- A 11.5k-line test file that includes CSS-source greps and copy assertions.

## Findings

### web-panes-b-1: `SpawnForm` is a single ~2,980-line function (high)

Locations:
- `panes/spawn/Spawn.tsx:281-3262` (`SpawnForm`)
- `Spawn.tsx:1213-1311` (`providerConnected`, about 100 lines of nested async fencing)
- `Spawn.tsx:1550-1635` and `1688-1795` (catalog and model-list effects)
- `Spawn.tsx:2266-2538` (`doSpawn`)

Evidence:
- The counts above show one function owning many unrelated concerns:
  - host selection and fallback
  - provider onboarding completion with generation fencing (`ProviderCompletionAttempt`, `completionOwned`, `providerCompletionGeneration`, `credentialChangeGenerationRef`)
  - directory re-seeding
  - harness and schema catalogs
  - model catalog stamping
  - slash menu
  - attachments
  - plugin preview
  - effort ladder
  - launch submit
  - create-directory dialog
  - view-ownership fencing
- Ownership state is spread across refs read by distant effects. For example `completionOwned` is touched at 414, 422, 436, 495, 1109-1311 and 1700-1730.
- The file churn (58 commits in 60 days) means every change re-reads the whole thing.
- The 11.5k-line test file exists because nothing smaller can be mounted.

Impact:
- It is nearly impossible to reason locally about a change.
- Ordering bugs between effects (see -4) are likely.
- The test file can only drive the whole pane.

Fix plan: behaviour-preserving extraction into hooks beside `Spawn.tsx`. The existing `Spawn.test.tsx` is the safety net, so run it before and after each step. Order, smallest and most isolated first:
1. `useProviderCompletion(draft, ...)` takes the attempt, ownership and `providerConnected` logic (about 250 lines).
2. `useSpawnModelCatalog` takes `loadModelList`, `loadCatalog`, the stamp and merge state, `scopedModelCatalog` and the global model request (see -10).
3. `useSpawnSlashPrompt` takes the slash token, items, key handling and commit (see -6).
4. `useLaunchSubmit` takes `doSpawn`, `handleSpawn`, `handleCreateConfirm` and the busy guard (see -5 and -7).
5. `useHostScopedCatalogs` takes the harness and schema loading and reconciliation.

`SpawnForm` should shrink to composition plus JSX (about 600 lines). Each extracted hook gets focused tests, so new coverage replaces nothing and the big file can start shrinking.

Docs: `docs/product/subsystems.md` S16 links `panes/spawn/Spawn.tsx` as the web launch owner. Add the new hook files to that row in the same change.

Size: about 3,000 lines moved, net +150. Risk: medium. The effects are order-sensitive, so move one hook per commit and keep `Spawn.test.tsx` green.

### web-panes-b-2: Narrating and history comments dominate the code and several are stale or wrong (medium)

Locations:
- `Spawn.tsx` (64 history markers, 1,171 comment lines)
- `spawnDefaults.ts` (22 markers, 97 comment lines in 274 lines)
- `spawnDrafts.ts`, `startThread.ts:1-5`, `preflight.ts:1-7` ("T1 defines the signatures... T2 wires them"), `accessMode.ts`, `urlPrefill.ts`
- Duplicated pane `index.tsx` boilerplate in `welcome/index.tsx:1-7`, `spawn/index.tsx`, `transcript/index.ts`

Evidence:
- `spawnDefaults.ts:109-186` has a 25-line narrative: "Round nine fixed exactly that for the model alone (the carry-over the model line below replaced) and the fix HID the rest..."
- References to a legacy file that no longer exists: `find src -name spawn.js` returns nothing, yet comments cite "spawn.js:84-88,100", "spawn.js:154-175" and "spawn.js:527-566".
- Wrong or stale statements:
  - `Spawn.tsx:1042`, `1495` and `2330` say "Source-aware discovery needs the evener/host/request proxy, which is not in this branch". The same functions call `hostRequest(...)` a few lines away.
  - `Spawn.tsx:160-161` says the fallback effort ladder matches "StatusRow.tsx's DEFAULT_EFFORT_LEVELS". That constant no longer exists; StatusRow uses the shared `sessionEffortLevels`.
- Identical 7-line catch-block comment pasted at `Spawn.tsx:2599` and `2651` ("friendlyLaunchErrorMessage, not errorText: ...").
- Every pane `index.tsx` repeats a paragraph about why it is split from the component file.

Impact: AGENTS.md and the owner's rules say history belongs in commit messages. These comments cost reading time and the stale ones mislead.

Fix plan:
1. Delete all history, "round N", "kata", "floor §", "spawn.js", "T1/T2" and "branch" narratives.
2. Keep only comments that state a current invariant, in one to three sentences each.
3. Fix or delete the three false "absent from this branch" comments and the DEFAULT_EFFORT_LEVELS reference.
4. Replace the pane-split boilerplate with one sentence, or a pointer to `paneRegistry.ts`.

No code change, so no behaviour test is needed. Do it per file, either alongside the extraction in -1 or as its own PR first (it halves the file and makes -1 easier). Size: about -1,500 lines of comments. Risk: low.

### web-panes-b-3: Spawn re-implements host-routed path and launch RPCs that `launchConfigStoreForHost` already provides (high)

Locations:
- `Spawn.tsx:1313-1324` (`listRecents`, `complete`)
- `Spawn.tsx:1336-1369` (`validatePath` plus the re-ask-on-host-switch loop)
- `Spawn.tsx:1371-1375` (`createDirectory`), which calls `spawn/preflight.ts:48-50` (`createDir`)
- `Spawn.tsx:1448-1457` (`resolveConfig`)
- `Spawn.tsx:1599-1607` (`evener/harnesses/list`, `evener/launch/schema`)
- `Spawn.tsx:687` and `703` (`evener/path/validate`)
- Canonical home: `stores/launchConfig.ts:71` (`launchConfigStoreForHost`), whose store (created by `createLaunchConfigStore` in `@evener/appwire-client`) already has `validatePath`, `createDirectory`, `completePaths`, `resolve` and `schema`
- Already used this way by `settings/sections/project.tsx:68-80`, `launchServer.tsx:52-64`, `inrepo.tsx:35`, `mcp.tsx:258-266` and `dirListSetting.tsx:265-273`

Evidence: settings builds `paths = { directory: { validatePath: (p,k) => store.getState().validatePath(p,k), createDirectory: ... }, complete: ... }` from `launchConfigStoreForHost(host)`. Spawn builds the same three functions from raw `hostRequest(client, submittedSource, "evener/path/validate"...)`, each with its own `r.data ?? []` null coalescing (the same Go-nil-slice workaround the shared store applies).

Impact: two implementations of the same host-routing seam. The per-host store instance also gives per-host schema caching and a "host gone" guard that Spawn bypasses.

Fix plan:
1. Replace `validatePath`, `createDirectory`, `complete` and `resolveConfig` with `const store = launchConfigStoreForHost(submittedSource)` plus thin bindings.
2. Delete `createDir` from `preflight.ts`. `preflightDir` can take `validatePath` from the store or keep `hostRequest` for its fail-open semantics.
3. Keep the "re-ask the currently selected host" loop (`activeHostRef`, lines 1336-1368), since that is Spawn-specific stale-answer protection. It wraps the store call; it does not need its own RPC.
4. Check `launchConfigStoreForHost` for a remote host behaves the same on `harnesses/list` (it may need a store method; if not, leave `harnesses` and `schema` on `hostRequest`).

Tests: existing `Spawn.test.tsx` host-routing cases (they assert `evener/host/request` wrapping); add one to `preflight.test.ts` for the store-backed path. Docs: S16 and the multi-host spec stay accurate; update the subsystem row if the owner changes.

Size: about -80 lines in Spawn, -5 in preflight. Risk: medium. Check the "gone host" behaviour of the store on an unregistered host so Spawn does not start throwing on a stale draft source.

### web-panes-b-4: Seven hand-rolled "host changed" refs and reset effects (medium)

Locations:
- `Spawn.tsx:435` (`providerDialogHostRef`)
- `Spawn.tsx:578` (`modelHostRef`)
- `Spawn.tsx:603` (`createDialogHostRef`)
- `Spawn.tsx:640-714` (`cwdHostRef`, `cwdIssuanceRef`, `cwdCancelledRef`)
- `Spawn.tsx:1336` (`activeHostRef`, assigned during render)
- `Spawn.tsx:1391` (`pathValidationTarget`)
- `Spawn.tsx:1513` (`catalogHostRef`)

Evidence: each is the same shape: `if (xRef.current !== submittedSource) { xRef.current = submittedSource; reset(...) }` inside an effect, with its own explanatory essay. The reset targets are scattered (dialog, settled stamps, create dialog, cwd, catalogs, path verdicts). The comments themselves record the bugs that grew this shape ("a rapid A to B to A switch keeps reading settled === A", "round five, round seven, round ten").

Impact: each new host-dependent piece of state needs its own ref and effect, and each past regression came from forgetting one. The pane has no single statement of "what resets when the host changes".

Fix plan: introduce one small hook, `useOnChange(value, (prev, next) => void)`, or `useHostEpoch(submittedSource)` that returns a monotonically increasing epoch plus an `isCurrent(epoch)` check. Make the resets run in one place, ordered, and give async answers a single "is this still my epoch" test instead of per-feature `superseded()` closures. Do it as part of -1 step 5. Cover with a behaviour test that switches A to B to A while requests are in flight and asserts no stale state (the existing round-five/six cases in `Spawn.test.tsx` already do most of this and should stay green). Size: about -150 lines. Risk: medium-high, so land after -1 and -3.

### web-panes-b-5: Busy-state teardown pasted 11 times; `handleSpawn` and `handleCreateConfirm` duplicate their body (medium)

Locations: `Spawn.tsx:2268-2271`, `2309-2312`, `2338-2341`, `2356-2359`, `2418-2421`, `2432-2436`, `2533-2536`, `2579-2582`, `2592-2595`, `2606-2610`, `2659-2662`; handlers at `2540-2611` and `2613-2670`.

Evidence: `busyRef.current = false; setBusy(false); setBusyStartedAt(null);` appears 11 times (`grep -c 'setBusyStartedAt(null)'` = 11). `handleSpawn` and `handleCreateConfirm` repeat: busy guard, catalog gate, set busy, capture revision and view, `try { ... await doSpawn } catch { toast "Start failed" + reset }`.

Impact: any new early return in `doSpawn` must remember the three-line reset (the comment at 2528 already records one stuck-button bug of that class).

Fix plan: a single `endBusy()` and one `launch(preflight: () => Promise<"go" | "stop">)` wrapper that owns busy set/reset, the catch toast and the finally. `handleSpawn` passes the preflight check and `handleCreateConfirm` passes `createDir`. Wrap `doSpawn`'s success path in the same try/finally so early returns cannot leak busy. Part of -1 step 4. Tests: existing double-click and stuck-Start cases, plus one test per early-return path asserting Start re-enables. Size: about -90 lines. Risk: low.

### web-panes-b-6: Slash-completion state machine is copied from `Composer.tsx` and has already drifted (high)

Locations:
- Spawn: `Spawn.tsx:786-858` (token, highlight, effects), `1014-1031` (`commitSlashCompletion`), `2173-2223` (`handlePromptKeyDown`)
- Composer: `panes/session/composer/Composer.tsx:357-384`, `833-857` (`commitSlashCompletion`), `1440-1470` (`handleKeyDown`)

Evidence:
- `commitSlashCompletion` is a line-for-line copy. The only difference is that Composer calls `editorRef.current?.focus()` after the skill/command `insertSkill`/`insertCommand` branch (`Composer.tsx:848`) and Spawn does not (`Spawn.tsx:1014-1024`). After picking a skill in the spawn prompt, focus is not returned to the editor.
- The ArrowUp/ArrowDown wrap, Tab/Enter commit and Escape dismiss blocks are identical except for modifier-key checks.
- Spawn's own comment (`Spawn.tsx:853-858`) says its highlight reset is "deliberately stricter than the composer's twin effect", i.e. the duplication is known.

Impact: the drift is a real inconsistency (focus), and the next slash-menu fix has to be made twice.

Fix plan: extract `useSlashCompletion({ editorRef, textRef, write, items, onCommitExtra })` into `panes/session/composer/` (canonical home beside `SlashCompletionMenu`) returning `{ token, setToken, open, items, activeIndex, activeId, onKeyDown, commit }`. Spawn passes `commitOnPlainEnter: true` and no Mod+Enter handling. Composer adopts it too (that edit is in the other slice's files, so coordinate). Tests (write red first): a hook test for the focus-after-skill-insert case that fails on the Spawn path today; keep both panes' existing slash tests. Size: -120 in Spawn, -100 in Composer, +140 new. Risk: medium.

### web-panes-b-7: Pre-start builtin validation lives inline in `doSpawn` and repeats the "provider/model shape" check three times (medium)

Locations:
- `Spawn.tsx:2290-2447` (builtin match and validation)
- `Spawn.tsx:2248-2264` (`slashModelBootstrap`)
- `spawn/spawnSlashMenu.ts:57-170` (`runSpawnBuiltinAfterStart`)

Evidence:
- The same `splitModelId(...)`, then `provider === "" || modelId === ""`, then toast `/model: unknown value "x"` check appears at 2259, 2344 and 2430.
- The unknown-value and needs-a-value toast strings are built in four places (2312, 2364, 2441, `spawnSlashMenu.ts:101-130`).
- The effort-ladder resolution at 2379-2394 duplicates the display ladder code at 1996-2027.
- `runSpawnBuiltinAfterStart` has an "Unreachable" branch (line ~160) and a defensive `catch`.

Impact: about 180 lines of decision logic live in an async event handler and can only be tested by mounting the pane.

Fix plan: a pure `resolveSpawnBuiltinLaunch({ match, remote, scopedCatalog, scopeMismatch, effortLevels, reasoningEffort })` in `spawnSlashMenu.ts` returning `{ kind: "none" } | { kind: "block", message } | { kind: "scalars", modelProvider?, model?, reasoningEffort? }`. `doSpawn` then does one `switch`. `slashModelBootstrap` reuses it. Unit-test the pure function over the cases the pane tests currently exercise through the DOM (remote, unknown catalog, stale scope, bare values). Do not assert on message text; assert `kind` and the carried scalars. Size: about -130 in Spawn, +90 pure, +120 tests. Risk: low (pure).

### web-panes-b-8: `usePluginPreview` and `useSpawnSlashCatalog` are near-duplicates with drift (medium)

Locations: `panes/spawn/usePluginPreview.ts:1-110`, `panes/spawn/useSpawnSlashCatalog.ts:1-144`.

Evidence: both hooks have the same structure:
- args `{client, cwd, host, launchOverrides, pluginRevision, enabled}`
- the `retryRevision` state
- the `{client, requestKey, state}` result record
- the `lastResponse` ref
- `logicalKey`/`requestKey` built with `\u0000`
- a 250 ms debounce (`PLUGIN_PREVIEW_DEBOUNCE_MS` and `SPAWN_SLASH_CATALOG_DEBOUNCE_MS`, both 250)
- "keep previous response while loading"
- "stale error retains response"
- a derived first-render state block

Drift:
- Only the slash hook guards against a swapped client with a `clientGenerationRef` (lines 54-61).
- Only the plugin hook uses an `active` flag; the slash hook uses `latestKey`.
- The plugin hook writes `launchOverridesRef.current` during render (line 40); the slash hook explains why it must not (lines 43-48).
- Request params differ only in method name and an optional `harness`.

Impact: a bug fixed in one (client swap on reconnect) is still live in the other.

Fix plan: one `useScopedHostRead<T>({ client, host, method, params, keyParts, enabled, debounceMs })` in `spawn/` returning `{ state, retry }`, built from the slash version (the more careful one). Both hooks become 15-line wrappers or are deleted in favour of direct calls. Tests: parametrize the two existing test files (`usePluginPreview.test.tsx` 411 lines, `useSpawnSlashCatalog.test.ts` 422 lines) into one shared suite and add the client-swap case for the plugin path (red first). Size: about -150 net. Risk: low-medium.

### web-panes-b-9: AdvancedOptions is a second implementation of the launch-config form, and it has drifted from settings (high)

Locations:
- Spawn side: `spawn/AdvancedOptions.tsx:278-333` (`resolvedValue`, `stringDefaultLabel`, `booleanDefaultLabel`, `inputDefaultPlaceholder`), `335-344` (`pathFieldKind`), `346-526` (`Control`), `553-733` (`PathListControl`, `ModelListControl`, `EnvControl`, `McpControl`)
- Settings/shared side: `settings/sections/launchShared/fields.tsx`, `collectionFields.tsx:131-143` (`pathFieldKind`), `242-312`, `357-400`, `404-503`; `fields.tsx:82-88` (`browsablePathKind`); `LaunchConfigForm.tsx:110` (another `resolvedValue`)
- Shared client: `appwire-client/typescript/launchSchema.ts:202-247` (`resolvedDefaultLabel`)

Evidence:
- The "maxRounds -1 or 0 means unlimited" sentinel is reimplemented at `AdvancedOptions.tsx:318-322` and in `launchSchema.ts:225-229`. The comments in both cite the same Go flag.
- `resolvedValue` exists in both `AdvancedOptions.tsx:278` and `LaunchConfigForm.tsx:110`.
- Three `pathKind` to widget-kind mappers: `AdvancedOptions.tsx:335` (returns null for non-browsable), `fields.tsx:82` (a lookup table) and `collectionFields.tsx:131` (falls back to "dir").
- The MCP server add syntax differs between the two surfaces for the same config field. Spawn's `McpControl` (`AdvancedOptions.tsx:716-731`) accepts `name=command arg1 arg2` and splits on `/\s+/`, so quoted args and spaces in paths are impossible and the command is not validated. Settings' `parseMcpServerEntry` (`collectionFields.tsx:417-429`) accepts `name command [args...]` with shell-style quoting and validates the command via `evener/path/validate kind=command`.
- Env, path list and model list controls are structurally the same `CollectionEditor` wrappers in both places.

Impact: users type a different syntax into the same field depending on the screen, and every schema kind added to the shared form has to be added to Spawn separately.

Fix plan:
1. Move the shared primitives to the shared client, which already owns `resolvedDefaultLabel`: add `inputDefaultPlaceholder` and `booleanLabel` there (derived from `resolvedDefaultLabel`) and delete Spawn's copies. Export one `browsablePathKind`.
2. Make `AdvancedOptions` render the settings field components (`ScalarField`, `PathListField`, `ModelListField`, `EnvMapField`, `McpServerListField`) from `launchShared`, passing the `paths` binding from -3. Spawn already has the `LaunchFormPaths`-shaped props.
3. Only the spawn-specific parts stay: the disclosure, `draft`-owned values and errors, and the Access-mode/Harness children.
4. Behaviour change: Spawn's MCP syntax becomes the settings syntax. That is a user-visible change, so discuss with Jesse; the settings syntax is the richer one.
5. Tests: port the unit cases for default labels and syntax into the shared client or `launchShared` tests (there is already `launchSchema.test.ts`). Keep `AdvancedOptions.test.tsx` flows that exercise draft-owned values.

Size: about -450 in AdvancedOptions, +40 shared. Risk: medium-high (visual and syntax change). Recommend splitting: PR A shares labels and kind mappers (behaviour-preserving), PR B adopts the shared field components.

### web-panes-b-10: Model-list loading has three uncoordinated consumers plus a stamp state machine (medium)

Locations:
- `Spawn.tsx:1150-1190` (`modelListCache`, `loadModelList`)
- `Spawn.tsx:1688-1763` (uncached `model/list {harness:"evener"}` global request, with its own owner and sweep protocol)
- `Spawn.tsx:1796-1848` (`modelCatalog`, `modelCatalogStamp`, `scopedModelCatalog`, `modelCatalogScopeMismatch`)
- `spawn/useProviderSetup.ts:85-103` (a third `hostRequest(client, host, "model/list", {})`)

Evidence: the comment at `Spawn.tsx:1192` says every model-valued control "consumes this one scoped response... cannot issue duplicate model/list RPCs". The global request at 1719 and `useProviderSetup` (for keyless instances) still issue their own `model/list`. The scope-stamp machinery needs about 70 lines of comments to explain three states (null, matching, mismatched).

Impact: duplicate RPCs, three places to invalidate on credential change, and an implicit protocol (`completionOwned.outstanding`) to keep the global request from undoing a provider connection.

Fix plan: fold the three into one `useSpawnModelCatalog(host, harness, cwd)` (see -1 step 2) that returns `{ loadCatalog, scoped, scopeMismatch, listForSweep }`. Give `useProviderSetup` a `listModels` parameter instead of its own request. The sweep and validation then read from the same cache with an explicit `harness: "evener"` key. Tests: the existing credential-change and provider-completion cases; add an RPC-count assertion (no second `model/list` for the same key in one render cycle). Size: net -100. Risk: medium-high (it interacts with the completion fencing, so do after -1 step 1).

### web-panes-b-11: Desktop and phone UIs are both mounted and hidden by CSS; plus raw native controls (medium)

Locations:
- `Spawn.tsx:3183` (`MobileSettingRows` always rendered), `3208-3252` (desktop Harness and Access `Select`s inside `AdvancedOptions`), `3127-3170` (desktop plugin Disclosure)
- `spawn/spawn.module.css:205-206` (`.mobileConfig { display: none }`), `322-332`
- `spawn/pluginSelection.module.css:8`, `133-135` (`.desktopSurface` hidden below 900px)
- `spawn/MobileSettingRows.tsx:66-100`, `103-135` (`OptionSheet`)
- Raw controls: `Spawn.tsx:2772` (`<button>` working-dir trigger), `2998` (native `<select>`), `3085` (`<input type=file>`); `AdvancedOptions.tsx:219` (`<button>`); `MobileSettingRows.tsx:100` and `137` (`<button>`)

Evidence:
- Harness, Access mode, working-directory picker and plugin selection each exist twice: a desktop control and a phone row plus bottom sheet.
- The two plugin summaries are derived independently: `"Configured plugins: a, b"` (`Spawn.tsx:3164`) versus `"N of M"` (`MobileSettingRows.tsx:~190` (`pluginSummary`)).
- Both trees are in the DOM at all widths, both subscribed to the same state, so `PluginSelectionPanel` can be mounted twice.
- `OptionSheet` is a hand-built option list; the widgets directory already has `Select`, `RadioGroup` and `Sheet`.
- AGENTS.md says no raw `<button>/<input>/<select>` outside `src/widgets`. This slice has 6 (`scripts/ds-audit.mjs` counts them but does not fail).

Impact: double render cost, two summaries that can disagree, and a11y or layout fixes needed in two places.

Fix plan: render one tree and choose per `useIsMobile()` (already used in `zoom/CascadeColumn.tsx`). Derive the plugin summary once (`pluginSummaryText(selection, preview)`) and use it in both. Replace `OptionSheet` buttons with the `RadioGroup` widget inside `Sheet`. For the raw controls, use `Button`/`Select` where the widget fits. A new widget prop or widget needs Jesse's approval first (AGENTS.md), so list the missing capabilities for him rather than adding them: a per-option `disabled` on `Select` (the host picker note at `Spawn.tsx:233-236`) and an overlay-select for the effort chip. Tests: the existing mobile row tests (`MobileSettingRows.test.tsx`) plus a viewport test that only one plugin panel exists at each width. Size: about -150. Risk: medium (layout guards `spawnguard` must be run in CI).

### web-panes-b-12: Shared derivations living in the pane: effort ladder and access-mode table (medium)

Locations:
- `Spawn.tsx:165` (`FALLBACK_EFFORT_LEVELS`), `199-206` (`catalogEffortLevels`), `1996-2027`
- `appwire-client/typescript/reasoningEffort.ts:28-31` (`sessionEffortLevels`)
- `spawn/accessMode.ts:15-85`
- `mobile-native/src/newSession/launchSetup.ts:111-135` (`ACCESS_LEVELS`), `mobile-native/src/session/sessionFacts.ts:77-83` (`ACCESS_MODES`)

Evidence:
- `sessionEffortLevels` already returns `["minimal","low","medium","high"]` when the model supports reasoning without a ladder, and `[]` when it does not. `catalogEffortLevels` plus `FALLBACK_EFFORT_LEVELS` in Spawn reimplement that rule, with an extra null state for "unknown".
- The four access modes (full/off, workspace-write, read-only, restricted) with labels exist three times: web `ACCESS_MODE_OPTIONS`, mobile `ACCESS_LEVELS`, mobile `ACCESS_MODES`. The descriptions already differ in wording ("The session can read files and change files inside the workspace." versus "Writes only in the project; reads anywhere but secrets").
- `sandboxForAccessMode` and `accessModeForSandbox` exist only on web.

Impact: AGENTS.md says the shared client owns shared derivations. A label or ladder change has to be made in three places.

Fix plan: move the access-mode table (value, sandbox, label) and `sandboxForAccessMode`/`accessModeForSandbox`/`mergeAccessModeSandbox` into `appwire-client/typescript` with a test; keep descriptions per client unless Jesse wants one wording. Express Spawn's ladder through `sessionEffortLevels` plus one "unknown" check. Mobile adoption can follow, tracked separately. Docs: README of the package and the subsystem row for S16. Size: -60 web, +60 shared, mobile -50. Risk: low.

### web-panes-b-13: Test suite shape: an 11.5k-line file, CSS-source greps and copy oracles (medium)

Locations:
- `spawn/Spawn.test.tsx` (11,489 lines, 282 `test(` blocks)
- CSS-source greps: `Spawn.test.tsx:2873-2890`, `2932`, `5582`; `MobileSettingRows.test.tsx:272`; `AdvancedOptions.test.tsx:780`
- Copy assertions: `Spawn.test.tsx:6155`, `6176`, `6637`, `7253`, `7305`, `7407`, `7967`, `8854`, `8891`, `9164`; `spawnSlashMenu.test.ts:102`

Evidence:
- `Spawn.test.tsx:2865-2868` states in a comment that CSS-text matching "passes even when the rules are unused or overridden", and the very next test does it (`expect(rowsCss).toContain("min-height: 48px")`).
- AGENTS.md: "Never assert on phrasing" and "Copy Is Not a Test Oracle". Examples: `findByText("Start failed: Can't reach the hub right now.")`, `getByText(/\/model: unknown value "openai\/gpt-5"/)`, and the full-sentence "No agent daemon responded..." at 6155.

Impact: the copy oracles break on every wording change, the CSS greps give false confidence, and one file this size is slow to run and review.

Fix plan:
1. Delete the five CSS-grep tests. The `spawnguard` geometry guard already covers the intent (the test comment says so).
2. Replace each copy assertion with a behaviour assertion: toast kind, that `thread/start` was or was not called, Start re-enabled. Expose a `kind` or `data-reason` where the copy was the only handle.
3. Split `Spawn.test.tsx` along the extraction in -1 (providerCompletion, hostSwitch, slashPrompt, submit, attachments, defaults). Share the harness through a `spawnTestUtils.tsx`.

Size: about -300 deleted, the rest moved. Risk: low.

### web-panes-b-14: `AdvancedOptions` supports controlled and uncontrolled modes that only the tests use (low)

Locations: `spawn/AdvancedOptions.tsx:60-103` (props), `104-130`, `135-175`. Single production caller: `Spawn.tsx:3208`.

Evidence: `values`, `onValuesChange`, `readValues`, `errors`, `onErrorsChange` and `draftId` are optional. The component carries local fallback state (`localValues`, `localErrors`), an owner-key reset block (lines 146-160) and a comment about `useState` treating a function as an initializer. `grep -rn '<AdvancedOptions'` finds Spawn plus one render in each of the two test files.

Impact: roughly 60 lines of branching that production never takes.

Fix plan: make the draft-owned props required, delete the local fallbacks and the `ownerKey ?? readValues` path, and update `AdvancedOptions.test.tsx` to pass a small fake draft. Largely done as part of -9 PR B. Size: about -60 plus test edits. Risk: low.

### web-panes-b-15: Dead and pass-through code in the spawn helpers (low)

Locations:
- `spawn/startThread.ts:12-30` (`SpawnRequest.branch`), `SpawnResult`
- `spawn/preflight.ts:47-50` (`createDir`)
- `spawn/spawnDefaults.ts:42-67` (`readRaw`/`writeRaw`/`removeRaw`)
- `stores/prefs.ts:172-195` (the same three helpers)
- `spawn/spawnDrafts.ts` `resetSpawnDraftsForTests`

Evidence:
- `branch` is documented as "never sent", and no caller passes it (the `startThread(client, {...})` call at `Spawn.tsx:2469` has no `branch`, and `startThread.test.ts` has no `branch`).
- `createDir` is a one-line wrapper around `hostRequest`; it disappears with -3.
- `readRaw`/`writeRaw`/`removeRaw` in `spawnDefaults.ts` are the same try/catch localStorage wrappers as in `prefs.ts`, differing only in the key prefix. `shell/rail/railExpansion.ts`, `shell/palette/recentCommands.ts` and others repeat the try/catch inline.
- `SpawnResult { ref }` wraps a string.

Fix plan: delete `branch`. Return `string` from `startThread` (or inline it). Add `stores/safeStorage.ts` (`readString`, `writeString`, `removeKey`), used by `prefs.ts` and `spawnDefaults.ts`, as a follow-up for the other localStorage users. Size: about -60. Risk: low.

### web-panes-b-16: `job:` ref handling is a string literal in six files (low)

Locations: `zoom/intent.ts:35`, `zoom/inspectionOrigin.ts:12`, `transcript/Transcript.tsx:28`, `transcript/JobLog.tsx:137` (`slice("job:".length)`), `transcript/jobLogReader.ts:337` (`` `job:${jobId}` ``), `shell/attentionPlacement.ts:49`, `panes/session/transcript/openTranscript.tsx:124,182`; `shell/workspace.ts:213` has a private `JOB_REF_PREFIX`.

Evidence: grep for `startsWith("job:")` finds 5 non-test sites besides workspace.ts. A private constant exists but is not exported.

Fix plan: export `isJobRef(ref)`, `jobIdOf(ref)` and `jobRef(id)` from one module (a ref helper near `refParam` in `shell/routing.ts`, or the shared client if mobile also needs it), and replace the call sites. Size: about -20. Risk: low.

### web-panes-b-17: The "transcript" pane type is a job-log pane plus a legacy session-recovery shim threaded through the shell (medium)

Locations:
- `transcript/Transcript.tsx:1-79` (migration effect `42-65`)
- `transcript/index.ts`
- `zoom/intent.ts:84-86`, `95-97` ("legacy source naming a read-only transcript")
- `zoom/inspectionOrigin.ts:9-18`
- `zoom/actions.ts:35` ("Legacy Return")
- About 25 `type === "session" || type === "transcript"` sites in the shell: `shell/workspace.ts:83-88, 218, 365, 406, 468`, `shell/AppShell.tsx:265, 313`, `shell/mobile/StackHost.tsx:371-455`, `shell/needsYouActions.ts:76`, `shell/attentionPlacement.ts:47`, `panes/session/transcript/useTranscript.ts:102, 124`

Evidence: the header of `Transcript.tsx` says the read-only session transcript "is gone"; the pane now renders `JobLog` for `job:` refs and otherwise migrates a stale saved-layout record to a `session` pane at runtime. Zoom's parser still accepts `source.type === "transcript"` for the same reason.

Impact: the name `transcript` now means "job log", while a second meaning (legacy session view) is kept alive in four modules and many two-way type checks. Each check is a place to get the conversation-identity rules wrong.

Fix plan (needs Jesse's approval, since it removes existing compat):
1. Add a one-shot layout-restore migration that retypes `transcript` panes with a non-`job:` ref to `session`, and zoom params with `type: "transcript"` to `session`. `onWorkspaceRestore` already exists (used in `inspectionOrigin.ts:76`).
2. Delete the runtime recovery effect in `Transcript.tsx`.
3. Rename the pane type and directory to `jobLog`, and collapse the two-way checks.

Tests: a restore test with a saved layout containing a legacy `transcript` session-ref pane and a legacy zoom source (red first against current "recovers at runtime" behaviour, then green via the migration). Docs: update the pane list in the web routing doc and `docs/product/subsystems.md` if it names the pane. Size: about -150. Risk: medium (persisted layouts).

### web-panes-b-18: `Welcome` props are unused generality and it imports from the spawn pane (low)

Locations: `welcome/WelcomeContent.tsx:23-27, 64`; `welcome/Welcome.tsx:11-18, 22-35`.

Evidence:
- The only consumer renders `<WelcomeContent showNewSession showHints />` (`Welcome.tsx:34`). `note` and `showResume` are never passed, and `Welcome.tsx:24-29` has a paragraph explaining why `note` is deliberately not passed.
- `WelcomePaneParams.note` has no producer outside tests (`grep` finds only `Welcome.test.tsx:118` and `index.test.tsx:42`).
- `Welcome.tsx` imports `useProviderSetup` from `../spawn/useProviderSetup` (cross-pane dependency for a redirect rule).

Fix plan: drop `note`, `showResume` and the optional flags (always show the resume candidate, new-session button and hints), delete `WelcomeContent.tsx` or inline it. Move `useProviderSetup` to `stores/` (it already depends only on stores) so `welcome` does not import `spawn`. Tests: keep behaviour tests for the redirect and the resume candidate; remove the `note` test. Size: about -50. Risk: low. Removing `params.note` removes a (test-only) param shape, so confirm no stored layouts carry it.

### web-panes-b-19: Zoom: duplicated context lookup, side-effect imports and inline scroll-follow logic (low)

Locations:
- `zoom/actions.ts:30-33` (`contextFor`) versus `zoom/cascadeCoverage.ts:12-14` (the same `connectionStore` to `sessionActivitySnapshot(client, ref, "session")?.context` expression)
- `zoom/actions.ts:24-26` (`import "./index"; import "../session"; import "../transcript";`)
- `zoom/Zoom.tsx:195`, `244-290` (scroll-follow with `ResizeObserver` and `MutationObserver`, about 45 lines inline)
- `zoom/Zoom.tsx:138-142` (magic column widths 440, 400, 52 in TSX)
- `zoom/Zoom.tsx:107` (`(... : null) ?? null`)

Evidence: two copies of the context lookup; three bare imports in `actions.ts` register pane types as a side effect of importing the action module; the width constants sit next to `zoom.module.css` rules that otherwise own layout.

Fix plan:
- Export `cascadeContextFor(ref)` once from `actions.ts` or `intent.ts` and use it in `cascadeCoverage.ts`.
- Drop the side-effect imports and let the app shell registration cover pane types (verify the unit tests that need them import them explicitly).
- Move the scroll-follow effect into `useFollowTrackEnd(trackRef, active)`.
- Move the widths into CSS custom properties.

Tests: the cascade guard and `Zoom.test.tsx` already cover scrolling. Size: about -30. Risk: low.

### web-panes-b-20: `JobLogReader` is a dense undocumented state machine (low)

Locations: `transcript/jobLogReader.ts:48-340` (340 lines, 5 comment lines), `pump` (lines 165-192), `readOutput` (lines 247-318).

Evidence: about 12 mutable fields (`generation`, `terminalObservation`, `historyServedSinceLive`, `nextReadAt`, `liveDueAt`, `metadataDueAt`, `outputInFlight`, and so on) interact through `pump()`, and the only comments are one-liners. `JobLog.tsx` has 6 comments in 417 lines with several 30-line scroll-anchoring helpers.

Fix plan: do not rewrite. Add a short invariants header (what `liveDueAt`/`nextReadAt`/`drainPending` mean, why one transport slot per pane), and unit tests on the pure scheduling decision if `pump` is split into `nextActions(state, now)`. Size: about +40 comments, optional -0 code. Risk: low.

## Recommended fix sequence

1. Comment and stale-reference cleanup (-2), starting with `Spawn.tsx` and `spawnDefaults.ts`. This halves the file and makes the rest of the work readable.
2. Small safe consolidations: -15 (dead `branch`, shared storage helpers), -16 (`job:` helpers), -12 (shared effort ladder and access-mode table), -19 (`contextFor` and widths).
3. Tests (-13): delete CSS greps, replace copy oracles with behaviour assertions. Do this before the big extraction so the safety net does not encode wording.
4. Host-routed seam (-3), then the shared labels and mappers PR for -9 (PR A).
5. Extract hooks from `SpawnForm` (-1), in the order: provider completion, model catalog (-10), slash prompt (-6, which also fixes the focus drift), submit and busy (-5, -7), host epoch (-4). Merge `usePluginPreview` and `useSpawnSlashCatalog` (-8) alongside. Split `Spawn.test.tsx` as each hook moves.
6. UI duplication: one tree for desktop and phone (-11), `AdvancedOptions` on the shared field components with the MCP syntax decision (-9 PR B, -14).
7. Legacy `transcript` pane retirement (-17) after approval, then `Welcome` simplification (-18), then the `JobLogReader` doc pass (-20).

Items needing Jesse's input before work: the MCP add-syntax unification (-9), removing the legacy `transcript` session shim and renaming the pane (-17), and any new widget capability for -11.
