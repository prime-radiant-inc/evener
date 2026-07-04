# Working State & Metrics — Design (v3)

Date: 2026-07-04 (v2: round 1, E 10 / F 12 — F wins; v3: round 2, K 7 / L 9 — L wins; 14 distinct, all folded)
Status: Draft — awaiting Jesse's spec review
Workstream: 2 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Three complaints, one root pattern: the data exists and was never wired.

1. **Working state is split and stale.** Two 2-second pollers: the header one is a ghost (oob-only title swap, orphaned content template, a full `ReadThread(IncludeTurns: true)` per tab per 2s, discarded); the input-strip pill is the real status home but poll-only — `thread/status/changed` reaches the page and touches nothing in it.
2. **The liveness line floats in the wrong place** — a bare sibling of `#conversation` excluded from the content-column centering rule, duplicating state the status row should own.
3. **No metrics.** Providers populate full `llm.Usage`; it persists per assistant turn; everything downstream drops it. `contextmgr.cumUsage` accumulates on every response with zero readers and no persistence. `Turn.StartedAt/CompletedAt/DurationMS` are Codex-native wire fields serf never fills (StartedAt live-only, dropped in replay). `RunningFor` is computed on the appwire path, never set on the local-roster path, rendered by nothing. Cost fields dead; pricing catalog caller-less.

Found en route: `Session.Meta()` stamps `CreatedAt: now` on every call (session_state.go:97) and every autosave clobbers the persisted creation time.

## Decisions

1. **Tokens only in v1 (Jesse)**; cost is a fast-follow.
2. **Event-driven + 30s fallback (Jesse)**; header poller deleted.
3. **Persist the running totals (v2).** Derivation is structurally broken (compaction truncates `ResumeHistory`; forks copy prefix `Usage` + stale timestamps; transcript entries are message-records that cannot span; ended sessions have no source). `SessionMeta` carries the totals; autosave persists them; restore seeds them back.
4. **Work time counts every turn's wall-clock including interrupted and failed turns (v2)** — accumulated at the per-turn terminal boundary. Round 2 validated the site: `finishProcessingAtBoundary` fires per turn via `deliverIfCommunicated` (session_tool_round.go:400) with an idempotent Processing-only guard, and `turnStartedAt` resets per drained entry (session_lifecycle.go:679). **One amendment (round-2 L3): the terminal-model-error path calls `s.Close()` before the boundary** (session_model_call.go:540 vs :548), and `Close()` sets `SessionClosed` first (session_lifecycle.go:85), so the boundary no-ops — the dying turn's work vanished. `Close()` therefore accumulates for a still-Processing session before flipping the state; the "every outcome counts" invariant now includes the death-of-session turn.
5. **Scope is this session only, labeled (v2)**; subagent rollup is a fast-follow.
6. Carried: wall-clock turn duration; sidecar snapshot-only; client-owned quiet/stall; docs-first sequencing.

## Semantics

- **Current-turn elapsed**: wall-clock since the active turn started; live only.
- **Turn duration**: StartedAt → terminal transition, including tools and interrupted/failed outcomes.
- **Session work time**: persisted `WorkMillis` + the in-flight turn's live elapsed, client-added.
- **Tokens**: persisted cumulative self-only totals; the row shows uncached ↑/↓ **labeled as uncached, with a hover/title breakdown carrying cache-read and total** (round-2 L8 — no other web detail surface exists, and an Anthropic cache-heavy session's uncached ↑ would otherwise misread as tiny with no drill-down anywhere).

## Daemon & wire

- **In-memory homes and the round-trip, stated (round-2 K2/K3 — the v2 text would have zeroed everything)**: `Meta()` rebuilds `SessionMeta` from live fields on every call, so the Session must *hold* what meta persists. New: `Session.workMillis`, `Session.createdAt`; `contextmgr` gains `SetCumulativeUsage`. Seeding: `NewSession` sets `createdAt = now`; `RestoreSessionFromMetaWithConfig` seeds `createdAt`, `workMillis`, and `SetCumulativeUsage` from the loaded meta *before* the first autosave can run; `ForkSession` builds a fresh child meta (zeroing is automatic — fork.go:174-187, validated round 2). `Meta()` maps all three instead of stamping `now`/zeros. This *is* the CreatedAt fix's mechanism (round-2 K3: v2 claimed the fix with no design).
- **Meta shape (round-2 K6)**: a dedicated `schema.CumulativeUsage {InputTokens, OutputTokens, CacheReadTokens, TotalTokens int64}` — a deliberate lossy snapshot of `llm.Usage` (conversion stated: pointers nil→0, `Raw` dropped); tagged **`omitzero`** per house convention (snapshot.go:41 precedent) so legacy metas round-trip untouched; the byte-exact golden regenerates (named target: snapshot_golden_test.go:107).
- **Turn timing (round-2 K1 — the v2 mechanism was fictional: no per-turn-end event exists, and the projector's completion sites fire on later, duration-less events)**: a **new `EventTurnEnded` event**, emitted from `finishProcessingAtBoundary` — the same choke point, the same clock as the accumulator — carrying `{TurnDurationMS}`. The projector gains its handler: on `EventTurnEnded` it completes the active turn with `CompletedAt` = the event's timestamp (which *is* the turn's real end) and `DurationMS` = the payload; the legacy completion-at-next-input sites keep their emissions but become idempotent no-ops when the turn is already closed. The v2 `activeTurnStartedAt` projector field is dropped as vestigial. Replay: `appTurnsFromNotifications` copies the timing the logged notifications carry; transcript reconstruction stamps `StartedAt` from entry timestamps only and omits `DurationMS` (message-records, not spans).
- **Wire**: `SerfThread` gains `Usage *SerfUsage` — **a pointer, because `omitempty` never omits a value struct** (round-2 L5; `Goal *GoalState` precedent) — plus `workMillis` and `activeTurnStartedAt` (scalars, omitempty). `StatusInfo` gains the same, pull-callback-fed (shape validated round 2). Old daemons / Codex threads: nil pointer → clusters hide, no `↑0 ↓0`.
- **Ended sessions, the real carrier chain (round-2 K5/L1 — v2's "enumerated" chain named a live-only mapper)**: `WorkspaceData` gains the metric fields, populated on *both* paths — `workspaceDataFromAppThread` (live) and the past-meta literal (web_workspace.go:353-364); `apiSessionDetail`'s ended branch maps them from `wd` into the `SessionDetail` literal (web_api_tree.go:492-507); the live branch keeps `hubDetailFromAppThread`. No override clobbers: the fields flow from the same seed on both paths. **Legacy bound stated (round-2 L9): a session that ended before this ships never autosaves again and shows zero forever** — only sessions that autosaved at least once under the new code carry totals.

## Web

- **Delete the ghost**; port the oob-title assertion to `/state`; the deleted tests are enumerated.
- **One consolidated status row**, two clusters (state + metrics).
- **Elapsed/quiet split**: elapsed server-renderable from `activeTurnStartedAt`; quiet/stall client-owned (`lastFrameAt` is client-only). **Re-bind mechanics stated (round-2 L2 — today's code caches its element handle once and the status row is an innerHTML-swap target)**: the liveness renderer re-acquires its spans on every `htmx:afterSwap` for `#input-status` and the 3s ticker no-ops on detached nodes; `data-stalled` remains on `#conversation` (its consumers — the pulse gate at renderer.js:4691 and CSS — are unchanged); `attemptLivenessSelfHeal` coupling unchanged; the `.liveness` element and its CSS block die; **both liveness jstest suites are ported** to the new spans with their bucket/self-heal cases intact (round-2 L7 enumerates the dispositions).
- **`/state` goes light — as its own path (round-2 L6: v2 would have zeroed TurnCount/ActiveTurnID for the four other `apiSessionDetail` callers)**: a new lean status fetch for `/state` only (status + sidecar, no turns; the row's turns count reads `StatusInfo.Turns`); the shared `apiSessionDetail` keeps `IncludeTurns` for its JSON-API consumers; the double `workspaceData` call disappears with the split.
- **Refresh**: `load, serf-hub:status-refresh from:body, every 30s`; dispatched via `htmx.trigger(document.body, …)`; on `thread/status/changed`, `turn/started`, `turn/completed`; plus the client-side 10s tick while a turn is active (context-gauge freshness). Title: turn-boundary refresh common-case, ≤30s idle worst case, stated.

## TUI

`hubSessionDetail` gains WorkMillis/usage/activeTurnStartedAt; chip strip + details drawer render (drawer carries the full breakdown incl. cache-read). Dashboard unchanged.

## Cross-spec coordination

WS3 (sidebar rebuild) adds `SessionMeta.Origin`; this spec adds `CumulativeUsage`/`WorkMillis` and re-seeds `CreatedAt` semantics. Same struct, same persist path, same byte-exact golden (snapshot_golden_test.go:107). Whichever lands second rebases its meta fixtures on the first. **The reciprocal note now actually exists in WS3 v4** (round-2 K4/L4: v2 claimed "recorded in both specs" while WS3 carried nothing).

## Error handling

Old daemons/Codex: nil usage pointer hides clusters. Meta write failures: autosave semantics (metrics lag one autosave). Legacy live-restored sessions: totals start at zero, appear on first autosave; legacy *ended* sessions: zero forever, stated. Fork children start at zero by construction. Close-during-turn accumulates before the state flip (Decision 4).

## Out of scope

Dollar cost; subagent rollup; per-turn transcript badges (WS6); the task-status-row poller (WS6); a namer-rename push event.

## Testing

- Daemon: per-turn terminal accumulation (completed/interrupted/failed; each turn of a multi-turn drain; **the Close-mid-turn death turn counts** — the L3 regression); `EventTurnEnded` emitted per turn with the accumulator's ms; projector handler stamps CompletedAt/DurationMS at the *turn's* timestamp and legacy completion sites no-op idempotently; **restore round-trip: restore → one turn → autosave → totals are prior+turn, not turn-only** (the K2 regression); `Meta()` maps createdAt/workMillis/cumUsage (CreatedAt stable across autosaves — mechanism now exists, K3); `omitzero` legacy round-trip + golden regen; fork zeroing; replay copies; transcript omits DurationMS.
- Web: ghost removal + ported oob-title; lean `/state` (no turns fetched; `StatusInfo.Turns` renders; the four other `apiSessionDetail` callers keep TurnCount — the L6 regression); ended-session `/state` shows persisted totals **through the enumerated WorkspaceData chain**; dispatch mechanics; 10s active tick; **liveness re-bind across a swap (ticker survives, no detached-node writes)** + both ported liveness suites green; tokens hover-breakdown present; nil-usage hides clusters (no `↑0 ↓0`).
- TUI: chip strip width/content; drawer breakdown.
- e2e: turn completes → metrics advance across surfaces; interrupt → work time advances; daemon restart → totals survive **and next-turn autosave doesn't reset them**; session ends → totals still render; pre-feature ended session → clusters absent, not zero.

## Estimate

~800–1,150 loc including tests: daemon (meta fields + omitzero + golden, Session homes + seeds + createdAt, terminal accumulation + Close amendment, `EventTurnEnded` + projector handler + idempotent sites, restore/fork) ~420–600; web (ghost + ports, WorkspaceData chain, lean `/state` split, hybrid refresh + tick, liveness relocation + re-bind + suite ports, hover breakdown) ~280–400; TUI ~100–150.

## Review log

**Round 1** (v1→v2, E 10 / F 12 — F wins; ~16 distinct): projector held no per-turn start and the claimed source was the completion timestamp; transcript can't span; ended sessions sourceless; compaction/fork corrupt derivation → the persistence pivot; settle-site missed interrupted turns and all but the drain's last turn → terminal-boundary accumulation; scope honesty; dispatch precedent wrong; quiet/stall client-only; `/state` cost; context-gauge freeze; title latency; RunningFor mischaracterization; estimate; WS3 coordination.

**Round 2** (v2→v3, K 7 / L 9 — L wins; 14 distinct, overlaps K4=L4, K5≈L1): the DurationMS event-payload mechanism had no host event — no turn-end event exists and every completion-site event fires later, duration-less (K1, blocker → new `EventTurnEnded` from the boundary; vestigial projector field dropped); `Meta()` rebuilds from memory with no setter/seed, so the first post-restore autosave overwrote persisted totals (K2 → Session/contextmgr homes + restore seeding); the CreatedAt "fix" had no mechanism (K3 → `Session.createdAt` + capture sites); the WS3 cross-reference was one-directional and the "both ways" claim false (K4/L4 → reciprocal note actually added); the "enumerated" web chain routed ended sessions through a live-gated mapper (K5/L1 → WorkspaceData-seeded both-path chain); `CumUsage` shape/tag unspecified — value-struct `omitempty` never omits, house convention is `omitzero`, golden churn unnamed (K6); estimate omissions (K7); relocated liveness wrote through a cached handle into swap-destroyed spans (L2 → afterSwap re-bind + detached-node guard); the terminal-error path closed the session before the boundary, dropping the dying turn's work (L3 → Close accumulates first); nested value `usage` on the wire would render `↑0 ↓0` on Codex/old daemons instead of hiding (L5 → pointer); dropping IncludeTurns from the shared detail fn zeroed four other callers (L6 → lean `/state`-only path); liveness suites/`data-stalled`/CSS dispositions unenumerated (L7); cache-read had no web surface (L8 → hover breakdown + uncached label); legacy ended sessions show zero forever, now stated (L9). Round 2 also *validated*: per-turn boundary firing via `deliverIfCommunicated`, per-entry `turnStartedAt` reset, automatic fork zeroing, the pull-callback shape, and that the ask-user branch leaves `finishProcessingAtBoundary` untouched (no collision).
