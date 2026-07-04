# Attention & Status Model — Design (v2, post-adversarial-review)

Date: 2026-07-03 (v2 same day, after two-reviewer adversarial pass)
Status: Approved pending Jesse's spec review
Workstream: 1 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Live-verified defect: when an agent ends its turn — question or not — the thread shows plain gray **Idle**. The daemon collapses "asked and waiting" and "finished the task" into the same `SessionIdle`: `agent.SessionState` has only `idle`/`active`/`closed` (agent/session_state.go:13), so the wire `ThreadStatusAwaiting` is unreachable for serf sessions. Every attention consumer starves:

- The NeedsYou sidebar tier (internal/hubcore/tree.go:592) never lists serf threads.
- OS notifications key on transitions (`idle→awaiting`, `active→errored`) that cannot occur (assets/notifications.js:185); channels poll `/api/search` every 5s; all default off.
- Push gap: with zero open workspaces, nothing pushes sidebar updates (per-thread relay is lazy, app_rpc.go:106).

This design makes "the ball is in your court" a first-class, hub-derived state. It does **not** detect questions; under the chosen inbox semantics, a question-ending turn and a task-ending turn are identical — both await your move.

## Decisions (Jesse, 2026-07-03)

1. Inbox semantics: any finished turn puts the ball in the user's court.
2. **No read-tracking** in this workstream (SeenStore / `markSeen` / visibility detection punted). Clearing is by user action only.
3. **Never decay**: needs_you persists until acted on.
4. Notification defaults: tab-title count and favicon tint ON; OS notification and sound opt-in.
5. This workstream ships first; other diagnostic workstreams follow separately.

**Owned consequence of 1+2+3 (review finding, sign-off required).** Without read-tracking, `needs_you` is a stateless derivation: *every* live, settled, unarchived session you haven't replied to is amber, and the tab badge counts them all. Reply-clearing is suppression-until-the-agent-moves-again, not durable acknowledgment: each time a session finishes another turn, it re-arms and (if opted in) re-notifies — which is exactly "tell me when it's my turn again," but users who keep several idle daemons alive will see a permanently lit badge until they archive or shut sessions down. Archive/shutdown are the triage verbs. If this proves noisy in practice, the mark-seen upgrade (below) is the pressure valve; we accept the trade now rather than build read-tracking speculatively.

## Semantics

One hub-derived **attention level** per session, computed by a **single shared derivation function** (see Architecture):

| Level | Color | Meaning |
|---|---|---|
| `working` | blue | Daemon reports `active`. |
| `needs_you` | amber | Live session, ball in the user's court (rule below), or wire `awaiting`/`warning`. |
| `error` | red | `systemError`, including stale-stream wedge detection. |
| `idle` | gray | Everything else, including all non-live sessions. |

**needs_you rule.** A session is `needs_you` when all of:
1. Live (daemon running), top-level, not archived.
2. Status is not `active` and not `systemError`.
3. The daemon reports **`awaiting_input: true`** (new field, below): the session has at least one completed turn and the most recent turn-ending move was the agent's — no user send/steer followed it. Interrupting a turn is a user move (interrupt ⇒ `awaiting_input=false` ⇒ gray; the user knows they interrupted). A dormant session with no turns is never `awaiting_input`.
4. Or, regardless of 3: the wire status is `awaiting` or `warning` (today reachable only via external source passthrough; `warning` already counts as attention in the current UI, tree.go:464, and keeps that meaning).

**Clearing.** Send or steer a message (a user move flips `awaiting_input`), archive, or shut down. Queueing is **not** a clearing action: `turn/queue` is rejected on non-active sessions (server/appwire_runtime.go:243), so it can never be issued against an amber thread. Opening/reading does not clear (decision 2). Amber never decays with age (decision 3). Un-archiving or re-probing a still-unanswered session re-arms amber — intended: it still needs you.

**Live-only rule (flagged in v1, unchanged).** Ended sessions are `idle`. With decisions 2+3, admitting ended sessions would permanently amber nearly all non-archived history on ship day. Ended-with-unseen joins the inbox only when mark-seen lands.

**Ranking.** Within the NeedsYou tier: `error` first, then oldest `needs_you` first (preserves tree.go:638).

**Subagents.** Excluded from independent attention, as today (tree.go:614 skips them: "a subagent's parent is the actionable unit"). No upward propagation exists today and none is added; propagation is future work alongside job-control.

**Vocabulary unification (rides along).** Pills, settings copy, and notification text use the level names ("Needs you", not `awaiting`/`processing`). Rollup badges keep current semantics — `⟳N` already counts only `active` sessions (tree.go:466; the v1 claim that it needed redefining was wrong); `◆N` becomes the `needs_you`+`error` count.

## Architecture

**0. One daemon status field (revised from v1's "no daemon changes").** v1 claimed the hub already holds last-turn data; review verified it does not — the roster prober decodes only `{session_id, state}` (internal/hubcore/prober.go:19), `/status` (`server.StatusInfo`) has no turn-kind, rendezvous entries are static, and `ListThreads` doesn't populate turns (appwire_runtime.go:482). The daemon therefore gains one **additive** field, `awaiting_input bool`, in the `/status` payload — computed at turn boundaries from state the daemon already owns; no behavior change. Plumbing: `StatusInfo` → prober `statusInfo` + `Probe` signature (roster.go:34) → `LiveEntry` → derivation. This is the honest cost of the feature; the estimate includes it.

**1. Single shared derivation.** One function in `hubcore`: `DeriveAttention(status, awaitingInput, archived, live, topLevel, staleWedge) → level`. All three consumer paths call it with the same inputs:
- sidebar/tree rendering (`BuildTree`),
- wire-field stamping in the thread-list/read path,
- the attention watcher.

The stale-stream wedge heuristic **moves into this layer**: today `sanitizeStaleProcessingStatus` runs only in the appwire list path, only for local sources with a Past index (app_threadlist.go:40), so a wedged session would show red in the workspace but blue in the sidebar and never notify. Centralizing it is what makes "one vocabulary, all surfaces" true rather than aspirational (review findings A4/B2/B3).

**2. Attention watcher — full input set, not roster-only.** The watcher diffs the attention map on the roster cadence (fsnotify + 5s tick, roster.go:242), but computes it over the **same inputs the sidebar uses** (`navigationTreeInputs`: roster + past metas + remote/Codex source listings, web_api_tree.go:107) — a roster-only watcher would be blind to exactly the external sources that can emit genuine `awaiting` (finding B5). It also ingests live relay notifications: when an open thread emits `turn/started`/`turn/completed`, the watcher re-derives that thread immediately, so replying to an open thread clears amber instantly instead of at the next tick; the 5s tick is the latency floor only for threads nobody has open (finding A2). On change it emits **`serf/attention/changed`** via `BroadcastAll` (internal/appserver/server.go:76, mirroring notifyAuthUpdated, app_rpc.go:667). Restart re-seeds the baseline silently. Payload:

```json
{
  "changed": [{"threadId": "...", "title": "...", "project": "...", "level": "needs_you", "prevLevel": "working"}],
  "summary": {"needsYou": 3, "error": 1, "working": 2}
}
```

**3. Initial snapshot RPC.** `serf/attention/get` returns the current map + summary. Clients call it on connect and reconnect — without it, deleting the poll leaves the default-ON title/favicon blank on every fresh load until something changes (findings A3/B4).

**4. Wire field.** `appwire.Thread.Attention` (level string), stamped by the hub in the thread-list/read path via the shared derivation. Daemon never sets it. The TUI cannot import `internal/hubcore` (verified), so the wire field is its only clean path.

**5. No new stores.** Nothing persists. The punted mark-seen upgrade is additive: derivation gains a seen-cursor input, clearing gains "seen", ended sessions can join the inbox.

## Web client

- **Sidebar**: rows, rollups, NeedsYou tier render from the attention level; `serf/attention/changed` joins the refresh allowlist (sidebar.js:288).
- **notifications.js rewrite**: delete the 5s poll; on connect call `serf/attention/get`, then apply `serf/attention/changed` deltas. OS + sound fire on transitions **into** `needs_you`/`error`, suppressed when the hub tab is focused (`document.hasFocus()` — window-level: while you're looking at the hub, the sidebar and title are the signal; per-thread visibility was punted with mark-seen). Title count + favicon read `summary`. Defaults per-key: an absent key gets the new default (title/favicon ON), an explicitly stored `false` is respected (settings.js writes all keys explicitly on save, so legacy saved prefs are unaffected).
- **Settings copy**: unified vocabulary ("Native notification when a thread needs you or errors.").

## TUI (minimal slice)

Consume `Thread.Attention` in `hubNodeFromThread` (hub_types.go:151); render `◆` marker + needs-you count in the dashboard. Removes the TUI's divergent state labeling for attention cases (hub_dashboard_view.go:505 lacks the `systemError` mapping the web has).

## Error handling

- Watcher recovers from panics; a failed probe/list cycle keeps the previous baseline (no flapping).
- Derivation is nil-safe: absent `awaiting_input` (older daemon binaries) degrades to `idle`-family behavior, never blocks a listing — mixed-version fleets stay correct-but-quiet.
- `BroadcastAll` failures are per-connection, handled by appserver.

## Testing

- Table-driven derivation tests: status × awaiting_input × archived × live × subagent × wedge matrix.
- **Cross-surface agreement test**: sidebar tree, wire-field stamping, and watcher produce identical levels for the same synthetic inputs (finding B12b).
- Watcher: synthetic diff → exactly one broadcast per change; restart re-seed emits nothing; relay-notification ingestion re-derives immediately.
- `/status` round-trip: daemon sets `awaiting_input` correctly for ask-end, task-end, interrupt, dormant, steered cases.
- Appwire golden updates: new field, new method, snapshot RPC.
- jstest: snapshot-then-delta flow, per-key defaults migration, focus suppression, re-notify-per-turn (asserted as intended behavior).
- e2e scenario card (rewritten per finding B12c): spawn → agent completes **any** turn → sidebar row + NeedsYou tier amber + title count increments without opening the thread (allow the ≤5s watcher floor) → reply → amber clears immediately (thread is open/relayed).

## Out of scope

Read-tracking (punted; upgrade path above), question/ask detection (no ask state exists; the level slot fills when job-control's `ask` lands), subagent attention propagation, per-message read receipts, service-worker/background push, workstreams 2–6 (working-state placement + metrics; thread management incl. project delete; quick wins incl. send-key setting — decisions recorded: keep ⌘↵ default + add setting, project-level delete only).

## Estimate

~950–1,350 loc including tests: daemon field + prober/roster plumbing ~150–250, shared derivation + watcher + snapshot RPC + wire field ~400–550, web client ~250–350, TUI ~50–80.

## Review log

v1 → v2 after a two-reviewer adversarial pass (both read-only, findings verified against code before adoption): derivation input did not exist as claimed — daemon `/status` field added and "no daemon changes" retracted (A1/B1); single shared derivation incl. wedge heuristic replaces three divergent sites (A4/B2/B3); watcher input set widened beyond the roster so external `awaiting` sources are covered (B5); initial-snapshot RPC added (A3/B4); reply-clear made immediate for open threads via relay ingestion, 5s floor documented for unwatched (A2); "queue" removed from clearing — unreachable on idle sessions (A5); `warning` mapped into `needs_you` (A7); ⟳ redefinition dropped as no-op, v1 claim corrected (A9); subagent "absorption" corrected to "excluded, as today" (A10); interrupt classified as user-move ⇒ gray (A11); per-key defaults migration pinned (A12/B9); lit-badge/re-notify consequence surfaced for explicit sign-off (B6/B1); Codex `awaiting` passthrough claim softened — default catch-all, unverified emission (B10); push-gap wording narrowed (A13); estimate raised to include daemon plumbing (B11).
