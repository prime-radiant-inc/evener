# Web Rewrite Wave 6 — Report (M6 surfaces layer)

Status: COMPLETE (pre-merge). T1 chokepoint, four parallel streams (T2 spawn, T3 palette/search,
T4 notifications, T5 display + rail host), each adversarially reviewed, a five-item fix round, an
absorb of settled main, and this close task (T6: four micro-items, parity sweep, live proof, gates,
report). Wave branch: `w6-surfaces`, HEAD before this task `5c3d8d66e`; this task adds the
micro-items commit (`a6debb8f0`) plus this report/artifacts commit on top. NOT yet merged to
integration — that is the controller's serial step (and the integration then re-absorbs main),
outside this task's scope.

## What shipped

- **T1 — surfaces chokepoint** (`51f67ebc9`..`5e8a9ebf7`; report `a0fdfdaa3`): the controller-owned
  seams every stream fills — `SpawnParams`/`SpawnResult` (the `SpawnResult` **carries the QUALIFIED
  `thread.serf.ref`**, a wire-truth ruling baked into the plan at `835c17e2b`: the legacy `local:`
  strip is dead-on-arrival because `ParseRef` rejects a bare id), `preflight.ts`, the AppShell spawn
  route + `openPalette` global wiring + `initNotifications()` boot call + the `RailHost` slot, and
  the Composer leading-`/` → palette entry.
- **T2 — spawn pane body** (merge `d8717b707`; report `.superpowers/sdd/w6-t2-report.md`):
  `panes/spawn/**` — the 6-field launch bar as **design-system FormRow widgets** (not legacy
  chip-pickers), the dir picker (recents-first-listing / 150 ms completions / browse-into-vs-accept
  / `..` / stale-requestID drop), sticky-defaults + prefill layering, stale-model classify/sweep,
  the schema-driven advanced panel (collect/precedence complete) with a resolve preview, prompt +
  image attachments (reusing the composer's `useAttachments`/`Dropzone`), preflight
  offer-create-vs-abort, `startThread` (appwire-native, qualified ref), `?dir=`/`?prompt=` prefill.
- **T3 — command palette** (merge `c7b9a3134`; report `.superpowers/sdd/w6-t3-report.md`):
  `shell/palette/**` — the Dialog overlay, the three-mode machine (search / command-filter /
  command-args), search mode (150 ms REST `GET /api/search?q=`, live/past/in-session sections,
  structured `<mark>` highlighting), the 23-command registry with scope gating + idle guards, the
  `{paletteBlocked,message}` error strip, and the `/theme` **hazard-#1 fix** (immediate apply, not a
  reload-only no-op). Fix round 2 folded in a sticky-help fix, an in-session-scan WIDEN (I2), and the
  client half of the qualified-ref search→open (I3).
- **T4 — notifications engine** (merge `59123dd35`; report `.superpowers/sdd/w6-t4-report.md`):
  `notifications/**` — pure transition detection off `treeStore.tree.needs_you[]`, the title-count /
  favicon-dot / OS-notification / sound channels (pinned colors, 800 Hz/0.1/120 ms tone), Web-Locks-
  only leader election, edge-fire gating with a silent reconnect re-baseline, and the **all-OFF
  defaults** pin (the legacy `notifications.js:31` title/favicon-TRUE default is deliberately NOT
  ported — code-wins pre-adjudication).
- **T5 — display application + rail host** (merge `4bab9ca27`; report
  `.superpowers/sdd/w6-t5-report.md`): `tokens.css` font-size/phone-density gates (ramp moved to
  `<body>`, scaled via `--font-scale`; density via `--density-scale` on `--line-height-body` gated
  `@media (max-width:900px)`) and the `sidebarMode` consumer — `RailHost` + `useSidebarMode` +
  `railController` reveal seam, the ⌘B rail→pane→auto cycle, and the ☰ overlay drawer.
- **Fix round** (`ceb88f08c`..`2cc3ce89a`; report `.superpowers/sdd/w6-fixround-report.md`): the
  Sheet gained a **left** anchor and the rail drawer now opens from it (matching the top-left ☰
  chip); a ⌘B/Ctrl+B **editable-target guard**; a tightened `display-gates` density regex; the
  phone-density help copy corrected to the shipped 900 px gate; and stale `prefs.ts` rail-collapse
  comments reworded.
- **Absorb of settled main** (`5c3d8d66e`): the wave branch caught main up on the concurrently-landed
  hub work — the **search-ref** fix (`e18f84cba`, `/api/search` now returns a qualified `ref` per
  hit, completing the search→open path started client-side in I3), the **needs_you unification**
  (`fb171f89c`, tier-eligibility unified so `needs_you[]` can't drift from `AttentionSummary` — the
  exact set the notification engine reconstructs transitions from), **instance broadcasts**, and
  assorted hardenings. Go + frontend gates green after the absorb.

## Key stories

**The qualified-ref through-line.** One wire-truth ruling threads the whole wave. `ParseRef`
(`appwire/refs.go`) rejects a bare session id, so the legacy `local:`-strip is dead-on-arrival. T1
baked "`SpawnResult` carries the qualified ref" into the seam; T2's `startThread` keeps it; T3's
`/copy-id` copies it; and the **search→open** path was the one place it wasn't yet end-to-end — the
Go `/api/search` returned a bare `session_id`. That was closed in two halves: client-side in T3's fix
round (`ref?: string`, open-by-ref with old-hub fallback, I3), and Go-side on main (`e18f84cba`),
absorbed here at `5c3d8d66e`. Live proof confirmed the join: a search hit opened
`/s/local:033u6kjm…` (the qualified route), not a bare id.

**needs_you unification is what makes the notification engine correct.** The engine reconstructs the
legacy `into && !was` transition from snapshots of `treeStore.tree.needs_you[]`, trusting that array
to be exactly the daemon's tier-eligible attention population. Main's `fb171f89c` unified that
eligibility so the array can't drift from `AttentionSummary` (fork-superseded parents included) —
absorbed here, and the reason the live title-count/favicon-dot fired on precisely the right
transition.

**Hazard #1 was a decision, not a port.** The legacy palette `/theme` set dead `body.light-theme`
classes no CSS keyed off — a visual no-op until the next full reload. The rewrite consciously fixed
it (`prefsStore.setTheme` sets `data-theme` immediately), flagged as a divergence rather than
faithfully reproduced. Live proof: `/theme dark` flipped the whole UI on the spot and persisted
`serf.prefs.theme=dark`.

**The all-OFF defaults pin held.** The single most safety-critical decision of the wave — never
resurrect the legacy title/favicon-TRUE default — is verified three ways (unit mutation tests, the
settings UI showing all four opt-ins OFF, and the "1"/"0" encoding written only on explicit enable).

## Parity sweep

All **250 floor items** across the four sections of `docs/web-ui/parity/parity-m6-surfaces.md` were
swept via four parallel read-only verification packages, each row assigned MET / DIVERGED / GAP with
the citation **verified against the actual code** (not transcribed from the stream reports). Totals:

| Section | MET | DIVERGED | GAP | of |
|---|---|---|---|---|
| §1 Spawn | 59 | 42 | 8 | 109 |
| §2 Palette/Search | 57 | 11 | 3 | 71 |
| §3 Notifications | 22 | 19 | 0 | 41 |
| §4 Theme/Density/Font/Display | 21 | 6 | 2 | 29 |
| **Total** | **159** | **78** | **13** | **250** |

**Verdict.** A faithful, well-documented port. The 78 divergences are overwhelmingly one root cause
repeated — the htmx/DOM-event/server-section legacy plumbing replaced by React controlled inputs +
Zustand store subscriptions (all of §1.2, §1.15, most of §3.1/§3.8/§3.9) — plus the pre-ruled scope
divergences (below). No user-facing capability is lost in any divergence. All **13 gaps are minor or
low; none is a blocker.**

**GAP punch list (severity-ranked):**

- **§1.14 L186 (minor, most consequential)** — the spawn pane never resets after a successful spawn.
  The singleton pane persists (AppShell opens the session pane without closing spawn), so returning
  to it leaves an already-sent image + prompt staged and **re-sendable**. Fix belongs before merge or
  early in W8. (`Spawn.tsx`; no post-success form reset.)
- **§4.2 (low-med, cosmetic)** — **no pre-paint FOUC-avoidance successor.** Theme is applied by
  `initPrefs()` at bundle-eval (`AppShell.tsx:32`), not a synchronous inline `<head>` script, so a
  user on explicit `light` (or `system` on a light OS) can see a brief dark flash on every full page
  load until the module bundle runs (default `:root` is dark). Self-correcting, subset of users, but
  the legacy's canonical anti-FOUC pattern was dropped and the "before first paint" comment
  overclaims.
- **§4.8 (low)** — **`showCost` preference is inert.** Persisted + toggleable in Settings, but no code
  consumes it to gate cost display (`StatusRow`/`SystemNoticeItem`/`TurnSeparator` render cost
  unconditionally; there is no `body[data-show-cost]` successor). Defaults ON, so only an explicit
  OFF is a no-op; plausibly a deferred Wave-5-transcript concern.
- **§1 spawn cluster (minor)** — L45 prompt textarea not autofocused; L49+L154 `SafeEnv`
  env-fallback hint not implemented (two rows, one root cause: zero `envFallback` consumers); L52 no
  `/settings/launch` persistent-defaults link; L136 no synchronous pre-`model/list` drop of malformed
  (no-slash) models (async sweep only; bites migrated legacy localStorage); L140 the stale-model
  sweep can clobber a model picked during the in-flight `model/list` fetch; L188 failure message
  double-prefix ("Spawn failed:") lacks the already-prefixed guard.
- **§2 palette cluster (minor)** — live-result status dot does not pulse (`StatusDot` is static; live
  states still color-distinguished); no `scrollIntoView` on ↑/↓ (a long result list won't scroll the
  highlight into view; the rest of the keyboard-nav contract is intact); precise in-session
  scroll-to-hit + `.search-hit` flash deferred (ruled beyond-parity).

Note: the T2/T3/T4/T5 stream-report concern lists do NOT enumerate the §1 and §2 minor gaps above —
they are genuine small omissions beyond the sanctioned divergences, surfaced by this sweep's
code-level verification, not covered by any prior ruling.

## Divergence ledger (consciously-diverged, each with its ruling)

- **Qualified `thread.serf.ref`** (§1.14, §2.5 `/copy-id`, search→open) — wire-truth; `ParseRef`
  rejects a bare id, so the legacy `local:`-strip is dead-on-arrival. [T1 plan `835c17e2b`;
  `startThread.ts`]
- **Recent prompts DROPPED** (§1.1 `.RecentPrompts`) — Jesse 2026-07-22 "DROP THE ROW"; no UI, no
  storage built. [plan `d043a1b34`]
- **Chip chrome → design-system equivalence** (§1.2, 10/10 rows) — the 6 params render as inline
  FormRow widgets; openPicker dispatch, the three click-outside impls (hazards #3/#4), mobile
  bottom-sheet reparenting, and toggle-off-on-re-click are subsumed, not ported. [T2 concern #1]
- **Interim model Combobox; rich catalog = Wave 8** (§1.4 11/11, §1.5 5/5) — flat qualified-id
  combobox; the REST `/api/models` display_name/badges/grouping/Recent/pricing catalog is W8-deferred
  (the none-vs-(default) reasoning split IS preserved). [T2 concern #2, ledger-206]
- **Advanced-options control fidelity is interim** (§1.11) — modelPicker→plain text,
  multiline→single-line, scalar-only path validation; the pure collect/precedence logic (`schema.ts`)
  is complete. [T2 concern #5]
- **In-session search scan WIDENED beyond the floor** (§2.3) — scans `item.text` + tool output + tool
  error + reasoning summaries, not just text. [T3 fix I2 `5ec1d2f17`]
- **`/fork` intentionally omitted** (§2.5) — needs an edited message the palette can't collect
  (legacy `search.js:497-499`). [plan; T3]
- **`/tasks` & `/status` inert until the chrome grows the trigger attributes** (§2.5) — ported
  faithfully as no-op-safe `querySelector(...).click()`; light up automatically later. [T3 concern
  #2]
- **`/theme` immediate apply (hazard #1 FIX)** (§4.4) — chose immediate-effect over reproducing the
  reload-only no-op. [`commands.ts:209-210`]
- **All-OFF notification defaults** (§3.1) — title/favicon default FALSE, not the legacy TRUE
  (code-wins pre-adjudication); the safety-critical pin. [T4]
- **Web-Locks-only leader election** (§3.7) — no BroadcastChannel; the spec's "Web Locks +
  BroadcastChannel" resolved to Web-Locks-only. [plan spec-ambiguity resolution; hazard #2 KEPT]
- **Title base `"<pane> · serf hub"`** (§3.2) — focused-pane title, vs the legacy `"<section> ·
  serf hub"`; one honest divergence. [T4]
- **Right→left drawer resolution** (§4.6) — the Sheet gained a `left` side and the rail drawer opens
  from it, matching the top-left ☰ chip. [fix round item 1 `ceb88f08c`]
- **⌘B editable-target guard** — ⌘B/Ctrl+B skips when focus is in an editable, avoiding the
  emacs-style back-char collision. [fix round item 2 `b97e0c9d1`]
- **Scroll-to-hit deferred** (§2.3) — beyond-parity, plan-acknowledged. [T3 concern #3]

## Live proof

Real hub (built `serf-hub`, `SERF_HUB_WEB=new`) under an **isolated fake `$HOME`** (its own
`$HOME/.serf/hub.lock`, run dir, state root — the real host hub was never touched) on
`127.0.0.1:19286`, spawning real `serf serve` daemons via the built `serf` binary, driving the
cheapest real model **`openai/gpt-5-nano`** (materialized from the repo `.env`), through Chrome. No
mocks. Evidence: `.superpowers/sdd/w6-close-t6-evidence/` (8 screenshots). Never-echo-credentials
honored.

| # | Journey | Verdict | Evidence |
|---|---|---|---|
| 1 | **Full spawn** | **Pass** | The 6-field FormRow bar; the interim Model combobox filtered to flat **qualified** completions (`openai/gpt-5-nano`, no badges/pricing — the W8-interim shape); the dir picker showed 150 ms completions + `../` + browse-into `proj`; **Branch auto-resolved to `main`** via the REST HEAD lookup (§1.7 live); the advanced schema panel rendered (Agent/Model/Reasoning/Fast-cheap/Context-strategy…); an image attachment inserted the `[image 1]` marker + a `magenta.png ✕` chip; Spawn → a real `serf serve` daemon started and ran a real turn; the **image rendered as a thumbnail** ("Image 1 of 1", proving base64 reached the turn) and the agent ran the shell tool. `01/02` |
| 2 | **THE headline: queue / steer / edit / promote under load** | **Pass (strong)** | Against the **hub-spawned** session (which advertises the caps the wave-5 bare `serf serve` could not): typing under an active turn flipped the primary button **Send → Queue** and surfaced the Stop (■) + enabled Steer. Two messages queued; on turn completion they **drained FIFO** ("echo alpha", "echo beta"). On a fresh longer turn: **promote** ("Send now") pulled `QB` out of the queue and it ran ahead ("echo dos"); **edit** restored the entry's text to the composer and dequeued it (restore-then-cancel); **steer** injected mid-turn — the transcript shows the `STEER_INJECT` "You" turn, the agent ran "echo tres" → "Output: tres", and a "**Steering injected**" marker. The wave-5 close could not run this at all. `03/04` |
| 3 | **Command palette** | **Pass (strong)** | ⌘K open; `/theme` filtered the registry; args mode showed the dark/light enum with the "Switch theme ✕" pill; **`/theme dark` flipped the UI immediately** (`data-theme=dark` + persisted — the hazard-#1 fix); search returned **PAST · 1** + **IN SESSION · 1** with `<mark>` highlight + turn label; **search→open resolved to `/s/local:033u6kjm…`** (qualified ref, concern #1 end-to-end); the idle-guard **blocked sentinel** fired ("interrupt failed: no active turn", `role="alert"`); Escape closed. `05` |
| 4 | **Display** | **Pass (strong)** | Font size **XL** → `--font-scale 1.25`, `--font-size-body calc(14px * 1.25)`, all UI text visibly larger (the CSS cascade jsdom can't verify); at a ≤900 px width, density **Comfortable** → `--density-scale 1.25`, `--line-height-body calc(1.5 * 1.25)` (vertical rhythm, not type scale); theme flips persisted. `06` |
| 5 | **Rail** | **Pass** | All three `sidebarMode` values (auto / pane / rail-Collapsed); **⌘B cycled rail→pane→auto** (verified via `serf.prefs.sidebarMode`); the ☰ chip opened the **left-anchored** overlay drawer (fix-round item 1). `07` |
| 6 | **Notifications** | **Pass (core); OS/sound env-limited** | Settings showed **all four opt-ins OFF** (the all-OFF pin), enabling wrote the "1"/"0" encoding; a real **ask_user** rested the session in **needs_you** (ask dock + NEEDS YOU tier) and fired the **title count "(1)"** + **favicon dot `#e0af68`** (the pinned needs_you color); leader election — across two tabs **exactly one** held the `serf-hub-os-leader` Web Lock (the second tab a non-leader, not queued, per `ifAvailable:true`); answering cleared the count, then it re-applied when the session returned to needs_you (dynamic tracking). `08` |

**Not verified live (environment-limited — findings, not failures):** the actual OS-notification
popup (headless Chrome did not grant `Notification` permission; the fire path is permission-guarded
in code), sound audibility (`AudioContext` oscillator, no audio in headless), and reconnect-does-not-
re-alert (would need a forced WebSocket reconnect; verified in code + unit tests via the silent
re-baseline). `/project` reveal was not driven live (verified in code: `railController` +
`RailHost`).

**Critical operational finding (for the merge / M9 cutover).** The rewritten SPA serves the page
routes **only when `SERF_HUB_WEB=new`** — `webnext.go:16` (`newWebEnabled`), and its comment states
"Default is the legacy UI until the M9 cutover flips it." A hub started without that env var serves
the **legacy htmx UI**, whose `spawn.js`/`dir-picker.js` chip-pickers render a superficially similar
(but rich, non-interim) model catalog. Any future live-proof or demo of the new UI must export
`SERF_HUB_WEB=new`; the M9 cutover work is what flips the default.

**Out-of-scope observations (not W6 surfaces).** (a) The session-chrome work-time clock rendered an
absurd value ("495269h" — a likely zero-start-time bug in the frozen Wave-5 `StatusRow`); worth a
Wave-5/chrome follow-up. (b) A stale dockview tab (`local:033u2Ikbcf…`, a session ref from a prior
hub) was restored from the shared browser profile's dockview layout localStorage — expected
persistence behavior, but it points at a session absent from this hub.

## Decisions (for Jesse)

- **Nothing is currently open** on the wave itself — every divergence above is pre-ruled or a
  flagged, non-blocking gap.
- **The M9/M10 order flip is proposed and awaiting your word** (unchanged from the ledger; not
  decided here).
- Two items you may want to weigh for W8 sequencing (not decisions this task can make): whether the
  **§1.14 re-sendable-image** gap is fixed before the integration merge or folded into W8, and
  whether the **§4.2 FOUC** pre-paint successor is worth restoring now that the new UI is the target
  of the M9 cutover.

## Next steps (NOT done in this task)

- **The controller's serial merge** — W6 to integration, then the integration branch **re-absorbs
  main**. This close does NOT merge anything.
- **W8 fold-in points from this close's findings** — the rich model/reasoning catalog (§1.4/§1.5,
  already ledger-206-deferred here) and the 13-gap punch list above, led by §1.14 (re-sendable
  images), §4.2 (FOUC), and §4.8 (inert `showCost`).

## Verification

Gates run from `cmd/serf-hub/frontend`, AND-chained, `vitest` bare (exit code unmasked):

```
npx tsc --noEmit  → EXIT 0
npx vitest run    → EXIT 0  (217 files / 3189 tests — unchanged; the micro-items reword comments,
                             a help string, and a gallery demo, adding/removing no tests)
npm run lint      → EXIT 0  (biome ci, 611 files, no fixes)
npm run build     → EXIT 0  (dist/PLACEHOLDER restored via git restore; tree clean after)
```

Go (this branch absorbed main's hub changes):

```
go build ./...                → EXIT 0
go test ./cmd/serf-hub/...    → ok (all packages; root suite 29.9s, rest cached)
```

**Commit trail (this task):** micro-items `a6debb8f0` (`webui wave6 close: micro-items`), then this
report + the parity-sweep record + evidence artifacts.

**Live-proof housekeeping.** The isolated hub (port 19286) and its spawned `serf serve` daemon (port
52537, session `033u6kjm…`) were both stopped and confirmed gone (no `serf`/`serf-hub` process, ports
free, isolated run dir empty). The real host hub was never touched (the fake `$HOME` gave it its own
`hub.lock`). The browser tab was released to `about:blank`. No credential material was echoed into
logs, screenshots, or this report. The built `serf`/`serf-hub` binaries and the fake `$HOME` remain
under the session scratchpad (outside the repo).
