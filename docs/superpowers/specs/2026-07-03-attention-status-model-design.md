# Attention & Status Model — Design (v3: daemon-truth)

Date: 2026-07-03 (v3 after two adversarial review rounds and an architecture re-derivation)
Status: Approved pending Jesse's spec review
Workstream: 1 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Live-verified defect: when an agent ends its turn — question or not — the thread shows plain gray **Idle**, because `agent.SessionState` collapses "waiting for your reply" and "genuinely idle" into one value (`idle`/`active`/`closed` only, agent/session_state.go:13). The wire `ThreadStatusAwaiting` is therefore unreachable for serf sessions, and every attention consumer starves: the NeedsYou tier never lists serf threads, notifications key on transitions that cannot occur (notifications.js:185) behind a 5s poll with all channels off, and nothing pushes sidebar updates for unopened sessions (lazy relay, app_rpc.go:106).

## Decisions (Jesse, 2026-07-03)

1. Inbox semantics: a finished turn puts the ball in the user's court.
2. **No read-tracking** (seen-store punted). Clearing is by user action only.
3. **Never decay**: needs-you persists until acted on.
4. Notification defaults: tab-title count + favicon ON; OS + sound opt-in.
5. **Architecture: daemon-truth over hub overlay** (v3 pivot, after review round 2). The daemon reports `awaiting` on the existing Codex-shaped status field instead of the hub reconstructing it from a sidecar bool. Rationale: once seen-tracking was punted, the predicate became stateless and fully daemon-knowable; and the tree layer is already keyed to the `"awaiting"` string — NeedsYou filter (tree.go:602), AttentionRank (tree.go:131), rollup counts (tree.go:464), NormalizeState (tree.go:243), `hubapi.TreeNode.State`, TUI `Status.Type` (hub_types.go:170) — so daemon-truth lights up the entire existing pipeline, while a parallel field would have required rewriting every one of those sites (round-2 finding A2). Reporting `awaiting` on the standard field is convergence with the Codex app-server protocol, not extension.

**Owned consequence of 1+2+3 (sign-off carried from v2).** Without read-tracking, every live, settled, unarchived session you haven't replied to is amber; the badge counts them all; each completed turn re-arms and (if opted in) re-notifies — "it's your turn again." Archive/shutdown are the triage verbs; mark-seen (below) is the pressure valve if this proves noisy.

## Semantics

Attention **is** the normalized status plus hub-tier suppression — no separate derivation layer:

| Level | Color | Source |
|---|---|---|
| `working` | blue | status `active` |
| `needs_you` | amber | status `awaiting` (serf daemons now produce it; external passthrough unchanged) or `warning` |
| `error` | red | `systemError`, including the stale-wedge heuristic |
| `idle` | gray | everything else, including all non-live sessions |

Hub-tier suppression: archived sessions and subagents never surface attention (subagents excluded as today, tree.go:614 — "a subagent's parent is the actionable unit"; upward propagation is future work with job-control). Ranking in the NeedsYou tier: `error` first, then oldest first. Live-only rule carried from v2: ended sessions are `idle` (an ended+never-decay+no-seen inbox would permanently amber all history). Un-archive or re-probe of a still-unanswered session re-arms — intended.

**Clearing:** send or steer (the daemon flips `awaiting → active` on user input), archive, shutdown. Queue is not a clearing action (`turn/queue` Conflicts on non-active sessions). Opening/reading does not clear. Never decays.

**Vocabulary:** pills, settings copy, notification text speak the level names ("Needs you"). Rollup `◆N` counts `awaiting`+`warning`+`systemError` (today it omits systemError, tree.go:464 — small edit); `⟳N` already counts only `active` (unchanged).

## Daemon: the `awaiting` state

`agent.SessionState` gains `SessionAwaiting = "awaiting"`. This is the whole feature's truth source, so its transition rules are the spec's core (review findings A3/B5 shaped these):

- **Arming.** At the serve-loop turn boundary (cmd/serf/serve.go:418): when a turn completes with agent output, the session persists, **and no autonomous input is pending** — input queue empty, no scheduled goal-engine continuation, no buffered subagent notifications — the state becomes `awaiting`. The pending-autonomy check is what prevents amber flashing between goal-loop iterations and notification-drain turns: an autonomously continuing session reads as still-working, while a goal that completes and stops correctly lands `awaiting` (the ball genuinely returns to you).
- **Entry kinds.** `USER_INPUT`/`STEERING` are user moves; `EntryContinuation`/`EntryNotification` (server/server.go:486,499) are autonomous, not user moves — their turns run `active` and their completions follow the same arming rule.
- **Interrupt** is a user move → `idle` (you know you interrupted; the event-bridge interrupt path sets it explicitly rather than falling through, bridge.go:44).
- **`/compact`** and self-compaction run outside the turn boundary (serve.go:321, session_compaction.go) and never touch the state.
- **Dormant** (no completed turns) → `idle`.
- **Resume/daemon-restart:** state is recomputed at init from the transcript tail plus the pending-autonomy check, so `awaiting` survives restarts (the transcript is append-only; compaction appends, session_compaction.go:119).
- **Single-writer discipline** (finding A3 noted two status writers): only the serve-loop boundary, the interrupt handler, and resume-init may set `awaiting`/`idle`; the bridge otherwise forwards.
- **Audit gate:** every `SessionIdle` / `"idle"` comparison in the repo gets an explicit ruling — *not-busy* (now `idle || awaiting`: composer/capability gating, spawn/resume eligibility, TUI modes) vs *nothing-pending* (`idle` only). Call sites are bounded (bridge.go ×4, serve.go ×3, plus consumers); the audit list is a named implementation-plan task and its rulings land as tests.

`appStatus()` already passes `awaiting` through to the wire (appwire_runtime.go:623 — dead plumbing comes alive). **Deleted from v2:** the `/status` `awaiting_input` bool, all prober/roster/LiveEntry plumbing, `DeriveAttention()`, `SerfThread.Attention`, and the `serf/attention/get` snapshot RPC. The prober keeps decoding `{session_id, state}` — the state string simply becomes honest. Mixed-version fleets: old daemons keep saying `idle` and stay quiet.

## Hub

- **Stale-wedge unification (kept from v2):** `sanitizeStaleProcessingStatus` currently runs only in the appwire list path for local sources (app_threadlist.go:40), so a wedged session shows red in the workspace but blue in the sidebar. The heuristic moves to a shared hubcore spot applied by tree build, list/read enrichment, and the watcher alike.
- **Attention watcher (fan-out only, no recomputation of the world):** per-source async collectors — local sessions ride the existing roster probe (now truth-carrying); remote sources are polled per-source with per-source timeouts so one slow Codex endpoint cannot stall local updates (findings A5/B6); fsnotify events are debounced. The differ operates on an `id → (state, archived, topLevel)` map only — no `BuildTree`, no `Past.AllMetas()` per tick. Empty-but-successful remote lists get one tick of hysteresis before dropping threads, so a source restart doesn't re-arm amber and re-fire notifications (finding A5). Archive mutations feed the differ directly from the `/api/archive` handler. On change: **`serf/attention/changed`** via `BroadcastAll` (server.go:76, `notifyAuthUpdated` pattern) with `changed[] {threadId,title,project,level,prevLevel}` + `summary {needsYou,error,working}`. Restart re-seeds silently.
- **Instant clear needs no new machinery** (dissolves v2's relay-ingestion, findings A1/B3/B4): when you reply, the daemon flips `awaiting → active` and the existing per-thread relay already broadcasts `thread/status/changed` to the open workspace, whose sidebar-refresh allowlist already includes it. The watcher's 5s tick is the latency floor only for threads open nowhere.

## Web client

- Sidebar/NeedsYou/rollups: no structural work — the tier and rank sites consume `awaiting` today. `serf/attention/changed` joins the refresh allowlist.
- **notifications.js rewrite:** delete the 5s `/api/search` poll. Baseline = one `/api/tree` fetch on connect (no new RPC); **no baseline → no edge-firing** (a transition arriving in the connect window updates counts but never fires OS/sound — finding A6/B7); counts always recompute from the latest fetch, so missed-during-reconnect transitions self-heal silently (parity with today's poll). OS + sound fire on transitions into `needs_you`/`error`, suppressed while the hub tab is focused (window-level; per-thread visibility was punted with mark-seen). **Multi-tab dedup:** Web Locks (localStorage-CAS fallback) elects one firing tab, so N open tabs produce one OS notification, not N−1 (finding A7).
- **Prefs migration (corrects v2's false claim — finding A4):** `commit()` merges single keys (settings.js:59-61), so partial blobs exist. One-time versioned migration: an existing non-empty prefs blob gets absent keys backfilled as explicit `false` (legacy users keep exactly the behavior they chose under old defaults); a wholly absent blob gets the new defaults (title+favicon `true`).
- Settings copy: unified vocabulary ("Native notification when a thread needs you or errors.").

## TUI

No wire changes at all — `hubNodeFromThread` already carries `thread.Status.Type` (hub_types.go:170). Work shrinks to: `stateLabel`/dot mappings for `awaiting` and `systemError` (hub_dashboard_view.go:505 currently lacks both), and a `◆` needs-you count in the dashboard.

## Error handling

Old daemons degrade to today's quiet behavior. Watcher: per-source isolation, panic recovery, failed cycles keep the previous baseline. State transitions are covered by the single-writer rule; resume-init recompute is defensive against crashes mid-turn (a crash during `active` resumes to recomputed truth, not stale `active`).

## Testing

- Daemon state-machine table: {user, steer, continuation, notification, interrupt, compact, resume, dormant, crash-mid-turn} × {queue empty/pending, goal pending/complete} → expected state. `/status` and appwire both assert the same value.
- `SessionIdle` audit rulings land as compile-checked tests (not-busy sites accept `awaiting`).
- Cross-surface pin: sidebar tree state == thread/read status for the same session (now same source by construction; test prevents regression).
- Watcher: one broadcast per change; restart re-seed emits nothing; per-source timeout isolation; empty-success hysteresis; fsnotify debounce.
- jstest: baseline-before-edge rule, prefs migration matrix (absent blob / partial blob / explicit false), leader election, focused suppression.
- e2e scenario card: spawn → agent completes a turn → sidebar row + NeedsYou tier amber + title count increments without opening the thread (≤5s floor) → reply in the open thread → amber clears immediately via `thread/status/changed`.

## Out of scope

Read-tracking (punted; protocol-clean upgrade path unchanged from v2: hub-inferred cursor from `thread/read` traffic + hub-terminated `serf/thread/seen/set`, `serf/*` namespace, daemon hop untouched), question/ask detection (job-control's `ask` becomes just another `awaiting` producer), subagent attention propagation, per-message read receipts, background push, workstreams 2–6 (recorded decisions: keep ⌘↵ + add setting; project-level delete only).

## Estimate

~800–1,150 loc including tests: daemon state machine + audit + resume recompute ~250–400, hub (wedge unification + watcher/differ + broadcast) ~250–400, web client ~200–300, TUI ~30–60.

## Review log

**Round 1** (two adversarial reviewers, findings verified then folded into v2): derivation input didn't exist as claimed (A1/B1); single shared derivation proposed for three divergent sites (A4/B2/B3); watcher widened beyond roster for external awaiting (B5); snapshot RPC added (A3/B4); relay-ingestion instant-clear added (A2); queue dropped from clearing (A5); warning mapped in (A7); ⟳ claim corrected (A9); subagent "absorption" corrected to exclusion (A10); interrupt = user move (A11); per-key defaults pinned (A12/B9); lit-badge consequence surfaced (B6/B1); estimate raised (B11).

**Round 2** (fresh reviewers, prior findings excluded; scored a 6–6 tie) invalidated much of v2's own scaffolding and triggered the v3 pivot: read path couldn't carry `awaiting_input` (B2); relay ingestion had no in-process channel and fought relay teardown (A1/B3/B4); string-keyed tier/rank/rollup sites wouldn't consume a parallel field (A2 — the decisive pro-pivot finding); automation turns would false-amber and "no behavior change" was overstated (A3/B5 → the pending-autonomy arming rule); settings.js migration claim was factually false (A4 → versioned backfill migration); watcher cost/head-of-line blocking + empty-success flap (A5/B6 → per-source collectors, hysteresis, map-differ); snapshot/diff race (A6/B7 → baseline-before-edge rule, snapshot RPC deleted); multi-tab duplicate OS notifications (A7 → leader election); stale TUI field reference (A8/B1 → moot, field deleted). v3 deletes the `awaiting_input` field, prober/roster plumbing, `DeriveAttention`, `SerfThread.Attention`, snapshot RPC, and relay ingestion; daemon-truth supersedes them.
