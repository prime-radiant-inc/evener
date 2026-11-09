# Tech-debt audit: web-shell-widgets

Slice: `cmd/evener-hub/frontend/src/{shell,widgets,notifications,transcriptDisplay,keybindings,styles,motion}` plus loose non-test files at `src/` root. Audited at origin/main `a6c0b9c561`, read-only.

## Overview and inventory

| Dir | Files (all) | Non-test lines |
|---|---|---|
| shell | 196 | 19,539 |
| widgets | 271 | 18,458 |
| notifications | 12 | 521 |
| transcriptDisplay | 7 | 453 |
| keybindings | 4 | 236 |
| styles | 18 | 672 |
| motion | 3 | 72 |
| src/ root (non-test) | ~14 non-test files | ~600 (mostly test-harness guards) |

Tests in these dirs total about 64,300 lines against about 40,000 non-test lines. Biggest non-test files: `shell/rail/Rail.tsx` 2,133; `shell/palette/CommandPalette.tsx` 1,202; `shell/rail/railNodes.ts` 1,146; `shell/AppShell.tsx` 1,135; `shell/workspace.ts` 955; `shell/rail/RailRow.tsx` 903; `shell/palette/commands.ts` 832; `widgets/pathfield/index.tsx` 676; `shell/DockHost.tsx` 667. Biggest tests: `Rail.test.tsx` 3,617, `RailRow.test.tsx` 2,453, `styles/token-contract.test.ts` 1,519.

Design-system conformance (`node scripts/ds-audit.mjs`, repo-wide): 23 raw `<button>`, 6 `<input>`, 2 `<select>`, 2 `<textarea>` outside widgets, 38 module classes re-implementing a widget concept, 45 spacing / 29 radius / 14 duration literals bypassing tokens, 14 declared tokens never referenced, 8 `var()` references to undeclared tokens. In this slice: raw controls at `StatusBar.tsx:52`, `RailRow.tsx:572`, `CommandPalette.tsx:786,791,1064`; re-implemented-widget classes in `statusbar.module.css` (`.chip .chipGlyph .chipName .chipCount`), `holdhints.module.css` (`.chip`), `commandpalette.module.css` (`.pill .pillBack`), `RailHost.module.css` (`.chipBar`), `RailRow.module.css` (`.chevronButton .contextCard`), `activitybar.module.css` (`.foldButton`).

Overall health: the widget library itself is mostly well-factored (barrel, requireClass, realm-aware portals). The debt concentrates in three places: (1) the shell's two mega-files (`Rail.tsx`, `AppShell.tsx`) that embed data adapters and state machines; (2) review-round and "port of legacy JS" narration that has outlived the code it describes; (3) near-copy helpers (keyboard stepping, wire-state classification, page sorting, chunk retry) that exist in 3-10 places each. There are also four shipped widgets with zero product consumers.

---

## Findings

### web-shell-widgets-1 (high): Rail.tsx embeds a ~500-line navigation-to-rail adapter with a "compatibility" path that production never takes, and 10+ copies of page-sort/last-page logic

Locations: `shell/rail/Rail.tsx:463-960` (`summarySession`, `projectPageStates`, `cachedProject`, `loadedSection`, `projectFromSummary`, `graphSessionsForResource`, `projectFromGraph`, `withArchivedList`, `projectsFor`, `catalogOverflowFor`, `railResources`, `adaptNavigationResources`), `Rail.tsx:154-157` (`mode: "compatibility" | "graph"`), `Rail.tsx:582-600`. Shared owner: `appwire-client/typescript/state/navigation/selectors.ts:98-170`.

Evidence:
- Every fetched resource gets `normalized` (`appwire-client/typescript/state/navigation/store.ts:507-523` builds it for snapshot, delta and gone, throws otherwise), yet `Rail.tsx` keeps a parallel path for `!resource.normalized`: `loadedSection` (`:628-646` falls back to `resource.data.sessions`), `projectsFor` (`:831-846`), `projectFromSummary` (the whole "compatibility" builder, `:655-697`, 40 lines, with its own cache key `compatibilityError`). Only tests that seed `resources` by hand without `normalized` reach it.
- "Sort the matching pages by offset/limit, take the last" is hand-written at `Rail.tsx:551, 623, 823, 874, 893, 910`, `needsYouActions.ts:51, 387`, and again in the shared selectors (`selectors.ts:115, 131, 166`). Ten copies of one comparator.
- `loadedSection` (`Rail.tsx:609`) re-derives what `selectLiveRows`/`selectNeedsYouRows`/`selectSectionRemaining`/`selectNextSectionOffset` already return from the shared client.
- Presentation component file also holds `adaptNavigationResources`, exported only for a test.

Impact: the biggest drift risk in the slice. The product guide says shared client owns shared derivations; the rail model is a web-only fork of them, and any change to paging has to be made in a dozen places. A component file this size is also why `Rail.tsx` needs a 3,617-line test file.

Fix plan:
1. Extract a `pagesInOrder(state, predicate)` helper next to the shared selectors and replace all ten comparators (behavior-preserving; existing selector and Rail tests guard it).
2. Make test seeding build `normalized` (add a helper to `needsYouTestUtils.ts` / `Rail.test.tsx`'s `resource()` at `:83`), then delete the non-`normalized` branches and `projectFromSummary`, the `mode` discriminator and `compatibilityError` from the cache entry.
3. Move the remaining adapter (summary caches, `projectFromGraph`, `withArchivedList`, `railResources`) out of `Rail.tsx` into `stores/navigation/railResources.ts` (or the shared client if mobile needs it; confirm with Jesse). `Rail.tsx` keeps rendering only.
4. Update `docs/product/subsystems.md` row for the rail/navigation owner.

Size: about 350 lines deleted, 150 moved, 40 added. Risk: medium. The paging and archived-list caches are subtle; test-first by pinning current outputs for a graph fixture before deleting.

### web-shell-widgets-2 (medium): NavigationRail is a 1,120-line component owning mutations, reveal state machine and four dialogs

Locations: `shell/rail/Rail.tsx:1010-2130` (`NavigationRail`), reveal guards `:1060-1110`, mutation callbacks `:1590-1720`, rename/delete dialogs further down; `Rail.tsx:981-1004` (`archiveSessionIdentity`).

Evidence: one function body of ~1,120 lines with 9 `useRef`, 7 `useEffect`, 8 `useCallback`, rename dialog state (`sectionRenameTarget/Value/Error/Submitting` plus two token refs), delete-section and delete-project dialogs, optimistic `pending` ops, overflow paging and reveal. `archiveSessionIdentity` carries a 25-line comment citing "round eleven's high finding" and "Component 06a's round six".

Impact: every rail change touches one function; review comments have been absorbed as prose instead of structure.

Fix plan: extract (a) `useRailMutations(client, toasts, setPending)` returning the `onToggleArchiveSession/onDeleteSession/...` callbacks, (b) `PinSectionRenameDialog` and `DeleteProjectDialog` components owning their own state, (c) `useRailReveal` for the reveal refs and effects. Pure moves, tests unchanged. Trim the `archiveSessionIdentity` comment to the one rule (local rows use the bare id, remote the host-qualified ref).

Size: about 1,100 lines moved, 150 comment lines deleted. Risk: low-medium (hook dependency lists).

### web-shell-widgets-3 (high): AppShell holds a 200-line live-session demand state machine inline in a useEffect, duplicating needsYouActions' pattern

Locations: `shell/AppShell.tsx:638-844` (effect), `:601-620` (route epoch), `AppShell.tsx:461-1128` (`AppShellContent`, 670 lines); `shell/needsYouActions.ts:116-497`; `shell/rail/liveSessionCycle.ts`.

Evidence: the effect defines `LiveDemandOwner`, `demandedLivePages`, `liveNavIntent`, `liveNavGeneration`, `liveDemandOwnerFresh`, `openLiveSession`, `demandLivePage` and two registered actions. Its comments cite "roborev PR #1044 round-9 medium 3", "round-4 medium 1", "round-8 low 1", "round-11 medium 1", "round-13 low 5" (13 review-round citations in AppShell.tsx alone). The inertness guard is `owner.intent !== liveNavIntent || generation || routeEpoch || focusedPaneId || focusedSessionRef() || paletteStore.open || document.querySelector('[aria-modal="true"]') || isEditableTarget(...)` (`:742-749`); `needsYouActions.ts` implements the same guard set differently (serial counters, a MutationObserver on aria-modal, popstate/route serial). `liveSessionCycle.ts` holds only the pure index math; the stateful half lives in the shell.

Impact: two controllers for "load next page of a rail section then navigate", written at different times, with divergent guard rules; the live one is only testable through the full AppShell. This is where review-round fixes accreted.

Fix plan: create `shell/liveSessionController.ts` mirroring `createNeedsYouController` (injected store reads, `cancel()`, `dispose()`), share the "navigation is still the press that started it" guard (generation, route epoch, pane, palette/modal, editable focus) as one `createIntentGuard()` used by both, and have AppShell register two one-line actions. Move the epoch bump into `usePathname`. Delete the round-citation comments, keeping the invariants as short statements. Tests: port the 19 round-numbered AppShell tests to controller-level tests first (red-green), then delete the inline code.

Size: about 250 lines moved out of AppShell, about 60 deleted by sharing the guard. Risk: medium; keyboard navigation regressions are user-visible, so keep the AppShell integration tests.

### web-shell-widgets-4 (medium): dockHostChunk.ts is a private 216-line copy of chunkRetry.ts

Locations: `shell/dockHostChunk.ts:1-216`, `shell/chunkRetry.ts:1-256` (comment at `:13-15`), consumers `shell/rail/railHostChunk.ts`, `panes/spawn/connectDialogChunk.ts`, `shell/DockRegion.tsx`.

Evidence: `chunkRetry.ts` says "DockRegion's own dockHostChunk.ts keeps its private copy: it is the read-only reference the rail and dialog mirrors were built from, and unifying it too would touch files outside this helper's lane." That is a work-assignment artifact, not a design reason. The two files have identical `LinkDescriptor`, `URL_IN_ERROR`, `describeLink`, `setLinkAttributes`, `createRetryLink`, URL-from-error and `preloadRetryAssets` code. The rail and dialog wrappers shrink to 46-47 lines using the helper; the dock version differs only in the retry param (`evener-dock-retry`, `dockHostChunk.ts:145,152,203`) and label.

Impact: a retry-asset bugfix (CSP nonce, crossorigin, Chrome failed-module cache) must be made twice.

Fix plan: rewrite `dockHostChunk.ts` as a ~45-line wrapper over `createChunkRetryLoader` with `{chunkPath: /\/DockHost-.../, stylesheetPath, retryParam: "evener-dock-retry", assetLabel: "DockHost"}`; keep exported names (`loadDockHost`, `isStaleDockHostChunkError`, the two test seams). `dockHostChunk.test.ts` (106 lines) is the guard. Update the `chunkRetry.ts` header.

Size: about 170 lines deleted. Risk: low.

### web-shell-widgets-5 (medium): CommandPalette keeps a hand-maintained shortcut legend that the registry-driven cheatsheet already replaced

Locations: `shell/palette/CommandPalette.tsx:101-135` (`HELP_ROWS`), `:977` (render); `shell/cheatsheet/CheatsheetOverlay.tsx:1-12` (header says "no hand-maintained copy can go stale (the survey's stale-HELP_ROWS lesson)"); chord source `appwire-client/typescript/keybindingDefaults.ts` (`$mod+B` at `:104`, `$mod+I` `:114`, `$mod+J` `:122`).

Evidence: the cheatsheet comment names this exact `HELP_ROWS` as the lesson, but `HELP_ROWS` is still there with literal `["Mod","B"]`, `["Mod","I"]`, `["Mod","J"]`, `["Mod","'"]` rows. Users can rebind these (the Settings keybindings section and hub-synced overrides exist), so the palette `?` view shows chords that no longer work for them. The comment at `:101` also points at `search.js:697-705`, which does not exist.

Impact: real product drift (the legend lies after any rebind) plus two shortcut-reference UIs.

Fix plan: have the palette `?` mode render the cheatsheet's grouped rows (extract the row builder from `CheatsheetOverlay` into `keybindings/display.ts` or reuse `ACTION_DISPLAY_ROWS` + `displayBindingsFor`), keep only the palette-local rows (arrows, Enter, Esc, Mod+Enter new tab, Shift+Enter jump to turn) as a small static list, drop composer/ask-dock rows duplicated in the registry. Test red-green: rebind an action in the registry and assert the `?` view shows the new chord. Update `docs/web-ui/design-system.md` §ShortcutList consumers if listed.

Size: about 45 lines deleted, 30 added. Risk: low.

### web-shell-widgets-6 (medium): wire-state to bucket mapping is re-implemented in at least five places

Locations: `shell/sessionStatus.ts:4-22` (`cadenceStateFor`), `notifications/attention.ts:6-19` (`levelFromState`), `shell/rail/railNodes.ts:289-298` (`CURRENT_SUBAGENT_STATES`) and `:373-375` (`stateNeedsYou`), `panes/session/liveness.ts:36`, `mobile-native/src/board/attention.ts:64,86` (`EXPLAINED`, `decisiveState`); shared home `appwire-client/typescript/railSessionState.ts` (`humanizeState`, `needsResponseRest`, `approvalWaiting`).

Evidence: each file switches over `"awaiting" | "warning" | "restartRequired"` to mean needs-you, `"active"` for working, `"errored"` for failure. `railNodes.ts:371` even comments "uses the same needs-you family as shell/sessionStatus.ts's cadenceStateFor", i.e. sync by hand. The shared file already owns the humanized word and the approval/needs-response predicates, but not the family.

Impact: adding a wire state (the last one was `restartRequired`, present in all five) means five edits across web and mobile; a missed one silently changes attention, notifications or the rail dot.

Fix plan: add `stateFamily(wireState): "working" | "needsYou" | "failed" | "ended" | "idle"` (and `isNeedsYouState`) to `railSessionState.ts` with a table test covering every `NormalizeState` value; reimplement `cadenceStateFor`, `levelFromState`, `stateNeedsYou`, `CURRENT_SUBAGENT_STATES` on it; mobile adopts it in a follow-up PR (outside this slice). Update `docs/product/subsystems.md` shared-derivation row.

Size: about 60 lines deleted across web, 25 added shared. Risk: low (pure functions with exhaustive table test).

### web-shell-widgets-7 (medium): keyboard-stepping helpers copy-pasted across widgets and cycle code

Locations: `widgets/menu/index.tsx:130`, `widgets/radiogroup/index.tsx:58` (comment: "mirrors Menu's own stepEnabledIndex"), `widgets/segmentedcontrol/index.tsx:65`, each with its own first/last-enabled helpers (`segmentedcontrol/index.tsx:48-63`); `widgets/pathfield/index.tsx:140` and `widgets/modelCatalog/index.tsx:109` (`rowDomId`, byte-identical); ArrowDown/ArrowUp/Home/End switch in `pathfield/index.tsx:419-448` and `modelCatalog/index.tsx:213-250` (same four cases and guard); modulo cycling in `shell/sessionCycle.ts:27-31`, `shell/rail/liveSessionCycle.ts:21-25`, `shell/rail/needsYouCycle.ts:14-22`.

Evidence: three `stepEnabledIndex` with subtly different no-match returns (`-1` in segmentedcontrol, `from` in menu and radiogroup). Five hand-rolled combobox/listbox implementations exist (palette `CommandPalette.tsx:801`, pathfield, modelCatalog, `Composer.tsx:1700`, `Spawn.tsx:2881`), each with its own `aria-activedescendant` bookkeeping and `scrollIntoView` effect (`pathfield:348-351`, `modelCatalog:199-204`).

Impact: the differing edge-case returns are latent bugs; every a11y fix to listbox behavior needs five edits.

Fix plan: add `widgets/internal/rovingIndex.ts` exporting `stepEnabled(items, isEnabled, from, delta)`, `firstEnabled`, `lastEnabled`, `cycle(length, index, delta)` and a `listboxKeyHandler` for the clamp-style Arrow/Home/End case; migrate widgets one PR at a time. Pin the no-match return in a test first. Out of scope here but follow-up: palette and the two slash menus onto the same helper.

Size: about 120 lines deleted, 50 added. Risk: low.

### web-shell-widgets-8 (medium): four shipped widgets have no product consumer

Locations: `widgets/table/` (`index.tsx` 160 + css 104 + test 152), `widgets/insightcard/` (143 + 85 + 100), `widgets/inspectorcard/` (70 + 50 + 74), `widgets/recommendationcard/` (111 + 76 + 92), plus `dev/gallery-sections/{table,insightcard,inspectorcard,recommendationcard}.tsx` and rows in `docs/web-ui/design-system.md:518-522`.

Evidence: for each name, the only non-test importers are `widgets/index.ts` and `src/dev/gallery-sections/*`. The doc says "Ported from Beautiful UI's ..." for each. `Table` is also referenced from `difftable/index.tsx` (comment-level; DiffTable has its own markup).

Impact: about 480 lines of widget code, 315 of CSS, 420 of tests, three gallery sections and doc rows carry a maintenance and token-contract cost for no screen.

Fix plan: needs Jesse's decision (widget API is locked by AGENTS.md). Recommended: delete all four, their barrel exports, gallery sections and doc rows; re-add from git when a screen needs one (YAGNI). If any is kept, say which feature is next to use it.

Size: about 1,300 lines deleted including tests and gallery. Risk: low (no consumers; typecheck proves it).

### web-shell-widgets-9 (medium): narrating review-round, ticket and "port of legacy JS" comments, some citing files that no longer exist

Locations (examples): `shell/AppShell.tsx:181, 605, 644, 651, 696, 754` (13 lines), `widgets/tree/index.tsx:190-195` (8), `shell/rail/Rail.tsx:981-1004, 1615-1621`, `shell/rail/RailRow.tsx:403`, `shell/DockHost.tsx:93, 200, 211`, `shell/cheatsheet/CheatsheetOverlay.tsx:98`; legacy-port citations in `shell/palette/{CommandPalette,commands,search,commandScore,paletteContext,paletteController,recentCommands,blocked,mode}.ts*`, `notifications/{channels,favicon,leader}.ts`, `widgets/textarea/index.tsx:23-24,80,90` (cites `cmd/evener-hub/assets/renderer.js:6307-6314`); stale sizes `shell/rail/railHostChunk.ts:3`, `rail/index.tsx:22`, `rail/index.test.tsx:15` ("1605-line Rail", file is 2,133).

Evidence (counts in this slice, non-test): 54 lines citing roborev/PR numbers/review rounds/"Component NN"/"finding N" across 22 files; 14 citing "kata xxxx" ids; 29 lines citing `search.js:NNN`, `notifications.js:NNN` or `renderer.js:NNN`, plus 13 in tests. `cmd/evener-hub/assets/` now holds only icons and a manifest, so none of those line references resolve. `docs/web-ui/design-system.md:488` says `widgets/internal/` is "currently just `requireClass`" while it holds `documentRealm.ts`, `useOwnedDocument.ts`, `viewportWatch.ts`, `fieldTrigger.module.css` and test helpers. Style rule: history belongs in commit messages.

Impact: noise that hides the real invariants, misleads readers hunting for the "legacy" source, and rots (the "1605-line" claims are already wrong).

Fix plan: mechanical comment-only PRs per directory. Keep the invariant each comment protects, drop the provenance (round number, PR, source-file line). Where a comment says "matches the legacy exactly" and the legacy no longer exists, state the rule itself. Fix the two doc-drift lines (design-system.md:488, size claims). No behavior change; `make lint` is the gate.

Size: about 250 comment lines deleted or shortened. Risk: very low.

### web-shell-widgets-10 (medium): 1,300 lines of `requireClass(styles.x, "file.module.css", "x")` boilerplate

Locations: 194 files; example block `shell/rail/Rail.tsx:92-105`, `widgets/pathfield/index.tsx:24-52`, `shell/palette/CommandPalette.tsx:72-95`; helper `widgets/internal/requireClass.ts`; contract test `styles/requireclass-contract.test.ts` (397 lines).

Evidence: 1,297 non-test call sites with `styles.` argument. Every call states the class name twice and the module filename once more as a string. The file label can drift from the import (nothing checks it), and the whole block exists only to satisfy `noUncheckedIndexedAccess` on CSS module types (`requireClass.ts` header).

Impact: about 1,300 lines of typing-only code, plus a 397-line test whose purpose is policing the convention.

Fix plan: replace with one call per module, `const CLASS = cssClasses(styles, "Rail.module.css", ["rail", "header", ...] as const)` returning `Record<Name, string>` and throwing listing all missing names, or generate `.module.css.d.ts` typings so plain `styles.rail` type-checks and the runtime check becomes one contract test. Migrate with a codemod, directory by directory; the contract test is rewritten once. Raise with Jesse first (convention is documented in design-system.md).

Size: about 1,000 lines net deleted. Risk: low; codemod plus typecheck.

### web-shell-widgets-11 (medium): design-system bypasses inside the shell

Locations: `shell/statusbar/StatusBar.tsx:52` (raw `<button>` styled as `.chip` with glyph, name, count), `shell/rail/RailRow.tsx:572` (raw `<button>` in `SessionTitle`), `shell/palette/CommandPalette.tsx:786` (back pill button), `:791` (raw `<input>` combobox), `:1064` (raw row `<button>`), `shell/holdhints/holdhints.module.css` (`.chip`), `shell/rail/RailHost.module.css` (`.chipBar`), `shell/rail/RailRow.module.css` (`.chevronButton`, `.contextCard`), `shell/activitybar/activitybar.module.css` (`.foldButton`), `shell/activitybar/AgentsTab.tsx:62-66`.

Evidence: ds-audit output above; design-system.md rule 1 forbids exactly these. `AgentsTab` hand-rolls an "Inactive subagents" fold with `Button variant="quiet"` plus `Chevron` plus `toggleDisclosure`, while `JobsTab.tsx:56` uses the `Disclosure` widget for the same "completed items" fold.

Impact: styling drift from the system and inconsistent folds between sibling tabs.

Fix plan: per site follow the "when no widget fits" ladder: StatusBar chips become `Chip` (or `Button size="sm"` with a documented slot) if a widget can carry glyph+name+count, else a documented bespoke exception with a comment; `AgentsTab` fold moves to `Disclosure` (matching Jobs); palette back pill uses `Chip`/`Button`. Anything needing a widget prop goes to Jesse first (AGENTS.md). Lower `ds-audit` counts and update `docs/web-ui/widget-adoption-catalog-2026-10-11.md`.

Size: about 120 lines changed. Risk: low-medium (visual change, check both themes).

### web-shell-widgets-12 (medium): needsYouActions reads the same "next ref" three times and modal detection is implemented five ways

Locations: `shell/needsYouActions.ts:168-170` (`read`), `:407-410` (`runNext`), `:430-433` (post-demand recompute); modal detection at `keybindings/dispatcher.ts:109`, `shell/needsYouActions.ts:145, 236, 255`, `shell/AppShell.tsx:748`, `widgets/focusscope/rehome.ts:69`, `shell/installKeybindings.ts:5-6`.

Evidence: the identical three lines `nextNeedsYouRef(needsYouRefs(rows), focusedSessionRef()); if (remaining > 0 && focusedSessionRef() === refs.at(-1)) nextRef = null;` appear three times. `needsYouRefs` also accepts `unknown` and a `{needs_you: [...]}` object (`rail/needsYouCycle.ts:5-11`) though its only caller (`needsYouActions.ts`) always passes an array. `aria-modal` is queried or observed in five different ways, one of which is a 70-line MutationObserver with realm-aware multi-document tracking local to this controller.

Impact: guard drift (AppShell checks only `document`, the controller checks the focused popout document, the dispatcher checks the event target ancestor).

Fix plan: add `nextNeedsYou(rows, remaining, focusedRef)` returning the ref or null and call it three times; tighten `needsYouRefs` to `readonly NavigationSessionSummary[]` and delete the legacy-shape branch; extract the modal tracker into `shell/modalOpen.ts` (`isModalOpenIn(doc)`, `createModalTracker(documents)`) and make AppShell's guard, the dispatcher default, and `rehome.ts` use `isModalOpenIn`. Existing `needsYouActions.test.ts` is the guard.

Size: about 70 lines deleted, 40 added. Risk: low-medium.

### web-shell-widgets-13 (low): dead exports and compatibility seams

Locations: `shell/rail/railNodes.ts:303` (`subagentChildrenOf`, zero references anywhere), `widgets/disclosure/disclosureStore.ts:93` (`clearDisclosureScope`, zero references), `notifications/attention.ts:54-62` (`snapshotFromTree`, "Compatibility seam for the notification owner during the migration", zero references), `shell/rail/needsYouCycle.ts:24` (`openNeedsYouSession`, a one-line pass-through to `openAttentionSession`, referenced only by its own test and a comment at `AppShell.tsx:786`), `shell/focusedSession.ts:52-55` (`resetFocusedActivityScopeForTests(): void {}`, an empty function imported by 3 tests), `shell/rail/Rail.tsx` `adaptNavigationResources` (test-only), `shell/useIsMobile.ts:60` (`subscribeMobileViewport` wraps `subscribe`; used once from `stores/transcriptDisplay.ts`, which is fine, but the wrapper is a pure alias).

Evidence: grep across `src` for each identifier (results above). The deadexports sweep over the slice's exports also lists many type-only exports with no outside user (for example `ChunkRetryOptions`, `StackHostProps`, `RailRowProps`); those are cheap and not worth a PR.

Fix plan: delete each (and the empty reset plus its three call lines, and the test of `openNeedsYouSession`), run typecheck. No migration needed; none of these have callers. No new compat added.

Size: about 70 lines deleted. Risk: very low.

### web-shell-widgets-14 (low): SessionMenu takes the same model through two props

Locations: `shell/sessionMenu/SessionMenu.tsx:81-83, 109, 142`, `shell/rail/RailRow.tsx:367`, `panes/session/chrome/SessionChrome.tsx:341`, `shell/sessionMenu/SessionMenu.edge.test.tsx:90`.

Evidence: `treeNode?: NavigationSessionModel` is documented as "Compatibility input for rail rows; the pane chrome uses `session`", resolved by `const sessionModel = session ?? treeNode`. The rail row is the only `treeNode` caller (plus one edge test).

Fix plan: change `RailRow.tsx:367` to `session={session}`, delete `treeNode` and the `??`, update the edge test. Size: about 8 lines. Risk: very low.

### web-shell-widgets-15 (low): transcript render context accepts redundant aliases

Locations: `transcriptDisplay/renderContext.tsx:25, 38-40, 83, 120-125, 160-172` and `:262-269`.

Evidence: `TranscriptRenderContextValue` stores `metadata` and `projectedMetadata` set to the same object (`:125`); no production file reads `projectedMetadata` (only `metadata` is destructured, in `SystemNoticeItem.tsx:436`, `TurnSeparator.tsx:51`). Input accepts `metadata ?? projectedMetadata ?? projection?.metadata` and `eligibleDisclosureIds ?? projection?.eligibleDisclosureIds`; the only product caller (`panes/session/transcript/TranscriptBody.tsx:566-576`) passes `projection` and also `eligibleDisclosureIds`. `value` and `metadata` props are test-only. `useOptionalTranscriptRenderContext` is documented "Test/transition hook" yet is used in production by `fileOpenBeside.tsx:68` and `shellTool.tsx:115`, and `useTranscriptRenderContext` silently falls back to `DEFAULT_CONTEXT` "for direct leaf tests", which hides a missing provider in production.

Fix plan: drop `projectedMetadata` from value and input; take `projection` as the single source (tests pass a small projection); remove the redundant `eligibleDisclosureIds` prop from `TranscriptBody`; rename the optional hook for what it is, or make the strict hook throw and have tests wrap in a provider. Size: about 40 lines. Risk: low-medium (many leaf tests render without a provider; use a test helper).

### web-shell-widgets-16 (medium): tests assert on rendered copy against repo rules

Locations: `shell/DockRegion.test.tsx:122, 214, 233, 237`; `shell/ConnectionBanner.test.tsx:143, 145, 461` (5 uses of "Connection closed."); `widgets/emptystate/emptystate.test.tsx:13, 18, 46, 47, 71, 72`; `widgets/collectioneditor/collectioneditor.test.tsx` (7); `shell/rail/Rail.test.tsx` (15), `shell/rail/RailRow.test.tsx` (15), `shell/activitybar/JobsWatchesTabs.test.tsx` (7); policy `docs/developing-evener/testing.md:62-87` ("Copy Is Not a Test Oracle").

Evidence: 205 `expect(...getByText/findByText("<12+ char literal>"))` assertions in shell and widgets tests (771 and 252 `*ByText` calls overall, many used only to locate). `emptystate.test.tsx` passes a title prop and asserts the same string comes out, which the policy names as proving nothing. These counts are from grep only; I did not classify each as locator versus oracle.

Impact: tests break on wording changes and do not prove behavior.

Fix plan: triage by file: replace oracle assertions with role/state assertions (`role="alert"`, disabled, callback called, focus), keep `ByText` only as a locator; for pure prop-echo tests delete. Do it as the files are touched, starting with `DockRegion`, `ConnectionBanner` and `emptystate`. Size: about 150-250 assertion lines changed. Risk: low; coverage must not drop (replace, do not remove).

### web-shell-widgets-17 (medium): Jobs/Watches/Agents activity tabs repeat one scaffold

Locations: `shell/activitybar/JobsTab.tsx:19-77`, `WatchesTab.tsx:17-51`, `AgentsTab.tsx:31-115`, `TasksTab.tsx`; also misplaced `import { ActivityPageBoundary }` above each file's header comment (`JobsTab.tsx:1`, `WatchesTab.tsx:1`, `AgentsTab.tsx:1`).

Evidence: each tab does `useSessionActivity(...)`, `useActivityScrollProgress(ids, complete, hasMore)`, then the same ladder (`permanent && empty` -> unavailable `EmptyState`; `!collection || (empty && !complete)` -> `Skeleton lines={2}`; `empty && complete` -> "No X at this level"), the same `ActivityPageBoundary` prop list (8 props), and the same `collection.error && !collection.permanent ? <InlineNotice live="off">X are updating…` footer. Only the row renderer differs.

Fix plan: add `ActivityCollectionTab` (or a hook returning the three early-return nodes plus boundary props) in `shell/activitybar/`, used by the three tabs; fold remains tab-specific. Fix import placement. Existing `JobsWatchesTabs.test.tsx`, `ActivityPageBoundary.test.tsx` guard it. Size: about 70 lines deleted, 40 added. Risk: low.

### web-shell-widgets-18 (low-medium): legacy persisted-layout migration has no sunset

Locations: `shell/workspace.ts:357-373 (readPanelParams transcript branch), 375-450 (migrateLegacyLayout), 453-490 (legacyDuplicatePanels), 694-800 and 852 (call sites and comments)`; `shell/sessionPlacement.ts:32`; `shell/DockHost.tsx:630`.

Evidence: about 150 lines convert saved layouts that contain the retired read-only "transcript" pane and "legacy cascade" sources into session panes, lifting parent edges and deduplicating panels. Comments refer to "the conversation-view ruling" and "legacy transcript params". No removal condition is stated.

Impact: recovery of the user's saved layout is legitimate product behavior (AGENTS: preserve the user's work), but there should be a stated end. This is a decision for Jesse, not a unilateral deletion.

Fix plan: ask Jesse whether layouts older than the transcript-pane retirement still exist in the wild; if not, delete `migrateLegacyLayout`, `legacyDuplicatePanels`, the transcript branch in `readPanelParams` and tests, and keep the existing "skip an unloadable panel instead of discarding the workspace" rule (`readPanelParams` null return), which already covers unknown pane types. If kept, add a dated removal note to `docs/product/subsystems.md`.

Size: about 200 lines (with tests) if removed. Risk: medium (user layouts); needs approval.

### web-shell-widgets-19 (low): test-only seams exported from production modules

Locations: 20 `*ForTests` exports in slice production files, including `shell/dockHostChunk.ts:175,179`, `shell/rail/railHostChunk.ts:32,36`, `shell/DockRegion.tsx:90`, `shell/mobile/StackHost.tsx:169`, `notifications/index.ts:191` (prod module imports `resetNavigationStoreForTests` from `stores/navigation/store`), `notifications/leader.ts:48,52`, `shell/paneRegistry.ts:73`.

Evidence: `dockHostChunk.ts:199-210` branches at runtime on `dockHostImporterForTests === null`; `StackHost.tsx:169` is a setter for a module-level flag read in production code. 79 such exports exist repo-wide (excluding test-util files).

Impact: production control flow shaped by tests; the boundary-fake rule says fakes belong at the boundary, not inside the unit.

Fix plan: no sweep. When touching each module, prefer constructor/parameter injection (`createChunkRetryLoader(options, importer, retryImporter)` already does this for the rail) and move reset helpers into `*TestUtils.ts`. Fold into finding 4 for the dock loader. Size: small per module. Risk: low.

### web-shell-widgets-20 (low): environment-feature guards and layering around matchMedia

Locations: `shell/useIsMobile.ts:5-58`, `shell/holdhints/usePrefersReducedMotion.ts:11-30` (header calls itself "the one JS-side read" yet lives under holdhints), `stores/prefs.ts:417-440`, `widgets/hovercard/useFloatingLabel.ts:31`; `stores/transcriptDisplay.ts:20` imports `shell/useIsMobile` (store depends on shell).

Evidence: four hand-written `typeof window.matchMedia !== "function"` guards plus subscription code exist because jsdom lacks `matchMedia`. `useIsMobile.ts` is a 70-line singleton `MediaQueryList` manager.

Fix plan: add one `useMediaQuery(query)` / `readMediaQuery(query)` in `src/dom/mediaQuery.ts` (guard once), rebuild `useIsMobile` and `usePrefersReducedMotion` on it, and move `isMobileViewport` to that module so `stores/` does not import `shell/`. Size: about 60 lines deleted. Risk: low (tests stub `matchMedia`).

### web-shell-widgets-21 (low): minor token and icon drift

Locations: `styles/tokens.css:79,285` (`--tooltip-muted` declared in both themes, referenced nowhere), `:147` (`--space-9`, never referenced), `widgets/pathfield/index.tsx:144-170` (`FolderIcon`, 14x12 viewBox) vs `widgets/directorypicker/index.tsx:41` (`DirectoryIcon`, 16x16, different path) for the same folder glyph; 30+ files with inline `<svg>` icons and no shared icon module beyond `ToolIcon`, `Chevron`, `SendIcon`, `CloseIcon`.

Evidence: `ds-audit` unreferenced-token list plus grep. Remaining `ds-audit` literals (45 spacing, 29 radius, 14 duration) are repo-wide; none verified as slice-specific except those in widgets CSS.

Fix plan: delete unused tokens (and confirm the `--ansi-*` ones are consumed dynamically before touching them; they appear in `tokens.css:98-113` and `ansi.ts` builds names at runtime, so I left them out). Use `DirectoryIcon` in `PathField`. Size: about 25 lines. Risk: very low.

---

## Recommended fix sequence

1. Dead code and trivial aliases (findings 13, 14, 21): no behavior, unblocks later diffs.
2. Comment cleanup per directory plus doc-drift lines (finding 9): comment-only, reduces noise before moving code.
3. `dockHostChunk` onto `chunkRetry` (finding 4): isolated, covered by existing tests.
4. Shared state-family helper (finding 6) and shared page-order helper (first half of finding 1): small shared-client additions, tests first.
5. Palette legend from the registry (finding 5): small user-visible correctness fix.
6. Roving/listbox helper and widget migrations (finding 7), one widget per PR.
7. Activity tab scaffold (finding 17) and DS bypass fixes (finding 11), after checking widget-extension questions with Jesse.
8. Rail adapter: delete compat branch, extract to store module (finding 1), then split `NavigationRail` (finding 2).
9. Live-session controller and shared intent guard out of AppShell (finding 3) together with `needsYouActions` dedupe and modal tracker (finding 12).
10. Test cleanup (finding 16) alongside whatever file each earlier step touches; render-context aliases (finding 15) and matchMedia module (finding 20) opportunistically.
11. Decisions needed from Jesse before work: delete the four unused widgets (finding 8), `requireClass` replacement (finding 10), sunset of layout migration (finding 18).
