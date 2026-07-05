# Consistency & quick-wins sweep — design

Date: 2026-07-05
Status: approved direction (Jesse, 2026-07-05 brainstorm); implementation pending plan + SDD.
Workstream: WS4 + WS6 of the 2026-07 web-UI UX overhaul, plus the folded-in remainder of
`2026-07-04-ask-attention-tiering-design.md` (§3–§7).
Base: branch `consistency-sweep` off main `6647b744`. Code anchors verified 2026-07-05.

## Why one spec

WS4 (quick wins), WS6 (consistency sweep), and the ask-tiering remainder all touch the same
handful of surfaces — attention rendering, the status vocabulary, the sidebar rows, the
composer, the settings pane, the metrics display. Shipping them apart would mean three passes
over the same files and three chances to re-diverge. The value here is coherence, so they land
as one workstream, decomposed into parallel tracks (§Tracks) that own disjoint file sets.

WS4 and WS6 inventories overlap deliberately (copy fixes, "tasks no tasks", settings copy
staleness); this spec resolves the overlap by owning each item once.

## Decisions (Jesse, 2026-07-05)

1. **Full scope.** Every bucket-D design-adjudication item is in, including the two largest —
   vocabulary standardization and model-picker curation. Keep the workstream whole rather than
   splitting vocabulary into its own spec.
2. **Real icons, not text glyphs.** The `⟳ ◆ ✕` text glyphs are too low-signal; replace with
   line icons (Lucide-style). Web renders icon + colored dot with the **word as the hover
   tooltip**; the TUI keeps the **word**.
3. **Palette = semantic ramp.** green Working · blue Needs-you · amber Warning · red Error ·
   gray Idle/Ended. Chosen over the warm ramp because amber-needs-you and orange-warning were
   too close to distinguish at dot size. Needs-you moves off amber onto blue; this is accepted
   churn.
4. **Needs-you splits into two visible bands** (the ask-tiering fold-in): Question-waiting
   (`?`, loud by default) and Your-move (`!`, quiet). Both blue.
5. **Warning gets its own identity** (amber triangle), no longer folded into needs-you.
6. **`processing` → `working`** rename across tokens/CSS/theme so the code word matches the UI.
7. **Composer at rest:** a rested `awaiting` session shows plain **Send** (no Stop/steer/queue).
8. **Model picker:** a `Recent` group (global recency, last 5, derived from session history — no
   manifest) atop the **browsable** full catalog, with catalog metadata surfaced as badges.
   All three pickers.
9. **Cost display:** show estimated `~$` from catalog pricing, gated by a new **Show-cost** web
   setting (default **on**); precision to the cent (`~$2.41`). Session-level and per-turn.
10. **New web settings:** font-size (4 presets), Enter-to-send toggle (⌘↵ default), Show-cost.
11. **Context-pressure** display added to the web details panel (already present in the TUI).
12. **stdio probe** `LookPath`-only limitation: **left as-is** — a documented limit, not worth
    a real MCP handshake probe now.

---

## 1. Unified status vocabulary & icons

**The problem.** The same concept carries up to three names across surfaces. "Working" is the
normalized state `active` (tree.go:279-298), the attention level `working` (attention.go:51-62),
and the color token `processing` (style.css:18 `--state-processing`, tuitheme `StateProcessing`).
The TUI prints the word `ACTIVE` (hub_session_view.go:28-29, uppercased `StatusBadge`); the web
shows only the `⟳` glyph + blue dot (sidebar.js:30-66) with no word. `warning` is a distinct
normalized state that rolls up visually identical to `awaiting` (both → amber `◆`,
tree.go:508-519). Glyphs are overloaded: `⟳` means working but is reused for the rollup-live
count, subagent-running, plan-in-progress, and the "Reconnecting…" banner (renderer.js:814-821).

**The target — one word + one icon + one color per state, identical web/TUI:**

| State | Color | Icon (Lucide) | Word (TUI) / tooltip (web) | Notes |
|---|---|---|---|---|
| Working | green | `refresh-cw` | WORKING | rename `processing`→`working` |
| Needs you · Question waiting | blue | speech-bubble `?` | "Question waiting" | ask-pending band; loud by default |
| Needs you · Your move | blue | speech-bubble `!` | "Your move" | generic awaiting; quiet by default |
| Warning | amber | `triangle-alert` | WARNING | own identity, un-folded from needs-you |
| Error | red | `circle-x` | ERROR | the shipped errored lane |
| Idle | gray | `pause` | IDLE | live, nothing pending |
| Ended | dim gray | `check` | ENDED | not live |

**Scope of edits (this is the bulk of the loc).** The attention design
(`2026-07-03-attention-status-model-design.md` §"errored lane — full enumeration") already
enumerated nearly every site that renders a state; this sweep re-touches the same set to swap
glyph→icon, recolor needs-you amber→blue, split warning out, and rename `processing`→`working`:

- **Web:** status-dot rendering (sidebar.js:30-66) becomes icon+dot; state colors (style.css:
  593-605) recolored per palette; colorblind shape channel (style.css:974) extended so needs-you
  (blue) and warning (amber) each get a distinct non-colliding shape; the rollup `⟳N · ◆M`
  (sidebar.js:482-507) adopts the working/needs-you icons; connection banner (renderer.js:814-821),
  subagent glyphs (renderer.js:2546-2562, :3170), plan/task status glyphs (renderer-format.js:
  475-493), and needs-you affordances (renderer.js:4213/4321/4451/4787) migrate to the icon set;
  `stateLabel` web labels (web_format.go:178) unify.
- **TUI:** `stateLabel` (hub_dashboard_view.go:556-577) and the uppercased `StatusBadge`
  (hub_session_view.go:28-29) adopt the unified words; `stateColor`/theme tokens rename
  `StateProcessing`→`StateWorking`; the `◆N` needs-you badge (hub_dashboard_view.go:177-182) and
  composer question chip (composer_panel.go:222) adopt the icon vocabulary where the terminal can
  render it (unicode fallback where it can't); subagent tally (msgrender/tool_bodies.go:304,322).
- **Glyph legend** (style.css:939-940) rewritten to the new icon set.
- **Rank-function consolidation (opportunistic):** `AttentionRank` (tree.go:150-165) and
  `rollupRank` are duplicated, and the TUI has a third (`attentionRankLabel`,
  hub_dashboard_view.go:597-611). Fold toward one shared source **only where cheap and low-risk**;
  do not let it block the vocabulary work.
- **Existing ask-question chips** adopt the same blue `?`-bubble language (one vocabulary for
  "respond to me").

**Guardrail.** JSON/TOML keys stay snake_case; the `processing`→`working` rename touches
CSS/theme/Go identifiers, not wire keys. Do not rename any wire enum value (`active`, `awaiting`,
`warning`, `errored` on the wire are the Codex-shaped contract) — this is a **display** rename
only. `namingcheck` runs at `make lint`; per-task golangci misses it.

## 2. Ask-aware attention tiering + loud-scoping (folded in)

Remaining §3–§7 of `2026-07-04-ask-attention-tiering-design.md`; §2 (restore rebuilds the
pending-ask set) already shipped to main. Anchors re-verified on `6647b744` per
`/tmp/ask-attention-tiering-remaining.md`. Absorbed verbatim in intent:

- **Wire (`pending_ask`, daemon-truth, additive).** `StatusInfo` (server/server.go:82) gains
  `PendingAsk bool` (`pending_ask,omitempty`), from the snapshot that already exposes
  `HasPendingAsk()` to serve.go. Prober (`statusInfo`, prober.go:19-22) decodes it; `LiveEntry`
  (roster.go:23) gains `PendingAsk`. `hubapi.TreeNode` (hubapi/types.go:78) gains
  `ask_pending,omitempty`; `hubcore.AttentionEntry` (attention.go:17) gains `askPending`
  (camelCase, matching the AttentionSummary parallel-type precedent) so `serf/attention/changed`
  rows carry it without a tree refetch.
  - **appwire cross-check:** `pending_ask` is a new struct *field*, not a new method — so it does
    not require the dual-router catalog change (per handoff appwire rule); confirm during build.
  - **Naming note (reviewer flag, not a decision):** the spec uses `pending_ask` on StatusInfo
    but `ask_pending` on TreeNode. Implement as written unless Jesse unifies them.
- **Ranking — three bands in NeedsYou:** errored → ask-pending → your-move, oldest-first inside
  each band. Replace the errored-first boolean sort (tree.go:711) with a band function; `AttentionRank`,
  `rollupRank`, `attentionLevel` unchanged (ask-pending is a band, not a level). TUI
  `dashboardRowLess` (hub_dashboard.go:221) gains the same band.
- **Loud-scope setting** `loudScope`: `"asks"` (default) | `"all"`. `DEFAULT_PREFS`
  (notifications.js:27) gains it; `migratePrefs` (notifications.js:63-76) bumps version, backfills
  `"asks"`. Settings control uses the existing `data-notif` commit pattern (settings.js:49,115).
  Gating (notifications.js:238-254): loud branch becomes
  `if (prefs.loudScope === "all" || ch.askPending || ch.level === "error") { … }`. Badge/title/
  favicon keep reflecting **all** of needs-you.
- **Row markers** = the §1 icons: Question-waiting rows get the blue `?`-bubble + `data-ask="true"`
  (sidebar.js row template); Your-move rows get the blue `!`-bubble. TUI `hubRow`
  (hub_model.go:42) gains `askPending bool`; the `◆N` header badge keeps counting all needs-you.
- **Tests** per the ask-tiering §7 (prober decode; serve `/status` pending_ask true→false→
  true-after-restart; band-sort units; jstest loud-scope migration + gating; extend
  `ask-cross-session-notify` e2e).
- **Severable docs-only companion:** scenario-card hermetics batch (§9 of the ask-tiering spec /
  §6 of the remaining doc) — five card mechanics fixes, every falsification line byte-identical.

## 3. Composer action-state at rest

Both surfaces collapse `awaiting`==`active` in one line — `sessionTurnActionState`
(composer_panel.go:99-105) and `turnAcceptsActions` (renderer.js:436-438) — so a settled
"your move" session shows a Stop button, a steer button, and routes text into Queue mode
(composer_panel.go:141-157; renderer.js:415-434; workspace.html:88/91/93).

**Fix:** distinguish `awaiting` from `active`. At rested `awaiting`, the composer drops to plain
**Send** — no Stop, no steer, no queue — even when `Capabilities.Queue` is present. `active`
(genuinely running) is unchanged: Stop/steer/queue all still apply. The gating that already lets
awaiting sessions send/steer (keys on `!processing`) is untouched; this changes only the
composer's presented affordance. The `AwaitingQuestion` chip (driven by the pending-ask set, not
by this state — composer_panel.go:122/220-223) is unaffected.

## 4. Model picker — Recent + browsable, enriched

Today all three pickers are a raw dump: raw model ids, provider-grouped alphabetically (so dated
snapshots sort above current models), catalog metadata mostly unused. Wire `ModelDescriptor`
(appwire/types.go:755-758) is thin (provider+model); the web `/api/models` handler already joins
`llm.ModelInfo` (web_spawn.go:222-253) but the pickers render only `m.model` + a ctx/price line
(spawn.js:1468-1475; settings-pickers.js:97; TUI model_picker.go).

**Target (all three pickers — web spawn `openModelPicker`, web settings `buildModelPicker`, TUI
`tuipick.ModelPicker`):**

- **`Recent` group** on top: the last 5 distinct models across recent sessions. Derived from the
  Past index — `Past.Search` returns `[]PastEntry` (past.go:240), each carrying full
  `SessionMeta` including `Model` (schema/snapshot.go:20). Global recency (not per-project/
  harness). No manifest. Fresh install with no history → no Recent group, just the provider list.
- **Browsable full catalog** below, provider-grouped. Browsable by scroll/expand, not
  search-gated; search only filters. Dated-snapshot ids (`-\d{8}`) sort last within a provider.
- **Auto-prettified display names** derived from the id (catalog `DisplayName` is currently the
  raw id, model_catalog.go:276) — no hand-maintained name map.
- **Surface catalog metadata as badges:** tools / vision / reasoning (+ effort levels) /
  web-search / context-window / max-output / price. Data is the fused live-list × embedded-catalog
  join that already exists server-side.
  - **Graceful degradation:** `catalogModelInfo` (web_spawn.go:422-446) returns nil for a live
    model absent from the embedded catalog — such a model must still render (name + provider + id),
    just without badges. Do not drop uncatalogued live models.
- **TUI:** the flat list (model_picker.go:145-188) gains a `Recent` group at top and a compact
  caps/ctx/price tail per row; keep the 15-visible + "… N total" affordance.

`llm/pricing.go` (`Price`/`GetPrice`, currently zero non-test callers) is **not** adopted here —
the picker path reads `ModelInfo.InputCostPerMillion`/`OutputCostPerMillion` directly, as the web
handler already does. Cost display (§5) is the natural home for `pricing.go` if we want one
pricing path; see §5.

## 5. Metrics & cost

WS2 shipped work-time + tokens for the web (input_strip.html:10-11, formatWorkMillis
web_format.go:99-101); the daemon computes `workMillis` (session_state.go:209-220) and token
totals (contextmgr CumulativeUsage), snapshotted into `SessionMeta.WorkMillis`/`CumulativeUsage`
(snapshot.go:99/95) and carried on `appwire.SerfThread.Usage`/`.WorkMillis`/`.ActiveTurnStartedAt`
(types.go:220-222).

- **Dollar cost.** Add an estimated `~$` next to tokens wherever tokens show, computed from
  catalog pricing (input/output, cache split where available). The `~` marks it an estimate,
  not billing-exact. **Gated by the new Show-cost setting** (default on, §6). Precision: cents,
  `~$2.41`. One blended total (no cache-read/write breakout for now). **Single pricing path:**
  route cost through `llm/pricing.go` `GetPrice` (pricing.go:30-64) — its first real caller —
  rather than re-reading `ModelInfo` fields, so cost has one source of truth. (Confirm parity
  with the picker's direct-field reads during build.)
- **Per-turn badge.** Extend the shipped hover-only turn-timing chip
  (`2026-06-25-hover-only-turn-timing-metadata`) with per-turn tokens and `~$` (cost honoring the
  Show-cost setting). The wire carries `Turn.CompletedAt`/`DurationMS` already.
- **The two skipped surfaces (WS2 deferred display wiring).** No new design — plumb the existing
  metrics in:
  - `pastEntryThread` (app_threadread.go:121) builds `SerfThread` (152-163) without
    `WorkMillis`/`Usage`/`ActiveTurnStartedAt`, so a TUI user viewing an **ended** session via the
    hub sees no metrics. The values exist on `entry.Meta` (web_workspace.go:346-347 maps them for
    the web path) — set them here too.
  - `details_drawer.go` (42-83) renders context pressure (61-62) but no work-time/token line — add
    the session-metrics summary line.
- **Context pressure in the web details panel.** The data exists — `Session.ContextPressure()`
  (session_state.go:164-173), wire `SerfThread.ContextPressure`/`ContextRemaining`
  (types.go:197/200), web `ContextPercent` (web_workspace.go:304/497). It is already shown in the
  TUI drawer (details_drawer.go:61-62); complete the equivalent display in the **web details
  panel** (verify the exact current gap during build — the value is mapped, the panel display may
  be missing or partial).

## 6. New web settings

Three new controls in the web settings pane:

- **Font size — 4 presets S/M/L/XL** scaling the `--text-*` base tokens (~90/100/115/130%).
  Never designed in the design system before; add to `docs/web-ui/design-system.md`. Persisted
  like other web prefs.
- **Enter-to-send toggle.** Default unchanged: ⌘/Ctrl-Enter sends, Enter = newline (hardcoded
  today at renderer.js `handleComposerKeydown`). Toggled on: Enter sends, Shift-Enter = newline.
  Web-only.
- **Show-cost toggle.** Default **on**. Gates all `~$` display from §5.

## 7. Copy & display fixes

- "tasks no tasks" copy.
- Eternal "tasks loading…" on ended sessions (should resolve to a terminal state, not spin).
- Settings copy staleness (WS4/WS6 overlap; the systemic vocabulary work is §1 — here, the
  literal stale strings).
- WS1 leftover: light-theme errored tint.
- **Recent-prompts desktop truncation.** The 2-line clamp for `.spawn-recent-row` exists only in
  the phone media query (style.css:4121-4127, inside `@media (max-width:767px)` opened at :3782);
  the base rule (style.css:3128) has no clamp, so desktop renders full multi-line walls
  (spawn.html:85-92, data from `Past.Search("",5,0)` web_spawn.go:50-57). Apply the clamp on
  desktop too.

## 8. Correctness / comment residue (the mechanical sweep)

- **`GET /api/sessions/<id>` ignores a live rename** (WS3 T25 Bug 2) — falls back to raw id though
  `/api/tree` and meta resolve the name; give the session-detail endpoint the same name resolution.
- **Untested `encodeURIComponent` for `working_dir`** in the client sidebar (WS3 T23) — correct
  but untested; add the test.
- **Row-menu edge (WS3 R2):** a project both test-run and archived shows "Archive" (idempotent
  no-op) instead of "Unarchive"; fix the menu logic.
- **Reconnect-recovery hint text** (WS5): the classifier derives Hint from Source alone, so
  recovery notices reuse failure-flavored text; give recovery its own non-failure hint.
- **T8 (WS3):** un-archive fires `Delete("project", filepath.Base(id))` on the common case —
  behaviorally inert; either fix the misleading comment or gate on `id != filepath.Base(id)`.
- **T18 (WS3):** an ended-rename racing a session live + daemon failure edits a live meta file
  (atomic, recoverable, silently reverted) — hard-fail instead of silent fallthrough, or tighten
  the comment.
- **Stale comments** at session_init.go:1204/1212 claiming SourceMCP "doesn't exist yet (Task 10)"
  — it shipped; remove.
- **`conn.reconnecting` clear** uses per-branch writes; a `defer` is strictly safer at zero cost.
- **`serf/task/updated`** is defined but never emitted; and the task-status-row 5s poller is a
  transport straggler — emit the event and retire the poller, or remove the dead topic. Adjudicate
  during build against what the status row needs.
- **`TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry`** is load-sensitive under
  full-suite parallelism (one observed "dial factory called 1 times, want 2"; 10/10 clean in
  isolation under `-race`). Introduce a fake clock or widen the backoff window in the test.

## 9. Investigate / verify-live (may yield no code)

- **Project last-touched ordering feel.** WS3 pinned `LastActivity` ordering; verify it feels
  right live now that WS2's honest `CreatedAt` is in. Adjust only if it reads wrong.
- **Spawn-failure UX re-verify** now that WS5 removed the main MCP-fatal cause of buried-stderr
  HTTP 500s — re-check what failure surfaces remain.

Both are e2e/observation tasks; any code they produce is small and folds into the owning track.

---

## Tracks (parallel worktrees)

Each track is a worktree off `consistency-sweep` with its own subagent-driven-development run.
Boundaries chosen so tracks own **disjoint files**; the two coupling points are called out.

- **Track A — Vocabulary/icons + ask-tiering (§1 + §2).** Tightly coupled: both rewrite attention
  rendering, needs-you icons, sidebar rows, TUI dashboard, notifications gating. Must be one
  track. Largest. Owns: sidebar.js, style.css state/glyph regions, renderer.js status regions,
  notifications.js, TUI hub_dashboard*/hub_session_view/composer question chip, hubcore
  tree/attention/prober/roster, hubapi/types, server StatusInfo, appwire AttentionEntry.
- **Track B — Model picker (§4).** Cleanly independent. Owns: app_models.go, web_spawn.go
  (models path), spawn.js (model picker), settings-pickers.js, TUI tuipick/model_picker.go,
  hub_commands.go model-list path, past.go (recent-models query — read-only add).
- **Track C — Metrics/cost + settings (§5 + §6).** Owns: input_strip.html, web_format.go
  (metrics), app_threadread.go, details_drawer.go, per-turn timing chip, web details panel
  context-pressure, llm/pricing.go adoption, settings pane (font-size, Enter-to-send, Show-cost),
  design-system.md.
- **Track D — Composer-at-rest + copy + correctness residue (§3 + §7 + §8 + §9).** Scattered small
  edits; runs **after A merges** (touches renderer.js composer + sidebar.js copy that A owns), or
  is partitioned so its files don't overlap A's open work.

**Coupling points to coordinate:**
1. **settings.js / notifications.js** — Track A adds the `loudScope` control; Track C adds
   font-size/Enter-to-send/Show-cost. Both edit the settings pane. Assign the settings-pane file
   to **one** track (A, since loud-scope is attention-coupled) and have C hand A its three
   controls as a spec'd addition, OR sequence C's settings edits after A. Decide in the plan.
2. **`~$` cost + model-picker pricing** — Track C routes cost through `llm/pricing.go`; Track B
   reads `ModelInfo` price fields directly. No file overlap, but both consume pricing — verify
   the numbers agree.

Merge order: **A first** (it moves the most and everything else rebases onto its vocabulary), then
B and C (independent, either order), then D last. `--no-ff` merges; full gates on merged main
before push (repo sets `merge.ff=only`; explicit `--no-ff` overrides). After each conflict
resolution: re-grep the whole repo for conflict markers and `go vet` the touched packages
(`go build` does not compile test files).

## Testing strategy

Per repo process — TDD red-first for every behavioral change; test output must be pristine
(capture and assert any intentional error output). Per module (`GO_MODULES` in Makefile).

- **§1 vocabulary:** cross-surface agreement test (sidebar tree vs list/read vs TUI) extended for
  the new icon/word/color set including the un-folded warning and blue needs-you; colorblind
  shape-channel assertions; jstest for the web status-dot/icon rendering.
- **§2 ask-tiering:** the ask-tiering §7 suite (prober decode; serve `/status`
  true→false→true-after-restart; band-sort units tree + TUI; jstest loud-scope migration +
  gating; extend `ask-cross-session-notify` e2e).
- **§3 composer:** unit tests for `sessionTurnActionState`/`turnAcceptsActions` distinguishing
  awaiting from active; the affordance shown at each state on both surfaces.
- **§4 model picker:** Recent-derivation unit test (from PastEntry.Meta.Model, dedup, last-5,
  global); uncatalogued-live-model graceful render; jstest picker grouping/ordering/badges; TUI
  picker snapshot.
- **§5 metrics/cost:** cost computation unit test (catalog price × usage; cache split); pricing
  parity between the cost path and picker; pastEntryThread + details_drawer now carry metrics;
  per-turn hover badge; web details-panel context-pressure.
- **§6 settings:** font-size preset application to `--text-*`; Enter-to-send keydown behavior both
  modes; Show-cost gating.
- **§7/§8:** each correctness item gets its red-first test (session-detail rename resolution;
  encodeURIComponent; row-menu Unarchive; reconnect-recovery hint; T8/T18 behavior or comment;
  the TestReconnect flake via fake clock).
- **e2e scenario cards** (test/scenarios/ format): run fully live with an isolated fake `$HOME`
  (real `~/.serf` untouched), dedicated Chrome profile; evidence over assertion. New/updated cards
  for: the unified status vocabulary round-trip; ask-tiering loud-scope default; composer-at-rest
  send vs queue; model-picker Recent + browse; cost display + Show-cost toggle. Plus the ask-card
  hermetics batch (§2 companion).
- **Lint:** `make lint` (namingcheck runs only here) before each merge — the `processing`→
  `working` rename and any new fields must pass namingcheck; deliberate camelCase (e.g.
  `askPending` parallel-type) takes a `// serf:naming-ignore:` line.
- **Golden churn:** any new `SessionMeta` field regenerates `goldenMeta()`/`goldenMetaJSON`
  (agent/snapshot_golden_test.go, omitzero keeps legacy round-trip). This sweep adds no
  SessionMeta fields by design (metrics fields already exist); confirm.

## Out of scope

- Read/seen tracking (punted at the attention-model gate; the ask-tiering re-ask behavior needs
  none).
- New notification transports.
- Live per-provider `/models` enrichment beyond the existing fused catalog join (deprecation
  flags, rate limits) — future item.
- A hand-maintained curated model manifest (explicitly rejected in favor of Recent).
- Cache-read/write cost breakout; a real stdio-probe MCP handshake.
- Model `pricing.go` becoming the picker's pricing path (§4 keeps the direct field read; only §5
  cost adopts `pricing.go`).

## Estimate

Rough, in loc including tests, by track:

- Track A (vocabulary/icons + ask-tiering): ~1,400–1,900 — the vocabulary re-touch dominates
  (every enumerated attention site) and the ask-tiering remainder is ~350–500 on its own.
- Track B (model picker): ~500–750.
- Track C (metrics/cost + settings): ~600–850.
- Track D (composer + copy + correctness + investigate): ~350–550.

Total ~2,850–4,050 loc across four tracks, dominated by breadth (many small edits) rather than
depth.

## Open items for the plan

1. Settings-pane ownership (coupling point 1): assign to Track A, or sequence Track C's settings
   edits after A. Recommend: Track A owns the settings-pane file; C's three controls land as a
   defined addition within A's task list or immediately after A merges.
2. Whether the rank-function consolidation (§1) is worth doing in this sweep or noted for later —
   default: opportunistic, non-blocking.
3. `serf/task/updated` (§8): emit + retire the 5s poller, vs remove the dead topic — decide from
   what the status row actually needs.
