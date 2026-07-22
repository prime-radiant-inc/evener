# Final two-wave integration review — Waves 5 (interaction) + 7 (settings), + closes, wiring, main-absorb, absorb round

Reviewer: opus (final whole-branch coherence pass).
Branch: `worktree-webui-workspace-shell`, HEAD `2e2dccab5`. Range reviewed: `0f3bcaff2..HEAD`.
Scope: whole-branch coherence + the controller-authored (only-unreviewed) commits. The four streams
and their close/absorb rounds were already adversarially reviewed; this pass does not re-review them.

## VERDICT: READY FOR WAVE 6

- Code-defect findings: **0 Critical, 0 Important, 0 Minor.**
- Independent gates: **all six green.**
- Controller-authored commits (the only unreviewed work): **all correct.**
- Cross-wave seams: **all five coherent.**
- Stale committed prose: **7 statements** across the two wave reports (documentation only; not edited per brief).
- Punch-list triage: **1 must-ratify-@M9 · 5 schedule-W6 · 12 schedule-W8 · ~16 accept-permanently · 1 Go follow-up.**

---

## Duty 5 — Independent gate run (ALL GREEN)

Run from a clean tree at HEAD `2e2dccab5`.

| Gate | Command (cwd) | Result |
|---|---|---|
| tsc | `npx tsc --noEmit` (frontend) | EXIT 0 |
| vitest | `npx vitest run` (frontend) | EXIT 0 — **185 files / 2866 tests passed** (matches expected) |
| lint | `npm run lint` (frontend, biome ci) | EXIT 0 |
| build | `npm run build` (frontend) | EXIT 0; `dist/PLACEHOLDER` restored; **tree clean** after |
| go build | `go build ./...` (repo root) | EXIT 0 |
| make lint | `make lint` (repo root) | EXIT 0 (all modules; includes `lint-generated` + `secret-scan`) |

Doc currency: `make generate` (= `go generate ./appwire/...`) produces **zero diff** on
`docs/appwire-protocol.md` — the regenerated protocol doc is current. (`make lint`'s `lint-generated`
target independently gates this and passed.)

Note: `dist/{index.html,webassets}` are gitignored; only `dist/PLACEHOLDER` is tracked. `vite build`
deletes PLACEHOLDER each run; restoring it returns the tree to clean, as the reports claim.

---

## Duty 1 — Controller-authored commit audit (the only unreviewed changes)

**All clean.**

### `e7243bd71` — main-absorb conflict resolution (appwire catalog unions + regenerated doc)
Merge of branch parent `595d01616` × main parent `1b5c58111`. Three conflict files, all resolved as
**correct unions**:
- `appwire/protocol.go` (Notifications slice), `appwire/types.go` (const block + struct), and
  `docs/appwire-protocol.md` each preserve **both** sides' entries: `NotifySerfSandboxEscalationResolved`
  (from the main/wire-honesty side) **and** `NotifySerfTreeChanged` (from the branch side). Combined-diff
  columns confirm provenance (col-1 add = branch-added-vs-main, col-2 add = main-added-vs-branch).
- No duplication: each const appears exactly once (types.go:126/131, protocol.go:187/188); the
  `SandboxEscalationResolved` payload struct is present (types.go:389).
- Doc regenerated correctly: `make generate` → zero diff.

### `388129621` — SA5011 guard proof
Adds `return` after `t.Fatalf(...)` in the nil-guard of `web_api_tree_test.go`. This is the standard
staticcheck-appeasement pattern (`t.Fatalf` calls `runtime.Goexit`, which the analyzer doesn't model);
**no behavior change**. Correct.

### `2789b561f` — requireclass comment reword
Rewords a Composer.tsx comment example from `styles.foo` to "bare direct module references" so the
requireclass guard's deliberately comment-blind textual scan stops false-matching. Meaning preserved;
the scanner stays strict. Correct.

### `5722471dc` — helpers.ts comment tighten (post-`3024b6b97`, A1 review minor)
Changes "the ONE wire field still dropped is ThreadItem.raw" → "raw is among the wire fields dropped."
Accuracy improvement (`wireItemToModel` drops several fields). Correct.

### Merge commits `e9b6b5395` / `087c0fd1f` / `3024b6b97` / `2e2dccab5` — collision surfaces
All four merges show **empty combined diffs** on the collision surfaces (git auto-unioned; no manual
conflict resolution was needed). Verified the final files are **supersets of both parents** (no silent
drops from either side):
- `src/widgets/index.ts` barrel — W5's `Dropzone`/`Textarea` **and** W7's `CollectionEditor`/
  `ConfirmDialog`/`FormRow`/`PathPicker`/`RadioGroup` all present, biome-ordered. Superset check at the
  W7 merge: **zero parent lines missing** from either side.
- `src/shell/routing.ts` — W5 routes (`/s/`, `/thread/`) **and** W7 routes (`/settings`,
  `/credentials`, `/settings/:section`) all present; superset check zero-missing.
- `src/shell/paneRegistry.ts` — `PaneTypeId` union carries both `session`/`transcript` and `settings`;
  `AppShell.tsx` imports welcome+session+settings for self-registration.
- `package.json` — coherent, all deps present, no markers.
- `src/styles/tokens.css` — shared design-system foundation, unmodified/coherent.

---

## Duty 2 — Cross-wave seam coherence (ALL FIVE COHERENT)

1. **Prefs store = single source for Display + Composer enter-to-send.** One key
   (`serf.prefs.enterToSend`), one encoding (`readBool`→`raw==="1"`; `writeBool`→`"1"/"0"`, `prefs.ts:143-152`),
   live sync: Display (`display.tsx:24`) and Composer both read the same zustand store — Composer at
   render (`Composer.tsx:179`, drives kbd hints) and a fresh `prefsStore.getState().enterToSend` at submit
   (`Composer.tsx:477`, avoids the stale closure). A2's unification is genuine; toggling Display
   re-renders Composer reactively. `readBool` is careful (`"1"`→true, `"0"`→false, else fallback — never
   coerces). The A2 `'0'`-test vacuity was honestly scoped (comment "not branch-reached"; adjudicated).

2. **Requireclass contract guard over the whole union — RAN.** `src/styles/requireclass-contract.test.ts`
   passes (174 tests). The one union false-positive (W5's `styles.foo` comment) was fixed by `2789b561f`;
   the guard's comment-blindness is intact.

3. **AppShell module-eval `initPrefs` + prefers-color-scheme listener — no double-listener, no
   test-order landmine.** `ensureSystemSchemeListener` is the sole install site, guarded by the
   module-level `systemSchemeListenerInstalled` flag (`prefs.ts:254-259`) → single listener, single named
   handler. The store-creator's install (via `loadInitialState`→`applyTheme`) and AppShell's `initPrefs()`
   (module-eval, `AppShell.tsx:28`) dedupe against the same flag. `resetPrefsStoreForTests` resets the flag
   (`prefs.ts:440`); `prefs.test.ts` has `beforeEach`→reset and `afterEach`→`restoreAllMocks`+`unstubAllGlobals`;
   vitest default isolation prevents cross-file module-state leakage.

4. **Settings dispatch map completeness vs section registry.** `SECTION_COMPONENTS` (Settings.tsx) has
   17 entries; `SETTINGS_SECTIONS` (sections.ts) has 16 nav sections. **Every** nav section resolves to a
   real component (none falls through to `PlaceholderSection`); the single extra dispatch key is `project`
   (intentionally nav-less, documented, reached via `/settings/project?cwd=`). `DEFAULT_SECTION_ID`
   (`general`) is valid. `/settings/providers` correctly falls to Placeholder (documented W7 Minor #1).

5. **ItemModel.error/exitCode consistency.** `reducer.ts:114-115` maps both `error` and `exitCode` from
   the wire. `exitCode` is rendered by `shellTool.tsx` with correct discipline (`item.exitCode ??
   parseShellExitCode(...)`, `??` not `||` so a real 0 stays 0; `autoExpand` = `!== undefined && !== 0`).
   `error` is **uniformly unrendered** by every tool descriptor: the only `item.error` reads outside the
   mapping are the ask-gate **logic** (`deriveAskQuestions.ts:49`, the A1 HIGH fix) and `turn.error`
   (turn-level scroll). The default renderer uses `RawToolOutput` (renders `output`, never `error`).
   Nothing half-renders it — the deferral (surface denied/failed tool error text) is uniform and
   scheduled (see punch list).

---

## Duty 3 — Punch-list triage (deduplicated)

**Already RESOLVED on-branch by the absorb round (no longer open):** both wave-5 parity HIGHs — denied/
errored ask_user renders answerable (A1: `deriveAskQuestions` `item.error===undefined` gate) and
escalation-resolve Conflict-terminal (A1: `resolveEscalation`→`mapConflict`); plus the
`serf/sandbox/escalation/resolved` multi-client reducer case (reducer.ts:628, both primary+watched).

### must-fix / must-ratify before M9 — 1
- **Ask_user transcript re-architecture is unratified** (no `[data-ask-anchor]`, no `.ask-settled-line`,
  dock not `form`-owned). A documented wave-4 structural choice; the parity sweep flags M9/M10 as the
  decision point. Not a bug — a **ratification gate**. Nothing else is a hard pre-M9 code blocker (the two
  HIGHs are fixed; the queue-under-load live journey needs W6 hub-spawn, which is a W6 dependency).

### schedule-W6 (spawn / palette / notifications / theme / sidebarMode / hub-spawn) — 5
- `/` command palette not implemented (explicitly W6).
- `serf.prefs.sidebarMode` persists but is **inert** (no consumer; the real collapse is
  `serf.rail.collapsed.v1`) — W6 owns the consumer.
- OS-notification "asks" `loudScope` not ported — W6 notifications surface.
- **Notifications default cross-wave disagreement** (W7 defaults all four OFF; the runtime notification
  engine's v3 migration defaults title/favicon TRUE on the same `serf.prefs.*` keys). Latent — no consumer
  reads these keys on-branch — but a real trap to reconcile when the engine is wired in W6.
- Instance-CRUD cross-client live-update: **already fixed on `main`** (`28e2b2141`, reuses
  `serf/auth/updated` BroadcastAll, which `credentialsStore` already subscribes to) but **main-only, not
  yet on this branch** — arrives with the next main re-absorb; no branch work needed.

### schedule-W8 (periphery + model-picker catalog per Jesse) — 12
- **Model-picker catalog** restore (Jesse: RESTORE IN W8; interim plain `provider/model` input blessed).
- `ItemModel.error` **text unsurfaced** by any tool descriptor (a denied shell's error message is
  invisible) — real parity gap, A1-disclosed follow-up.
- Optimistic-pending chips absent for send/steer/drain (only queue renders a chip).
- Session-chrome polish cluster: model-switch trigger not busy-gated; model picker not
  Escape/outside-click dismissable; `DEFAULT_EFFORT_LEVELS` fallback dropped; location cluster
  (branch/worktree/cwd) absent.
- `showCost` pref persists but has **no consumer** (no cost display reads it) — wire when a cost surface exists.
- W7 settings polish: dir-list "N entries" count header (§13/§14); `withBusy` on non-destructive per-row
  buttons (Marketplaces Refresh, Installed Enable/Disable/Auto-upgrade/Upgrade — double-click double-fire);
  Installed plugin status dot; `/settings/providers`→`/credentials` redirect (stale bookmarks only);
  per-project `?cwd=` page renders inside the settings-nav shell (cosmetic).

### accept-permanently (conscious divergences / cosmetic) — ~16
Toasts-not-banners failure feedback; optimistic-pending failure → toast-and-remove; plain send now
optimistic (beyond parity); AppWire JSON-RPC not REST; ConfirmDialog everywhere (beyond parity); mobile
nav-as-page via React conditional render; read-only sections fetch `serf/settings/overview` not
server-HTML; **theme prefers-color-scheme listener KEPT** (Jesse); credentialsStore staleness extension
(stands as reviewed); launchConfigStore no-cross-client-live-update boundary; draft-preserve divergence
(Jesse-approved); queue-edit text-only; cancelled-tone neutral; and the LOW cosmetic cluster (pasted-image
name uses `File.name`, `📎` chip prefix dropped, no visible state word, `document.title` not updated, no
in-place "Allowed once"/"Denied" escalation state, provider tabs → flat Combobox, etc.).

### Go follow-up (separate track, for Jesse) — 1
- Projector hardcodes a completed tool item's `Status:"completed"` regardless of error
  (`internal/appprojector/appwire_projection.go:437`). Root cause behind the (now frontend-mitigated) ask
  gap; a wire-side terminal-error status would resolve the whole class. Not a frontend change.

---

## Duty 4 — Stale-statement sweep (reports only; NOT edited)

Committed prose superseded by later events. Listed for the controller; no files touched.

1. **wave7-report — theme prefers-color-scheme veto "open" (3 places):** Decisions §1 ("your veto is
   open"), the divergence ledger ("Jesse's veto open"), and close-fix item 6 ("Jesse's veto open"). Jesse
   **KEPT** the listener on 2026-07-21 (ledger line 191). The known example; the W7-close ledger already
   flagged it.
2. **wave7-report — model-picker cut "the gate should consciously bless this cut — if unintended it is a
   Major reduction":** Jesse **blessed** it, RESTORE IN W8 (ledger 206). Now decided, not an open gate.
3. **wave7-report — sidebar-mode "Same gate caveat as the model picker":** decided — consumer is W6.
4. **wave7-report — Decisions §2 credentialsStore extension "trivially revertable if the roster
   intentionally excluded it… flagged for your awareness":** no veto raised; **stands as reviewed** (ledger
   191/201). Now settled.
5. **wave5-report — Go follow-up §2 "the frontend absorb is pending" and "Next steps: the absorb
   roster":** the absorb round (A1/A2) has since **completed and merged** — the deferred work is done.
6. **wave5-report — the two parity-sweep HIGH gaps** (denied-ask answerable; escalation-resolve
   Conflict): described as open gaps "belongs with the absorb roster," but **both are now fixed** by A1.
7. **wave5-report — "next steps: the serial merges" (W5→W7):** executed (`e9b6b5395`/`087c0fd1f`).

**Verified NOT stale (checked because it looked stale):** wave7-report's live-proof "instance CRUD does
not broadcast" is **still accurate for this branch** — the backend broadcast (`28e2b2141`) is main-only,
not yet re-absorbed here.

---

## Finding counts

| Severity | Count |
|---|---|
| Critical | 0 |
| Important | 0 |
| Minor (code) | 0 |
| Stale prose (Duty 4, non-blocking) | 7 |

## Punch-list triage totals

| Tag | Count |
|---|---|
| must-fix / ratify @ M9 | 1 |
| schedule-W6 | 5 |
| schedule-W8 | 12 |
| accept-permanently | ~16 |
| Go follow-up (Jesse) | 1 |
| already-resolved on-branch | 3 |

The branch is coherent end-to-end: the only unreviewed (controller-authored) work is correct, the five
cross-wave seams hold, and all six gates are green. Ready for Wave 6.
