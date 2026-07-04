# Attention & Status Model — Design (v4)

Date: 2026-07-03 (v4 after three adversarial review rounds)
Status: Approved pending Jesse's spec review
Workstream: 1 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

When an agent ends its turn — question or not — the thread shows plain gray **Idle**, because `agent.SessionState` collapses "waiting for your reply" and "genuinely idle" into one value (agent/session_state.go:13). The wire `ThreadStatusAwaiting` is unreachable for serf sessions, so the NeedsYou tier never lists them, notifications key on impossible transitions behind a 5s poll with all channels off, and nothing pushes sidebar updates for unopened sessions.

## Decisions (Jesse, 2026-07-03)

1. Inbox semantics: a finished turn puts the ball in the user's court.
2. **No read-tracking** (seen-store punted). Clearing is by user action only.
3. **Never decay.**
4. Notification defaults: tab-title count + favicon ON; OS + sound opt-in.
5. **Daemon-truth** (v3 pivot, review-validated): the daemon reports `awaiting` on the existing Codex-shaped status field. Round-3 reviewers confirmed the thesis's core: the tier/rank/gating pipeline consumes `"awaiting"` today, and send/steer already work in that state across appwire (`Send: !active && !closed`, appwire_runtime.go:555), the web composer, and the TUI — no capability rework needed.

**Owned consequence (sign-off carried).** Every live, settled, unarchived session you haven't replied to is amber; the badge counts them all; each completed turn re-arms and (if opted in) re-notifies. Async wakes (a subagent finishing later, a job notification) legitimately re-run the session and re-arm on completion — that is "it's your turn again," not a bug. Archive/shutdown are the triage verbs; mark-seen is the future pressure valve.

## Semantics

| Level | Color | Normalized status |
|---|---|---|
| `working` | blue | `active` |
| `needs_you` | amber | `awaiting`, `warning` |
| `error` | red | `errored` |
| `idle` | gray | everything else, including all non-live sessions |

**The `errored` un-collapse (round-3 findings A3/B1-B3).** `NormalizeState` today folds `systemError → "awaiting"` (tree.go:247), which made v3's distinct red level unimplementable: `AttentionRank` has no error case, the tier node hard-codes `"awaiting"`, and no client-facing source ever emits an error string — so the red favicon and `active→errored` notification paths in notifications.js are dead code today. v4 makes `errored` a first-class normalized state: `NormalizeState` emits `"errored"` for `systemError`; the NeedsYou filter (tree.go:602) admits `awaiting | warning | errored`; `AttentionRank` ranks `errored` above `awaiting`; tier sort becomes (rank desc, oldest first) — that is what "errors first" costs, and it is budgeted, not free. The client string `"errored"` already exists (notifications.js topState/favicon), so hub and client vocabularies converge. `warning` joins the tier via the same one filter edit (v3 listed it amber but the tier excluded it — round-3 A9).

**needs_you rule (daemon-owned):** live, top-level, unarchived session whose status is `awaiting` (rules below), `warning`, or passthrough. **Clearing:** send or steer (daemon flips to `active`), archive, shutdown. Queue is not a clearing action (Conflicts on non-active). Opening does not clear; never decays; un-archive/re-probe re-arms — intended.

**Vocabulary:** pills and settings copy say "Needs you" (web `stateLabel` currently says "Awaiting", web_format.go:184 — small budgeted edit, not "no work"); subagents excluded from independent attention as today (tree.go:614).

## Daemon: the `awaiting` state

`SessionState` gains `SessionAwaiting`. Round 3 relocated and sharpened the state machine (findings A1/A2/A4/B4/B5):

- **Arming site: inside the agent, not the serve loop.** The terminal state is written by the agent's `finishProcessingAtBoundary` family (13 call sites funneling into one function) and rides `EventSessionEnd{State}` to the bridge **before** `ProcessInputKind` returns — so serve-loop arming would race the bridge's forward (`bridge.go:42`) and lose. The boundary function itself computes idle-vs-awaiting; its 13 callers are untouched. Writers: the boundary function, the interrupt path (user move → `idle`), and resume-init. The serve loop mirrors (`serve.go:421`); the bridge forwards.
- **Arming rule.** At boundary settle (after `armGoalContinuation`): `awaiting` iff the completed turn produced agent output AND no autonomy is in flight — no scheduled goal continuation, **no live child subagents** (a delegating parent stays `working`-family while its child runs — round-3 A1's false-amber case), no buffered notifications, no queued input. All four are agent-internal knowledge, checkable synchronously.
- **Async arrivals re-arm by design.** A notification landing after arming wakes the session (`active`) and its completion re-evaluates the same rule. Micro-windows between autonomous kicks are invisible to the 5s-sampled watcher and harmless in the relay stream. The spec claims suppression only for autonomy knowable at the boundary — not for async futures (round-3 A1/B4 corrected v3's overclaim).
- **Resume/restart (explicit rules replacing v3's continuity claim — round-3 A2).** Queue and notification buffers are not persisted, and restored goals are deliberately not re-kicked ("loaded but idle," session_init.go:405). Resume therefore evaluates: transcript tail says agent moved last AND no live subagents restored → `awaiting`. A restored mid-goal session **is** `awaiting` on purpose: nothing will move until the user acts, and amber is what surfaces the stall (the alternative — treating a dormant goal as autonomy-pending — would silently hide a stalled loop as "working" forever).
- **Interrupt** → `idle`; **`/compact`**/self-compaction never touch state; **dormant** → `idle`.
- **Gating verification (reframed from v3's audit — round-3 B11).** Reviewers verified the not-busy gates key on `!active`/`processing`, not `== idle`, so `awaiting` sessions can already send/steer/queue-fallback everywhere. The remaining task is a verification checklist pinning that with tests (appwire Send gate, web composer enable, TUI composer modes, spawn/fork/resume eligibility), not an open-ended audit.

`appStatus()` passes `awaiting` through today (appwire_runtime.go:623). Old daemons keep reporting `idle` and stay quiet.

## Hub

- **Wedge unification, honestly costed (round-3 B6):** `sanitizeStaleProcessingStatus` needs Past lookups + a transcript tail read, so the watcher cannot apply it "map-only." The shared heuristic runs in tree build and list/read enrichment as before, and the **watcher probes only sessions sampled `active` past the stall threshold** (~3min — the heuristic's own constant): a small set, bounded disk work, no per-tick sweep.
- **Watcher differ inputs (round-3 A5/B7):** `topLevel`, project, and auto-archive age are meta-derived, so the differ consumes a compact **meta-projection cache** (`id → {isSubagent, project, lastActivity}`) refreshed on past-index rebuilds, rendezvous events, and archive-handler pushes — not `Past.AllMetas()` per tick. Per-source async collectors with per-source timeouts; fsnotify debounced; empty-but-successful remote lists get one tick of hysteresis.
- **Broadcast:** `serf/attention/changed` via `BroadcastAll` with `changed[]` + `summary`. The **summary is authoritative for badge counts** and is computed hub-side from the tier-eligible set (top-level, unarchived — same definition as NeedsYou), which resolves the `/api/tree`-population mismatch (subagents, remote sources) flagged in round 3 (A7/B8). Restart re-seeds silently.

## Web client

- Sidebar/NeedsYou/rollups: the `errored` lane edits above; `serf/attention/changed` joins the refresh allowlist. Rollup `◆` counts the tier-eligible set (`awaiting|warning|errored`) — note v3's "add systemError at tree.go:464" was wrong twice over: the collapse meant it was already counted, and post-un-collapse the switch gains the `errored` case instead.
- **notifications.js rewrite:** delete the 5s poll. Baseline = one `/api/tree` fetch on connect for initial paint; thereafter **counts come from `summary` in each `serf/attention/changed`** — no per-change tree refetch (round-3 A7). It also ingests the per-thread `thread/status/changed` relay events it already has access to via `onNotification`, so replying in an open thread clears the title/favicon instantly; the ≤5s watcher floor applies only to threads open nowhere (this was false in v3 — round-3 A6/B9 — and is now made true by the added ingestion, not by wishing). No baseline → no edge-firing. OS + sound on transitions into `needs_you`/`error`; focused-tab suppression; **multi-tab leader election** (Web Locks, localStorage-CAS fallback) so one tab fires.
- **Prefs migration:** versioned one-time backfill — existing non-empty blob gets absent keys as explicit `false`; wholly absent blob gets new defaults (title+favicon on). (`commit()` merges single keys, settings.js:59 — partial blobs are real.)
- Settings copy + pill label: "Needs you" vocabulary.

## TUI

`stateLabel`/`statusDot` already handle `awaiting` (hub_dashboard_view.go:507,492 — v3 overstated this gap). Remaining: map `systemError`/`errored` (label, dot, and `attentionRankLabel`, which currently ranks it 0 on the raw-status path) and add the `◆` needs-you count. No wire changes.

## Error handling

Old daemons degrade to today's behavior. Watcher: per-source isolation, panic recovery, baseline kept on failed cycles. Boundary-function arming is covered by the single-writer rules above; resume-init recompute is defensive after crash-mid-turn.

## Testing

- Agent boundary table: {user, steer, continuation, notification, interrupt, compact, resume, dormant, crash-mid-turn, live-subagent-running, restored-mid-goal} × {queue, goal, notif-buffer} → expected state, asserted at `/status` AND appwire (same value by construction; test pins it).
- Gating checklist tests: send/steer/queue-fallback/spawn/fork against an `awaiting` session on appwire, web, TUI.
- `errored` lane: NormalizeState, tier admission, rank ordering (errored > awaiting), rollup count, web pill/favicon red, TUI label+rank — cross-surface agreement test covers all three surfaces.
- Watcher: one broadcast per change; re-seed emits nothing; per-source timeout isolation; hysteresis; debounce; targeted wedge probe only for >threshold actives; summary == tier-eligible count.
- jstest: baseline-before-edge, summary-driven counts, relay-status ingestion (instant badge clear), prefs migration matrix, leader election, focused suppression.
- e2e card (split per round-3 A6): spawn → agent completes a turn → sidebar row + tier amber and title count increment without opening (≤5s) → reply in the open thread → row/tier amber clears instantly via `thread/status/changed` AND title/favicon clear via the ingested relay event; the watcher broadcast follows as reconciliation.

## Out of scope

Read-tracking (punted; protocol-clean path unchanged: hub-inferred cursor + hub-terminated `serf/thread/seen/set`), ask detection (job-control's `ask` lands as another `awaiting` producer; job-control's background jobs will slot into the same autonomy-in-flight suppressors), subagent attention propagation, per-message read receipts, background push, workstreams 2–6 (recorded: keep ⌘↵ + setting; project-level delete only).

## Estimate

~1,000–1,400 loc including tests: agent boundary function + suppressors + resume rules + gating tests ~300–450; hub `errored` lane (NormalizeState/rank/tier/rollup) + watcher/differ + meta-projection cache + summary ~350–500; web client ~250–350; TUI ~50–80.

## Review log

**Round 1** (v1→v2): derivation input didn't exist as claimed; single shared derivation; watcher beyond roster; snapshot RPC; relay-ingestion; queue dropped from clearing; warning mapped; ⟳ corrected; subagent absorption→exclusion; interrupt=user move; per-key defaults; lit-badge consequence owned; estimate raised.

**Round 2** (v2→v3, 6–6 tie): read path couldn't carry the sidecar bool; relay ingestion had no in-process channel; string-keyed tier sites wouldn't consume a parallel field (decisive pro-pivot finding); automation false-amber → pending-autonomy rule; settings.js migration claim false; watcher cost/flap; snapshot/diff race; multi-tab dupes; stale TUI ref. v3 pivoted to daemon-truth and deleted the overlay.

**Round 3** (v3→v4, 6–6 tie) attacked v3's own claims: `NormalizeState` systemError→awaiting collapse made the red error level unimplementable and two v3 claims false (A3/B1-B3 → `errored` un-collapse, budgeted); arming mislocated at the serve loop vs the agent's `finishProcessingAtBoundary` + bridge race (A4/B5 → in-agent boundary arming); async subagent notifications and running children defeat the flash-suppression claim (A1/B4 → live-subagent suppressor + honest async-re-arm semantics); restored goals aren't re-kicked and queues aren't persisted (A2 → explicit resume rules, amber-as-stall-surfacing); wedge heuristic incompatible with map-only differ (B6 → targeted probes); differ needs meta-derived inputs (A5/B7 → meta-projection cache); open-thread badge lag falsified v3's latency invariant (A6/B9 → relay-status ingestion in notifications.js, summary-driven counts, e2e split); `/api/tree` refetch cost + population mismatch (A7/B8 → summary authoritative); TUI awaiting already handled (A8/B10); warning excluded from tier (A9 → filter edit); pill label mismatch (A10); SessionIdle audit largely vacuous — send already works in awaiting (B11 → reframed as verification checklist). Estimate raised to fund the errored lane and agent-side work.
