# Wave 8 — T4 (session chrome + optimistic-pending chips) — report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-chrome` off base `e3b9c188c`
**Commit range:** `e3b9c188c..f83a1b2dc` (5 commits)
**Manifest touched:** `panes/session/chrome/**` + `panes/session/pending/**` only. No chokepoint,
sibling-stream, `reducer.ts`, `model.ts`, `tokens.css`, or barrel edit. 11 files changed.

**Final gate (HEAD):** `tsc --noEmit` clean · `vitest run` 223 files / **3236** passed (baseline
**3217**, +19) · `biome ci` exit 0 · `build` exit 0, `dist/PLACEHOLDER` restored, tree clean.
No new test files were added (all tests landed in the 5 pre-existing test files, incl. the T1
`PendingChips.test.tsx` stub), so file count held at 223 while test count rose; `tsc` runs first at
every gate, so no file was silently parse-excluded.

## Commits (each RED-first, per-commit AND-chained gate, mutation-verified)

| SHA | Item | Triage/Dispatch |
|---|---|---|
| `5dfa17b59` | honest work-time for a zero-valued `activeTurnStartedAt` | Dispatch #1 (W6 punch) |
| `21df762ee` | reasoning-effort default fallback + none-vs-(default) | Dispatch #2 + triage #6 |
| `2b9d14d8d` | busy-gate model-switch trigger + Escape/outside-click dismiss | triage #4, #5 |
| `def2eef36` | fill `PendingChips` (send/steer/drain) | triage #3 |
| `f83a1b2dc` | palette `/tasks` trigger (`data-tasks-trigger`) | W6-T3 fold-in |

## Items delivered

### Dispatch #1 — honest work-time (the "495269h" bug). ROOT-CAUSED, not guessed.
**Trace (wire receipts):**
- `StatusRow.tsx:107` → `totalWorkMillis(model.workMillis, model.activeTurnStartedAt, now)`.
- `statusFormat.ts:49-52` (old) added `now - Date.parse(activeTurnStartedAt)` for ANY parseable anchor.
- `ThreadModel.activeTurnStartedAt` comes only from `hydrateThread` via `epochMsToISO(thread.serf.activeTurnStartedAt)` — `reducer.ts:262`. **No live push** (`model.ts:119-122`).
- `epochMsToISO` (`reducer.ts:78-80`) guards ONLY `undefined`: `ms === undefined ? undefined : new Date(ms).toISOString()`. A present **`0`** becomes `"1970-01-01T00:00:00.000Z"` (`Date.parse` → `0`), so `now - 0 ≈ now` ⇒ `formatWorkDuration(~1.78e12) = "495269h…"`.
- Wire field `SerfThread.ActiveTurnStartedAt int64` is `omitempty` (`appwire/types.go:256`; `types.gen.ts:660 activeTurnStartedAt?: number`) — a genuine `0` is normally omitted (→ `undefined` → safe), which is why the reducer's own test uses `1_000` (`reducer.test.ts:1886`). The defect is the `epochMsToISO` asymmetry (undefined guarded, `0`/negative/NaN not): any path that carries a present zero/near-epoch anchor surfaces the absurd clock.

**Fix (`statusFormat.ts`):** `totalWorkMillis` treats an anchor that does not parse to a positive finite
epoch-ms (`!Number.isFinite(startedMs) || startedMs <= 0`) as the wire's unset sentinel and returns the
banked `workMillis`. "No clock beats an absurd one." No magic threshold — a positive Unix-epoch-ms is
the definition of a real timestamp; `0`/negative (Go zero-time)/NaN are the "no value" encodings.

**RED evidence:** `statusFormat.test.ts` — `totalWorkMillis(45_000, new Date(0).toISOString(), 1_800_000_000_000)` returned `1806000045000`, expected `45_000` (FAIL) before fix.
**Wire-true fixture:** `StatusRow.test.tsx` runs the REAL `hydrateThread({thread:{…serf:{activeTurnStartedAt:0}}})`, asserts `model.activeTurnStartedAt === new Date(0).toISOString()` (proves the reducer surfaces the epoch sentinel), then asserts the row renders `"45s"`, never a `…h` clock.
**Mutation:** reverting the guard ⇒ all 3 nets bite (2 `statusFormat` unit + 1 `StatusRow` wire-true); restored to zero net diff.

### Dispatch #2 + triage #6 — reasoning-effort fallback + honest none-vs-(default). TRACED.
**Trace (the pin's "does the wire emit supportsReasoning:true with an empty ladder?"): YES.**
- Daemon `Profile.SupportsReasoning() = p.reasoning`, `ReasoningEffortLevels() = p.effortLevels` are **independent fields** (`agent/provider/profile.go:323,328-330`).
- Live `/models` enrichment sets them from **independent conditions**: `clone.reasoning = true` iff `info.SupportsReasoning` (`profile.go:454-456`), but `clone.effortLevels` only iff `len(info.ReasoningEffortLevels) > 0` (`profile.go:442-443`). So `reasoning=true` + empty `effortLevels` is reachable.
- Flows to the snapshot via `serve.go:590` → `SerfThread.{SupportsReasoning,ReasoningEffortLevels}` (`appwire/types.go:274-275`, both `omitempty`); reducer coerces the absent ladder to `[]` (`reducer.ts:263-264`).
- "none" semantics: serf's `"none"` **clears the effort to the provider default** (`llm/types.go:670` `req == "" || req == "none"` pass through; `providercfg/load.go:76` "serf's 'none' clears the effort"; `openaicompat/request.go:212`). Legacy conflates none↔default: `search.js:409-415` prepends `{id:"",label:"(default)"}` and omits `"none"`. `DEFAULT_EFFORT_LEVELS = ["minimal","low","medium","high"]` (`model-switch.js:30`, from `spawn.js:1605`).

**Fix (`StatusRow.tsx` `ReasoningEffortControl`):** effective ladder = model's own levels, else
`DEFAULT_EFFORT_LEVELS` when `supportsReasoning`, else none. Options prepend a real `{value:"",label:"(default)"}`
head; unset (`undefined`/`""`) **and** `"none"` normalize to the `""` selection — shown as `(default)`,
never the first ladder level (which a bare `value=""` select would display) and never a literal
`"none"`. A ladder that lists `"none"` collapses it into the single `(default)` entry. Superseded the
old comment's "no ambiguous third state / no fallback" reasoning (the pin's trace-first mandate).
The Go root of the `Status:"completed"`-on-error class is MW-A's, not T4's — untouched here.

**RED:** 5 `StatusRow.test.tsx` tests failed pre-fix (fallback ladder, unset→(default), collapses-none, plus 2 updated expectations).
**Mutation (3 nets):** removing the `DEFAULT_EFFORT_LEVELS` fallback ⇒ 2 bite; removing the `(default)` prepend ⇒ 5 bite; removing the ladder `"none"` filter ⇒ 1 bites. All restored clean.
**Honest caveat:** the current-value `!== "none"` normalization is a defensive companion to the filter — its DOM effect is masked in jsdom (an unmatched controlled-`<select>` value coerces to the first option there), so it is not independently mutation-observable; kept because it keeps a wire `reasoningEffort:"none"` pointed at a real option (no React controlled-value mismatch) and matches legacy `search.js`.

### triage #4 + #5 — busy-gate + dismiss (`ModelSwitch.tsx`).
- **Busy-gate:** trigger `disabled = busy || !changeModel` where `busy = isTurnActive(model.status.type, model.activeTurnId)` — the same predicate Composer's Stop/Steer uses (`isTurnActive`, `submitRouting.ts:47-49`: `statusType === "active" && !!activeTurnId`; Composer `Composer.tsx:233`). `openPicker` also early-returns when busy/incapable (defense in depth).
- **Dismiss:** `document`-level `keydown` (Escape) + `mousedown` (outside `pickerRef`) listeners when open — the `widgets/menu` idiom (`menu/index.tsx:193-202`). Two-stage Escape falls out for free: the Combobox consumes+stops Escape only while ITS popup is open (`combobox/index.tsx:147-157`), so first Escape closes the popup, second closes the picker. React 19 delegates to the root container, so a Combobox `stopPropagation` correctly shields the `document` listener.

**RED:** 3 `ModelSwitch.test.tsx` tests failed pre-fix (disabled-while-active, Escape-closes, outside-click-closes).
**Mutation (3 nets):** reverting the busy-gate / removing each listener ⇒ the matching test bites; restored clean.
**PIN-B:** T2's `ModelCatalog` is NOT in this base (sibling branch), so `ModelSwitch` keeps the existing `model/list` Combobox — the busy-gate/dismiss fixes stand alone (per PIN-B "not a hard dependency"); adopting `ModelCatalog` is the later one-liner.

### triage #3 — `PendingChips` (`panes/session/pending/**`).
Reads `usePendingTurnEntries(sessionRef)` (`pendingTurnsStore.ts:208`), filters `method !== "queue"`
(`PendingMethod = send|steer|queue|drain`, `pendingReconcile.ts:18`; `QueueStrip.tsx` owns `"queue"`),
renders one dimmed chip per entry with `queueEntryPreviewText(entry.text, entry.imageCount)`
(`queueDisplay.ts:26`). Adds **no store state**; reconcile + 10s reap stay in the store. New
`pendingchips.module.css` is tokens-only, dimmed (`opacity` not color — in-flight is not an
attention state), every class via `requireClass`.

**RED:** 5 tests failed against the T1 stub (chip render, method filter, ref filter, image placeholder, method label).
**Mutation:** dropping the `method !== "queue"` filter ⇒ the queue-exclusion test bites; restored clean.
**Presentation divergence (conscious, for close sweep):** chips render **beside the composer**, not
injected into the virtualized transcript — per the seam comment / plan (optimistic items in the
virtual list are beyond the MEDIUM bar; the legacy chip was a lightweight out-of-transcript indicator).

### W6-T3 fold-in — palette `/tasks` (`TasksPanel.tsx`).
Added `data-tasks-trigger=""` to the Tasks trigger button so the palette's "Toggle tasks panel"
(`clickTrigger("[data-tasks-trigger]")`, `commands.ts:474,99-101`) is no longer inert. `Button`
forwards `data-*` to the real `<button>` (`widgets/button` rest-spread).
**RED/Mutation:** the pin (`document.querySelector("[data-tasks-trigger]") === trigger`) failed pre-fix; removing the attribute ⇒ it bites; restored clean.

## Concerns / deferrals (for the wave close sweep)

1. **triage #7 — location cluster (branch/worktree/cwd): NEEDS_CONTEXT (controller-scheduled reducer/model change).**
   TRACED: `ThreadModel` (`model.ts:76-128`) carries **no** location fields. The wire `Thread` DOES:
   `cwd` (`types.gen.ts:771`), `gitInfo.branch` (`:777` → `GitInfo.branch :159-161`), `projectPath`
   (`:762`), `path` (`:770`) — but `hydrateThread` (`reducer.ts:232-266`) **drops all of them**. The
   sidebar's `branch` is unrelated: it comes from the REST `/api/tree` `TreeNode.branch` (`stores/tree.ts:38`),
   not the appwire ThreadModel. Rendering a location cluster in StatusRow therefore requires:
   - `model.ts`: add `cwd?`, `branch?`, `worktree?` to `ThreadModel` (out of T4's manifest);
   - `reducer.ts:262`-area: `cwd: thread.cwd`, `branch: thread.gitInfo?.branch`, `worktree: thread.projectPath` (worktree source is `projectPath` vs `path` — confirm at change time) — **`reducer.ts` is a standing off-limits chokepoint.**
   Per the binding constraint (the W6 search-ref precedent), this is a controller-scheduled main-writer
   task, not a T4 stream edit. Built nothing to avoid a half-wired cross-store read of only `branch`.

2. **triage #8 — `showCost` consumer: consciously deferred (no honest wire cost).**
   `showCost` is a real pinned pref (`prefs.ts:86`, default true, toggle in `display.tsx`) with no
   consumer. No thread-level cost crosses the wire: `EstimateCost` is Go-side and never wired; the only
   cost on the wire is per-`Turn` (`TurnModel.cost?: unknown` `model.ts:72`; `Turn.cost?: string`
   `types.gen.ts:1037`), and summing loaded turns under-counts (`StatusRow.tsx:8-16` documents this).
   Its proposed home (the #7 location/telemetry cluster) is itself deferred. Per the plan: "records it
   consciously-deferred if no honest cost number crosses the wire."

3. **Palette `/status` (`data-details-trigger`): consciously deferred (no dead affordance).**
   The React status row is **always-visible**; there is no toggle-able "session details" panel to open
   (unlike the legacy). Adding a `[data-details-trigger]` with no target would be a dead affordance.
   `clickTrigger` is a safe no-op when the selector is absent (`commands.ts:99-101`). Divergence for the
   close sweep: always-visible chrome vs the legacy details-toggle.

4. **Dispatch #3 honored:** no popout affordance wired anywhere (`popOutPane` never referenced; the
   runtime popout-URL gap is the fix round's).

5. **Multi-pane note (not T4-scoped):** the palette's `clickTrigger` is a global `document.querySelector`
   (first match). In multi-pane dockview multiple `[data-tasks-trigger]` could exist; precision is a
   `shell/palette` concern (out of manifest). Adding the attribute matches the legacy single-target
   contract and is strictly better than inert.
