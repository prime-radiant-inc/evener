# Working State & Metrics — Design (v1)

Date: 2026-07-04
Status: Draft — awaiting Jesse's spec review
Workstream: 2 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Three complaints from the diagnostic, one root pattern: the data exists and was never wired.

1. **Working state is split and stale.** The web workspace has two 2-second pollers. The header one is a ghost: `renderWorkspaceMeta` (web_workspace.go:264-285) computes state/turns/model, then executes an oob-only title swap (`workspace_meta`, workspace.html:117) — the content template (workspace.html:115) is orphaned, so the header renders nothing while burning a full `ReadThread(IncludeTurns: true, ItemsView: "full")` per tab every 2s (via apiSessionDetail, web_api_tree.go:511-516). The input-strip pill (workspace.html:104-110 → input_strip.html) is the real status home but is poll-only: `thread/status/changed` already reaches the page and `updateThreadState` (renderer.js:313) runs on it, yet never touches the pill DOM.
2. **The liveness line floats in the wrong place.** "working · quiet ~Nm" is a bare sibling of `#conversation` (ensureLivenessEl, renderer.js:2026-2036) excluded from the content-column centering rule (style.css:483-488) with its own unrelated max-width (style.css:2004) — it detaches visually, and it duplicates state the status row should own.
3. **No metrics.** Every provider adapter populates full `llm.Usage` (in/out/reasoning/cache tokens; llm/types.go:454-465) and it persists per assistant turn (agent/session.go:745-767) — then everything drops it: `StatusInfo` carries no usage field (server/server.go:80-93), the transcript projector never reads `turn.Usage` (apptranscript.go:191-377), the live projector receives `AssistantTextEndData.Usage` and reads only `.Text` (appwire_projection.go:236-260). A cumulative accumulator already runs on every response (`contextmgr.cumUsage`, AddUsage at session_model_call.go:602) — `CumulativeUsage()` has zero callers. `serf-doctor apilog` (agent/doctor/apilog.go:45-119) already implements the exact aggregation, CLI-only. On timing: `Turn.StartedAt/CompletedAt/DurationMS` are Codex-native wire fields (appwire/types.go:304-313); serf sets only StartedAt, only live (startedTurn, appwire_projection.go:690-700), drops it in notification replay (appwire_turns.go:109-125 doesn't copy it), and never sets the other two — the only DurationMS producer in the tree is the Codex bridge pass-through (codex_mapping.go:31). `RunningFor` is computed and rendered by nothing (web_format.go:63-71; consumer deleted in 18f7fb3c5). Web `Cost` (web_types.go:174) and TUI `statusBarInfo.Cost` (statusbar.go:49) are dead fields; the pricing catalog (llm/pricing.go:7-83) has zero production callers.

Found en route, fixed here because honest metrics need honest timestamps: `Session.Meta()` stamps `CreatedAt: now` on every call (agent/session_state.go:97), and every autosave persists it — the on-disk meta's creation time is clobbered continually. (The transcript header holds a correct one-shot CreatedAt, transcript.go:39.)

## Decisions (Jesse, 2026-07-04)

1. **Tokens only in v1** — no dollar cost. Cost is a fast-follow behind the same sidecar struct (catalog-known models only); gateway/user-model pricing is unreliable (openai-compat spec deferred it, 2026-07-02-openai-compat-providers.md:273).
2. **Event-driven + 30s fallback** for the status row; the header poller is deleted unconditionally.
3. Sequencing (carried from the merge discussion): docs-only until the ask-user branch lands; implementation orders daemon-first, renderer.js/TUI last, to stay out of its contested files.

## Semantics

- **Current-turn elapsed**: wall-clock since the active turn started ("working · 34s"). Live only; disappears at turn end.
- **Turn duration**: StartedAt → CompletedAt wall-clock, **including tool execution** — what "working" means to the person watching. (Pure model latency exists per round as `transcript.APICall.LatencyMs` and stays a doctor/diagnostics concern.)
- **Session work time**: sum of completed turn durations; while a turn is active, clients add its live elapsed on top (they hold `StartedAt` and a clock — the wire value never ticks per-second). Survives restarts by derivation, not persistence.
- **Tokens**: cumulative session totals, uncached-input and output shown (`↑12.4k ↓3.1k`), cache-read/total available in detail surfaces. Provider-truth, never estimated.

## Daemon & wire

- **Turn timing is protocol convergence — fill the native fields.** The projector stamps `CompletedAt`/`DurationMS` on the `turn/completed` notification's Turn (it already knows the start from the triggering event's timestamp); the notification-replay reconstruction (appwire_turns.go) copies `StartedAt` from `turn/started` and `CompletedAt`/`DurationMS` from `turn/completed`; the transcript reconstruction (apptranscript.TurnsFromFile) derives both ends from persisted `schema.Turn.Timestamp` values it currently ignores. No serf/* namespace, no sidecar — same shape Codex itself emits.
- **Cumulative metrics ride the SerfThread sidecar, snapshot-only.** New optional nested struct on `appwire.SerfThread` (types.go:174-197): `usage {inputTokens, outputTokens, cacheReadTokens, totalTokens}` plus sibling `workMillis int64`. Precedent is explicit: context pressure "rides on the Thread snapshot instead" of a notification (appwire/protocol.go:136-139), and Codex's protocol has no token shape to converge on (verified: codex_wire_types.go carries only DurationMS). No new notifications, no new methods.
- **Sourcing**: `StatusInfo` gains the same optional usage/workMillis fields, fed like context pressure is (SetContextPressureFunc precedent, server/server.go:368) — daemon-side reads `contextMgr.CumulativeUsage()` (finally a caller) and a new small work-time accumulator summed at the drain-loop settle. `/status` therefore carries the same numbers the appwire snapshot does.
- **Restore recompute, no new persistence.** At restore the session already loads full history; seed `cumUsage` by summing assistant-turn `Usage` and work time from turn timestamps — mirroring the attention model's resume-recompute pattern. Accumulators are never written to meta; the transcript stays the source of truth.
- **Meta CreatedAt fix**: capture creation time once at session init (source: transcript header / first save), `Meta()` reports it; `UpdatedAt` keeps stamping now. Small, load-bearing, regression-tested against the autosave-clobber.

## Web

- **Delete the ghost**: the `.workspace-meta` poll div (workspace.html:27-34), `renderWorkspaceMeta`'s poll route, and the orphaned `workspace_meta_content` define. The oob title swap was that poll's one live effect, and no rename notification exists to replace it — so the title's `hx-swap-oob` span moves into the status-row (`/state`) response: one endpoint, one trigger set, and the namer's early rename lands at the next turn-boundary refresh. Halves per-tab status RPCs before anything else ships.
- **One consolidated status row** in the bottom strip (`#input-status` stays the element): two visual clusters.
  - *State cluster*: status badge + dot (unchanged semantics from the attention work) + current-turn elapsed while active + the quiet/stall warning inline ("working · 34s · quiet ~2m ⚠"). The separate `.liveness` element dies; its logic (20s/180s buckets, LIVENESS_* constants, stall self-heal, dot pulse) relocates into the status-row renderer. The stall threshold keeps mirroring hubcore's `StallThreshold` (wedge.go:13-18).
  - *Metrics cluster*: `⧗ 12m 34s` session work time + `↑12.4k ↓3.1k` tokens + existing context gauge, cwd, branch, goal. Cost span stays dead until the fast-follow.
- **Hybrid refresh** — structure from the server, liveness from the client:
  - htmx trigger becomes `load, serf-hub:status-refresh from:body, every 30s` (Decision 2). renderer.js dispatches `serf-hub:status-refresh` on `thread/status/changed`, `turn/started`, and `turn/completed` — it already dispatches a custom `serf-hub:thread-status` event for notifications.js, so this is the established pattern, extended to two more triggers.
  - The server renders point-in-time values for elapsed/quiet (it holds `StartedAt`); the client's 3s liveness timer, retargeted at the swapped-in spans (stable ids), keeps them fresh between swaps and owns the dot pulse and stall bucketing — the server never serves a seconds counter expected to stay live, and a swap never leaves the row blank.
- **RunningFor resurrection**: populated on both workspaceData paths (the local-roster path never set it — web_workspace.go:302-350) and rendered as the state cluster's elapsed; the replay StartedAt fix above is what makes it survive reconstruction.

## TUI

- `hubSessionDetail` (hub_types.go:56-85) gains `WorkMillis` + usage fields, read from the SerfThread sidecar over the existing push-plus-refetch-on-transition transport (hub_notifications.go:50-65) — no new polling.
- **Chip strip** (composer_render.go:14-29) gains work time + tokens; it is the TUI's own consolidation precedent ("the single live-context line", comment at :20-22). The scroll-away header keeps state; the details drawer (details_drawer.go) gains the full usage breakdown (in/out/cache-read/total).
- Dashboard rows unchanged (age ≠ work time; out of scope).

## Error handling

Old daemons: sidecar fields absent → clients render no metrics (omitempty; nothing breaks). Codex-bridged threads: DurationMS passes through as today; no serf usage sidecar → metrics clusters hide. Zero-usage responses (provider omitted usage): accumulator adds zero; display renders last known totals. Restore with malformed/legacy transcripts: derivation defaults to zero, never errors the restore.

## Out of scope

Dollar cost (fast-follow; Decision 1). Per-turn "took Ns" transcript badges (data becomes available; rendering is WS6 consistency). The task-status-row's 5s poller (`startTaskBadgePoller`) and the never-emitted `serf/task/updated` (WS6). Web details-panel context pressure (WS6). Per-model/token breakdowns, budgets, alerts. Notifications changes — none.

## Testing

- Daemon: accumulator unit tests (Add on response, settle-time work summation); restore-derivation matrix (assistant-turn sums, empty history, legacy transcript); Meta CreatedAt regression (autosave twice, creation time stable); StatusInfo/appwire snapshot carries usage+workMillis; turn timing populated live + both replay paths (golden updates named: appwire_turns, apptranscript).
- Web: template tests for the consolidated row + ghost removal (`workspace_meta_content` absent, no `/meta` route); jstest for event-driven refresh (status-refresh dispatch on the three triggers, 30s fallback wiring, client-owned spans surviving a swap, quiet/stall relocation with existing liveness bucket cases ported); RunningFor on both data paths.
- TUI: chip-strip width/content tests (existing exact-width discipline); details-drawer breakdown.
- e2e scenario card: turn completes → tokens/work-time advance across web + TUI without a 2s poll observed.

## Estimate

~550–800 loc including tests: daemon (timing + sidecar + StatusInfo + restore + CreatedAt fix) ~250–350; web (ghost deletion, row consolidation, hybrid refresh, liveness relocation) ~200–300; TUI ~100–150.
