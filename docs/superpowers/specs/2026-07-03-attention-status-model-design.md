# Attention & Status Model — Design

Date: 2026-07-03
Status: Approved pending Jesse's spec review
Workstream: 1 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

Live-verified defect: when an agent ends its turn by asking the user a question, the thread shows plain gray **Idle**. The `awaiting` state is unreachable for serf daemons — `agent.SessionState` has only `idle`/`active`/`closed` (agent/session_state.go:11), and nothing daemon-side ever produces the wire `ThreadStatusAwaiting`. Every attention consumer starves as a result:

- The NeedsYou sidebar tier (internal/hubcore/tree.go:592) never lists the thread.
- OS notifications fire only on `idle→awaiting` and `active→errored` transitions (assets/notifications.js:185), so even a correctly classified thread notifies on the wrong transition set. Channels poll `/api/search` every 5s and all default off.
- No unread/seen concept exists anywhere.
- Verified push gap: a session with no open workspace never pushes a sidebar update — the per-thread relay is lazy (app_rpc.go:106) and the sidebar refreshes only on notifications for already-relayed threads.

User feedback addressed: "optional browser notifications for 'thread is waiting for you'", "sidebar notifications of the same should be clearer", plus the vocabulary fractures found in the diagnostic.

## Decisions (Jesse, 2026-07-03)

1. Inbox semantics: a finished turn puts the ball in the user's court.
2. **No read-tracking** in this workstream. The SeenStore / `markSeen` RPC / visible-focused-at-bottom detection from the first draft is punted. Clearing is by user action only.
3. **Never decay**: needs-you persists until acted on.
4. Notification defaults: tab-title count and favicon tint ON; OS notification and sound opt-in.
5. This workstream ships first; working-state placement/metrics, thread management, quick-wins, MCP resilience, and the consistency sweep follow separately.

## Semantics

One hub-derived **attention level** per session — the single vocabulary all surfaces speak:

| Level | Color | Meaning |
|---|---|---|
| `working` | blue | Daemon reports `active`. |
| `needs_you` | amber | Live session, ball in the user's court (rule below). |
| `error` | red | `systemError`, including the hub's stale-stream heuristic (app_threadlist.go:254). |
| `idle` | gray | Everything else, including all non-live sessions. |

**needs_you rule.** A session is `needs_you` when all of:
1. It is **live** (daemon running), top-level (subagents never carry their own attention; the parent absorbs it, matching today's NeedsYou behavior), and not archived.
2. Its daemon status is not `active`.
3. Its last completed turn is an agent turn (or the wire status is a genuine `awaiting`, which external Codex sources can pass through), **and no user input followed it** — no sent message, no steering, no queued input.

**Clearing.** Send, steer, or queue a message; archive; or shut the session down. Opening/reading alone does not clear (decision 2). Amber never decays with age (decision 3).

**Live-only correction (flagged for review).** The approved Section 1 draft listed "session ended" as a needs_you producer. Combined with decisions 2 and 3, that would amber-flood history on ship day: nearly every past session ends with agent output, so all non-archived history would go permanently amber. Restricting needs_you to live sessions kills the flood with no backfill epoch hack, and matches the existing NeedsYou tier, which already lists only live sessions. Ended sessions read as `idle`/ended; history hygiene belongs to the thread-management workstream. When mark-seen lands later, ended-with-unseen can join the inbox safely.

**Ranking.** Within the NeedsYou tier: `error` first, then oldest needs_you first (preserves the current oldest-blocked rule, tree.go:638).

**Vocabulary unification (rides along).**
- Pills, settings copy, and notification text all use the level names above ("Needs you", not `awaiting`/`processing`).
- Sidebar project rollup `⟳N` counts **working** sessions only — a live-but-idle daemon no longer shows an activity glyph. `◆N` counts needs_you + error.
- The notifications settings page describes behavior as "when a thread needs you," not state-machine jargon.

## Architecture

No daemon changes. Four hub pieces:

**1. Derivation in `BuildTree`** (internal/hubcore/tree.go). Attention is computed per node from the normalized state plus last-turn data. Inputs the hub already holds: roster live entries and daemon status for live sessions; the last-turn kind ("was the final completed turn an agent turn, with no user input after it") comes from the daemon's status/thread projection — exact field confirmed during implementation planning; if absent, the daemon's `/status` payload gains a `lastTurnKind`/`awaitingInput` boolean derived from data the daemon already has (`server/appwire_runtime.go` builds turns today). Codex sources: honor a raw `awaiting` passthrough (codex_mapping.go:19 already forwards it); otherwise they derive like serf sessions where turn data exists.

**2. Attention watcher.** A hub loop on the existing roster cadence (fsnotify + 5s tick, roster.go:242) recomputes attention per live session and diffs against an in-memory baseline. On change it emits **`serf/attention/changed`** via the existing `BroadcastAll` (internal/appserver/server.go:76), mirroring `notifyAuthUpdated` (app_rpc.go:667). On hub restart the baseline re-seeds silently — no notification storm. Payload:

```json
{
  "changed": [{"threadId": "...", "title": "...", "project": "...", "level": "needs_you", "prevLevel": "working"}],
  "summary": {"needsYou": 3, "error": 1, "working": 2}
}
```

This also closes the verified push gap: sidebar refresh no longer depends on a lazily created per-thread relay.

**3. Wire field for clients.** `appwire.Thread` gains an `Attention` field (level string), **stamped by the hub** during its existing thread-list/read enrichment (precedent: `sanitizeStaleProcessingStatus` rewrites Status there today, app_threadlist.go:254). The daemon never sets it. The TUI cannot import `internal/hubcore`, so the wire field is the only clean path (verified: TUI consumes raw `appwire.Thread`, hub_types.go:170).

**4. No new stores.** Derivation uses live data only; nothing persists. (The punted SeenStore/`markSeen` design remains in git history; the upgrade path is additive — derivation gains a seen-cursor input, clearing gains "seen", ended sessions can join the inbox.)

## Web client

- **Sidebar**: rows, project rollups, and the NeedsYou tier render from the attention level. `serf/attention/changed` joins the sidebar-refresh allowlist (sidebar.js:288). The `⟳` redefinition lands in the tree rollup computation (tree.go:457), not the template.
- **notifications.js rewrite**: delete the 5s `/api/search` poll; subscribe to `serf/attention/changed`. OS notification + tone fire on any transition **into** `needs_you` or `error` for a thread that is not currently visible-and-focused (`document.hasFocus()` suppression stays). Level-based, not transition-pair-based — the missed `active→awaiting`-class transitions disappear structurally. Tab-title count and favicon read `summary`. Defaults: title + favicon ON, OS + sound opt-in (decision 4; migration: absent prefs get the new defaults, existing explicit prefs win).
- **Settings copy**: unified vocabulary; the OS-notification description becomes "Native notification when a thread needs you or errors."
- **Ended-session input strip**: shows "ended" plainly (no attention claim); the eternal "tasks loading…" fix belongs to the quick-wins workstream.

## TUI (minimal slice)

Consume `Thread.Attention` in `hubNodeFromThread` (hub_types.go:151); render a `◆` marker and a needs-you count in the dashboard. No markSeen (punted), no deeper TUI work. This also removes the TUI's divergent state labeling for the attention cases (hub_dashboard_view.go:505 lacks the `systemError` mapping the web has).

## Error handling

- Watcher: recovers from panics, logs and continues; a failed probe cycle leaves the previous baseline (no flapping).
- Attention stamping is nil-safe: absent turn data degrades to `idle`, never blocks a listing.
- `BroadcastAll` failures are per-connection, already handled by appserver.

## Testing

- Table-driven derivation unit tests: status × last-turn-kind × subsequent-input × live/ended × subagent matrix.
- Watcher test: synthetic roster diff → exactly one broadcast per change; restart re-seed emits nothing.
- Appwire golden updates for the new field and notification method.
- jstest: notifications.js level-transition logic, defaults migration, suppression.
- One e2e scenario card (agentic-testing): spawn → agent asks a question → sidebar row + NeedsYou tier turn amber and title count increments without opening the thread → reply → amber clears.
- Manual: live drive per docs/agentic-testing.md.

## Out of scope

Mark-seen/read-tracking (punted; future upgrade path documented above), daemon ask/permission states (nothing produces them today; the `needs_you` slot is ready when job-control's `ask` lands), per-message read receipts, service-worker/background push, working-state placement + token metrics (workstream 2), thread management (workstream 3), send-key setting (quick wins; decision recorded: keep ⌘↵ default, add setting), project-level delete (workstream 3; decision recorded: projects only).

## Estimate

~700–1,000 loc including tests: derivation + watcher + wire field ~350–500, web client ~250–350, TUI ~50–80, tests throughout.
