# Working State & Metrics — Design (v2)

Date: 2026-07-04 (v2 after adversarial round 1: E 10 / F 12 — F wins; ~16 distinct findings, all folded)
Status: Draft — awaiting Jesse's spec review
Workstream: 2 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Three complaints from the diagnostic, one root pattern: the data exists and was never wired.

1. **Working state is split and stale.** The web workspace has two 2-second pollers. The header one is a ghost: `renderWorkspaceMeta` (web_workspace.go:264-285) computes state/turns/model, then executes an oob-only title swap — the content template (workspace.html:115) is orphaned, so the header renders nothing while burning a full `ReadThread(IncludeTurns: true, ItemsView: "full")` per tab every 2s. The input-strip pill (workspace.html:104-110) is the real status home but is poll-only: `thread/status/changed` already reaches the page and `updateThreadState` runs, yet never touches the pill DOM.
2. **The liveness line floats in the wrong place.** "working · quiet ~Nm" is a bare sibling of `#conversation` excluded from the content-column centering rule (style.css:483-488) — it detaches visually, and it duplicates state the status row should own.
3. **No metrics.** Every provider adapter populates full `llm.Usage` and it persists per assistant turn — then everything drops it: `StatusInfo` carries no usage field (server/server.go:80-93), the transcript projector never reads `turn.Usage`, the live projector receives `AssistantTextEndData.Usage` and reads only `.Text`. A cumulative accumulator already runs on every response (`contextmgr.cumUsage`, AddUsage at session_model_call.go:602) — `CumulativeUsage()` has zero callers, and it is not persisted. On timing: `Turn.StartedAt/CompletedAt/DurationMS` are Codex-native wire fields; serf sets only StartedAt, only live, drops it in notification replay, and never sets the other two. `RunningFor` is computed on the appwire path (web_format.go:51,63-71), never set on the local-roster path, and rendered by nothing (round-1 F12 corrected the v1 phrasing). Web and TUI `Cost` fields are dead; the pricing catalog has zero production callers.

Found en route, fixed here: `Session.Meta()` stamps `CreatedAt: now` on every call (session_state.go:97), and every autosave persists it — the on-disk creation time is clobbered continually.

## Decisions

1. **Tokens only in v1 (Jesse)** — no dollar cost; fast-follow behind the same fields.
2. **Event-driven + 30s fallback (Jesse)**; the header poller is deleted unconditionally.
3. **Persist the running totals (v2, forced by round 1).** v1's derive-never-persist principle is structurally broken: compaction truncates the in-memory history the sum would read (`ResumeHistory` returns compaction-turn-onward, transcript_read.go:297-301), forks copy parent prefix turns verbatim with their `Usage` and stale timestamps (fork.go:154-165), the transcript's one-entry-per-turn shape cannot span durations (apptranscript.go:552-559), and ended sessions have no source at all — both hub past paths build from `SessionMeta` alone (app_threadread.go:152; web_workspace.go:351-368). One mechanism fixes all four: **`SessionMeta` gains `CumUsage {inputTokens, outputTokens, cacheReadTokens, totalTokens}` and `WorkMillis`**, updated by the accumulators and persisted by the existing autosave. Restore reads them back (no derivation); ended sessions get metrics for free through the meta they already read; forks zero their accumulators at `ForkSession` (a child's work is its own).
4. **Work time counts every turn's wall-clock, including interrupted and failed turns (v2).** The v1 settle-site accumulator was doubly wrong (round-1 F3): the drain-loop settle is unreachable on interrupt/error (session_lifecycle.go:508-516) and fires once per *drain*, not per turn (the follow-up/queue/goal `continue`s at :552-621 bypass it). Accumulation moves to the per-turn terminal transition — `finishProcessingAtBoundary`, the one choke point every turn outcome passes through — using a per-turn start the Session captures when processing begins. A 10-minute turn the user interrupts was still 10 minutes of work.
5. **Scope is this session only, and the label says so (v2).** `cumUsage` counts only the session's own round-loop responses: subagent children accumulate in their own Sessions, compaction summarization and the namer call the model outside `AddUsage` (round-1 F5). v1 shipping self-only totals is correct-and-labeled ("this session"); a subagent-inclusive rollup is a named fast-follow. Persisting the accumulator (Decision 3) also removes the live-vs-restore drift F5 flagged around `skipHistory` responses.
6. Carried: turn duration = wall-clock including tool execution; sidecar metrics are snapshot-only (context-pressure precedent); docs-first sequencing.

## Semantics

- **Current-turn elapsed**: wall-clock since the active turn started ("working · 34s"). Live only.
- **Turn duration**: StartedAt → terminal transition, including tool execution and interrupted/failed outcomes (Decision 4). Pure model latency stays a doctor concern.
- **Session work time**: `WorkMillis` — the persisted sum of this session's turn wall-clocks; clients add the in-flight turn's live elapsed on top.
- **Tokens**: persisted cumulative self-only totals (`↑12.4k ↓3.1k` uncached-input/output shown; cache-read/total in detail surfaces). Provider-truth, never estimated.

## Daemon & wire

- **Accumulation**: the Session stamps `turnStartedAt` when processing begins; `finishProcessingAtBoundary` adds the elapsed wall-clock to `WorkMillis` on every terminal transition; `recordResponseUsage` keeps feeding `cumUsage`, which now also lands in `SessionMeta` at autosave. Fork zeroes both in the child's meta. Restore reads both back from meta. **Golden/fixture churn is named work**, and this is a shared surface with WS3's `Origin` field — both specs add `SessionMeta` fields and both touch the persist path; whichever lands second rebases its fixture updates on the first (cross-reference recorded in both specs; round-1 E10).
- **Turn timing on the native fields (round-1 E1/F1 corrected the mechanism)**: the projector holds no per-turn start today — `AppEventProjector` gains `activeTurnStartedAt`, set at every turn start; the agent's turn-end event payload carries the exact duration in milliseconds (same clock as the accumulator), and the projector stamps `CompletedAt` (event timestamp, unix seconds) + `DurationMS` (the payload's exact ms — the seconds-quantization mismatch F1 flagged is thereby avoided) on **all** completion sites, including failed and interrupted turns; the synthetic system-announcement turn gets no timing. Replay: `appTurnsFromNotifications` copies whatever timing the logged notifications carried; the transcript reconstruction stamps `StartedAt` from each entry's `Timestamp` and deliberately omits `DurationMS` — transcript entries are message-records, not turn spans (round-1 E2/F10), and work-time truth lives in the meta, not in replay cosmetics.
- **Wire**: `SerfThread` gains the nested `usage` struct, `workMillis`, and `activeTurnStartedAt` (so the status row needs no turns at all — below); `StatusInfo` gains the same, fed by pull-callbacks in the `SetContextPressureFunc` shape. Ended sessions: `pastEntryThread` and the web past path populate the same fields from the persisted meta — metrics no longer vanish at session end (round-1 E3/F4). Codex-bridged threads: fields absent → clusters hide.

## Web

- **Delete the ghost**: the `.workspace-meta` poll div, its route, the orphaned template, and the skeleton classes. The tests pinning them are deleted and their one live assertion — the async namer's title surfacing via oob — is ported to `/state` (round-1 F11; web_test.go:3146-3187 today).
- **One consolidated status row**, two clusters: state badge + current-turn elapsed + quiet/stall inline; work time + tokens + context gauge + cwd/branch/goal.
- **The elapsed/quiet split, stated correctly (round-1 F2)**: elapsed is server-renderable (`activeTurnStartedAt` is on the wire); **quiet/stall is client-owned entirely** — it is a function of `lastFrameAt`, which only the client has. The relocated liveness logic (20s/180s buckets, stall self-heal, dot pulse, the hub-mirrored `StallThreshold`) keeps its client ticker and simply renders into the status row's spans.
- **The full web data chain, enumerated (round-1 F6)**: `hubapi.SessionDetail` gains usage/workMillis/activeTurnStartedAt; `hubDetailFromAppThread` maps them; `renderInputStrip`'s data map gains the metric keys **and the `Title` key the oob span requires** (it has none today); `input_strip.html` renders the clusters.
- **`/state` goes light (round-1 F7)**: the status row stops fetching transcripts — `RunningFor` derives from the sidecar's `activeTurnStartedAt`, so the endpoint drops `IncludeTurns` entirely and the duplicated `workspaceData` call inside `apiSessionDetail` is deduplicated. The local-roster RunningFor gap (round-1 E6) dissolves with it: no turn scan exists to miss.
- **Refresh cadence (round-1 F8 honest)**: htmx trigger `load, serf-hub:status-refresh from:body, every 30s`; renderer.js dispatches via `htmx.trigger(document.body, …)` — the `sidebar:refresh` precedent; the `serf-hub:thread-status` event the v1 cited is dispatched on `document` and would never reach a `from:body` listener (round-1 E7) — on `thread/status/changed`, `turn/started`, `turn/completed`, **plus a client-side 10s tick while a turn is active** so the context gauge doesn't freeze mid-turn (v1 would have left it stale up to 30s during exactly the moments it moves; the tick costs one light `/state` per 10s only while working).
- **Title latency, stated (round-1 E8/F9)**: the common case (namer lands during the first turn) refreshes at that turn's boundary; a rename landing while idle waits for the 30s fallback — accepted and stated, versus 2s today.

## TUI

`hubSessionDetail` gains `WorkMillis` + usage + activeTurnStartedAt from the sidecar over the existing push-plus-refetch transport; chip strip gains work time + tokens (the TUI's own consolidation precedent); details drawer gains the full breakdown. Dashboard unchanged.

## Error handling

Old daemons: sidecar fields absent → clusters hide. Zero-usage responses: accumulator adds zero. Meta write failures: autosave's existing semantics (metrics lag one autosave, never block a turn). Restore with legacy metas (no new fields): totals start at zero and the fields appear on first autosave — stated, not silent. Fork children start at zero by construction.

## Out of scope

Dollar cost; subagent-inclusive usage rollup (fast-follow; Decision 5); per-turn "took Ns" transcript badges (data now exists; rendering is WS6); the task-status-row's 5s poller (WS6); a namer-rename push event (the 30s idle bound is accepted).

## Testing

- Daemon: per-turn terminal accumulation — completed, **interrupted, failed** turns all count; **each turn of a multi-turn drain counts** (the settle-site bug's regression test); meta persistence round-trip + golden updates (coordinated with WS3's Origin); fork zeroing; restore-reads-meta (compacted session keeps its totals — the derivation bug's regression test); Meta CreatedAt stability across autosaves; projector `activeTurnStartedAt` + timing stamped at every completion site incl. failed/interrupted, absent on the synthetic turn; replay copies; transcript reconstruction omits DurationMS.
- Web: ghost removal + ported oob-title-on-`/state` assertion; light `/state` (no transcript fetch — asserted); dispatch mechanics (`htmx.trigger(document.body)` reaches the `from:body` listener; a `document.dispatchEvent` regression test); 10s active-turn tick present, absent when idle; elapsed server-rendered, quiet/stall client-rendered; metric keys + Title through the enumerated chain; ended-session `/state` shows persisted totals.
- TUI: chip-strip width/content; drawer breakdown.
- e2e card: turn completes → tokens/work-time advance across web + TUI; interrupt a long turn → work time still advances; restart daemon → totals survive; session ends → totals still render.

## Estimate

~700–1,000 loc including tests: daemon (meta fields + goldens, terminal-boundary accumulation, projector state + five sites, event payload, restore/fork) ~350–500; web (ghost deletion + test ports, chain plumbing incl. Title, light `/state` + dedup, hybrid refresh + active tick, liveness relocation) ~250–350; TUI ~100–150.

## Review log

**Round 1** (v1→v2, E 10 / F 12 — F wins; ~16 distinct): the projector holds no per-turn start and the v1 mechanism claim was inverted — the completion event's timestamp is the end, not the start (E1/F1 → projector state + event-payload ms + all five sites); the transcript's one-entry-per-turn shape cannot span durations (E2/F10 → replay omits DurationMS; work-time truth moves to meta); ended sessions lose all metrics (E3/F4 → persisted meta fields, the v2 pivot); compaction truncates and forks inherit the history the restore-sum would read (E4/E5 → persistence + fork zeroing); RunningFor's local-path claim needed plumbing v1 never named (E6 → dissolved by the sidecar's activeTurnStartedAt); the cited dispatch precedent never reaches a `from:body` listener (E7 → htmx.trigger); title-latency regression and dropped test coverage (E8/F9/F11 → ported assertion + stated bounds); estimate honesty (E9); WS3 SessionMeta coordination (E10 → cross-referenced both ways); the settle site misses interrupted turns and all but the last turn of a drain (F3 → terminal-boundary accumulation, Decision 4); "session totals" scope was undefined while excluding subagents/compaction/namer (F5 → Decision 5, self-only labeled); the web data chain and the missing Title key (F6); `/state` kept the full-transcript read and double workspaceData the spec indicted (F7 → light endpoint); mid-turn context-gauge freeze (F8 → 10s active tick); RunningFor mischaracterization (F12 → corrected).
