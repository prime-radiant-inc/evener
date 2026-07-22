# Wave 6 — T3 (command palette) implementation report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w6-palette` (off `835c17e2b`)
**Commit range:** `835c17e2b..527f2143e` (4 commits)
**Manifest:** `cmd/serf-hub/frontend/src/shell/palette/**` (only)

## One-line test summary

Full suite green: **201 files / 2976 tests** (baseline 194/2898 → **+7 files, +78 tests**); tsc clean, `npm run lint` (Biome ci) exit 0, `npm run build` exit 0 (dist/PLACEHOLDER restored, tree clean), token-contract + requireclass-contract guards pass. Scope-gate net mutation-verified (`!ended`→`ended` ⇒ 2 failures; clean on revert).

## What landed (floor §2, 71 items)

Filled T1's `paletteController.ts` + `CommandPalette.tsx` seams; new sibling modules `mode.ts`, `blocked.ts`, `commandScore.ts`, `recentCommands.ts`, `search.ts`, `paletteContext.ts`, `commands.ts`, `commandpalette.module.css` (+ co-located tests).

- **§2.1 open/close** — the overlay is Dialog-based (keeps T1's "Command palette" accessible name + Escape-close). Added `openSeq` to `paletteStore` so every `openPalette()` remounts the body fresh (the atomic reset). `openPalette("/")` seeds command-filter immediately (the composer leading-`/` entry). Global wiring (⌘K / `[data-search-trigger]` / composer hook → `openPalette`) is T1's and is confirmed present in `AppShell.tsx:161-170,227` and `Composer.tsx:479`.
- **§2.2 mode machine** — `computeMode({query,hasSelectedCommand})`: command-args if a command is selected, else command-filter if input starts with `/`, else search. Recomputed per keystroke.
- **§2.3 search mode** — debounced 150 ms REST `GET /api/search?q=` (`credentials:"same-origin"`; **no appwire `search` method** — verified; wire shape pinned against `web_api.go` handleApiSearch + `web_types.go` searchResponse `{live,past}` of `{id,title,project,state,age}`, incl. Go's null-empty-slice encoding). Live / "Past · N" / "In session · N" sections, `<mark>` highlighting (structured `HighlightPart[]`, no `dangerouslySetInnerHTML` — React escapes), ↑/↓ wraparound, Enter/⌘Enter(new tab)/⇧Enter.
- **In-session search** — scans the focused session's `ThreadModel` (turns→items→text) via `threadsStore`, NOT the DOM (virtualized). ~40-char/side snippet with ellipses (`buildSnippet`). Turn label = 1-based model-turn index (plan-resolved approximation of the legacy per-message-element counter).
- **§2.4 command-filter** — the registry rebuilt fresh; scope gating (global always / ended-ok when a session pane is focused / session only when focused-and-not-ended); `commandScore`+`fuzzyScore` ported verbatim (hand-computed exact-value tests); Recent (≤5) from `localStorage["serf.search.recentCommands"]`, excluded from the main list.
- **§2.5 every command (23 real entries; /fork intentionally omitted)** — session commands are an EXACT 1:1 to the pinned Wave-5 `threadsStore` actions (`compact`/`interrupt`/`clearThread`/`shutdown`/`steer`/`queue`/`drainAsSteer(ref,"")`/`setModel`(split provider/model)/`setReasoningEffort`/`setGoal(trim)`/`forkFromTurn({aside:true})`). Nav (`/new`,`/spawn`,`/settings`,`/dashboard`) → `routing.navigate`. **`/theme` → `prefsStore.setTheme`** (the hazard-#1 FIX — visible immediately, verified via `data-theme`). `/upgrade` → `serf/upgrade`. `/project` → `railController.revealSessionInRail(ref)` (PIN-A; tested against a mock of the SEAM only). `/copy-id`, `/tasks`, `/status` (see concerns).
- **Idle guards** — `/interrupt`/`/steer`/`/queue`/`/drain-as-steer` return the floor's inline blocked sentinel (`"<verb> failed: no active turn"`) derived from `model.activeTurnId`, no wire call; `/model` blocks `"model change failed: turn in progress"` from `isSessionBusy` (status active AND turn id landed). Conflicts from the store actions surface as the error strip, never a blind retry.
- **§2.6 args mode** — enum (loading/loaded/empty/error states) + free (hint) with the pill + `×` back; Esc backs out (stopPropagation keeps it off OverlayPanel's close handler), does not close.
- **§2.7 execution/error** — `{paletteBlocked,message}` sentinel + inline `.palette-error` strip (rendered via the allowlisted `Chip tone="danger"` — see below), argless success records recency unless stayOpen, rejected Promise → `commandErrorMessage`.
- **§2.8 help** — the 7 fixed shortcut rows; `/help` and `/search` are stayOpen (no recency, no close).

## Concerns (for the controller / T6)

1. **Search-result activation uses a bare `session_id`, but the SPA opens sessions by a qualified `ref`.** `/api/search` returns `id = le.SessionID` (live) / `Meta.ID` (past) — bare ids. The new shell routes/opens sessions by the qualified `ref` (`TreeNode.ref`, a field DISTINCT from `TreeNode.session_id`; rail uses `node.session.ref`), and the spawn seam already established that `ParseRef` rejects a bare id. I navigate `/s/{encodeURIComponent(id)}` faithfully (matching the legacy + routing shape), but a live/past result may not resolve until the gap is closed. **Fix belongs outside T3's manifest** — most cleanly the Go search handler returning the qualified ref alongside/instead of the bare id, or the session route qualifying bare ids. I did NOT invent qualification (the seam note forbids the `local:` divergence). **T6 live-proof should verify search→open end-to-end.**

2. **`/tasks` and `/status` are currently inert.** The floor behavior is "synthesize a click on `[data-tasks-trigger]` / `[data-details-trigger]`". Those DOM triggers do not exist in the new shell — the session chrome drives its Tasks Sheet and status via pane-local React state (`TasksPanel`/`SessionChrome`, frozen Wave-5 files, not in my manifest). I ported the commands faithfully as no-op-safe `querySelector(...).click()` (exactly the legacy's `if (btn) btn.click()`), so they light up automatically once the chrome grows those attributes. Beyond-parity follow-up; flag in the sweep.

3. **Precise scroll-to-hit for in-session results is beyond-parity** (plan-acknowledged). Activating an in-session hit closes the palette + leaves focus on the already-focused session pane; the virtualized transcript has no scroll-to-item seam in my manifest. Flagged.

## Deliberate divergences / notes

- **Error strip via `Chip tone="danger"`**: the token-contract guard gates `--attention/--alive/--danger` to allowlisted widgets, and the palette isn't a widget. Rather than a neutral (dishonest) strip, the message renders through the allowlisted danger `Chip` inside a `role="alert"` wrapper; the palette CSS stays token-clean (accent is explicitly un-gated, used for the pill / `<mark>` / focus ring).
- **`.palette-error` persists until the next `openPalette()`** (§2.7's literal contract — cleared only on remount, not on typing).
- **`/copy-id` copies the qualified `ref`** (the pane's identifier in this UI); the legacy copied the bare session id. Minor.
- **Same-tab result open uses `navigate()`** (SPA route) vs the legacy's `window.location.href` full reload; ⌘Enter still `window.open(..., "_blank")`.
- **Command count is 23, not the floor prose's "22"** (8 global + 11 session + 4 ended-ok; the floor's §2.5 enumerates all 23 with /fork noted as omitted — an off-by-one in the prose, every listed command implemented).
- **Legacy §2.1 focus-trap suspension of `#tasks-panel`/`#details-panel`** has no successor — those legacy focus-trapped side panels don't exist; the Dialog's own FocusScope is self-contained. Not ported (N/A).

## Verification posture

Wire-true tests throughout: `commands.test.ts` drives real `threadsStore` actions over a `FakeClient` (asserting the exact wire method + params), real `prefsStore`/`workspaceStore`, seam-mocked `railController`. `search.test.ts` pins the REST call shape + fixtures against the Go handler. Component tests render the real overlay in jsdom (modes, search, args, help, error strip, keyboard nav). Live hub proof (fake-`$HOME` flock) is T6's wave-close responsibility per the plan.

---

## Review fixes (round 2)

Coordinator verdict "Needs fixes" — four items addressed as new RED-first commits on `w6-palette` (`e7a94d161..c6e0c92ad`). Full suite after: **201 files / 2985 tests** (+9 over the pre-fix 2976), tsc clean, `npm run lint` exit 0 (now **pristine — zero warnings**), build OK, placeholder restored.

- **I1 — help panel was sticky and leaky (floor §2.8), `81e685804`.** `showingHelp` never cleared on query change, and `view.items` stayed populated underneath the help panel, so `ArrowDown`+`Enter` fired the next registry command invisibly (e.g. `/upgrade`). Fix: `buildView` returns **empty rows while `showingHelp`** (nav inert, matching the legacy `items=[]`), and the input `onChange` clears `showingHelp` (typing returns a real list). Two RED tests written first (invisible-fire + sticky-help), both now green — the RED→GREEN transition is the mutation proof.
- **I2 — in-session search only read `item.text` (adjudicated: WIDEN), `5ec1d2f17`.** `findInSessionMatches` now scans everything the transcript renders: `item.text`, tool `item.output`, tool `item.error`, and each live `reasoningSummaries` entry (streamed chunks joined). Each matching source yields its own hit sharing the item's turn label. 4 RED fixtures (output / error / reasoning / text+output→2 hits) drove it.
- **I3 — consume the optional qualified `ref` (adjudicated), `c6e0c92ad`.** Added `ref?: string` to `SearchResult`; `activateResult` opens by `item.result.ref` when present, falling back to the bare-id URL when absent (old-hub tolerance). This is the client half of the main-side Go fix `e18f84cba` (adds a qualified `ref` to each `/api/search` hit) — **it resolves concern #1**: once that Go commit lands on this branch (next main→integration absorb), search→open works end-to-end; until then, old-hub fallback keeps behavior identical to before. Both REST shapes tested (with/without `ref`) + the navigation-by-ref-vs-id component tests.
- **Minor — pristine lint, `c6e0c92ad`.** Removed two stale/unused `useExhaustiveDependencies` suppressions (`ctx` mount-once memo; the active-row reset effect) that biome flagged as `suppressions/unused`; folded their rationale into plain comments. Biome ci now emits zero warnings.

**Concern status after round 2:** #1 (search-result ref) is **client-side resolved** (I3), pending the `e18f84cba` absorb — T6 live-proof should still confirm end-to-end. #2 (`/tasks`/`/status` inert) and #3 (precise virtualized scroll-to-hit) stand as documented beyond-parity follow-ups.
