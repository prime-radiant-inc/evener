# WS6 — consistency sweep: inventory (not a design spec)

Queued follow-up items from the 2026-07 UX overhaul (WS1 attention, WS2 working
state & metrics, WS3 sidebar rebuild, WS5 MCP resilience). WS6 runs in its own
session: brainstorm → spec → plan from this inventory. Items are small
individually; the sweep's value is coherence. WS4 (quick wins: ⌘↵ send-key
setting, font-size setting, copy fixes) is a separate queued workstream, not
part of this list.

## Deferred display wiring (from WS2's final report)

- `cmd/serf-hub/app_threadread.go` `pastEntryThread` never got metrics wiring —
  a TUI user viewing an **ended** session through the hub sees no
  work-time/tokens (web path is complete; live sessions fine).
- `cmd/serf-tui/details_drawer.go` (the wide-terminal sidecar surface) never got
  metrics. The WS2 plan's C1 said "details drawer" but its checklist only named
  `renderHubSessionStatus`/`hub_status.go` (a different, pre-existing feature of
  that name).
- Per-turn duration/token badges in transcripts (explicitly out of scope in the
  WS2 spec; the wire now carries `Turn.CompletedAt`/`DurationMS`).
- Dollar cost display: tokens-only shipped; `llm/pricing.go` catalog still has
  zero callers.
- Composer action-state at rest: `cmd/serf-tui/composer_panel.go`
  `sessionTurnActionState()` treats `awaiting` like `active` (pre-existing
  Codex-era line, activated for every rested serf session by rest=awaiting).
  Adjudicate what the composer should show at rest.

## Small correctness/comment residue (from WS3's final report)

- T8: un-archive fires `Delete("project", filepath.Base(id))` on the common
  case during migration — behaviorally inert today; fix the misleading comment
  or gate on `id != filepath.Base(id)`.
- T18: an ended-rename that races a session back to live plus a daemon failure
  edits a live meta file (atomic, recoverable, silently reverted by next
  autosave) — hard-fail instead of silent fallthrough, or tighten the comment.
- T25 Bug 2: `GET /api/sessions/<id>` ignores a live rename (falls back to raw
  id) though `/api/tree` and meta are correct — separate session-detail
  endpoint needs the same name resolution.
- T23: the hand-maintained `encodeURIComponent` escaping for `working_dir` in
  the client sidebar is correct but untested.
- R2 menu edge: a project that is both test-run and archived shows "Archive"
  (idempotent no-op) instead of "Unarchive" in its row menu.

## Small residue (from WS5's final report)

- Stale comments at `agent/session_init.go:1204,1212` claiming SourceMCP
  "doesn't exist yet (Task 10)" — it shipped.
- Reconnect-*recovery* warnings reuse the failure-flavored hint text (classifier
  derives Hint from Source alone) — recovery notices read like failures.
- `conn.reconnecting` clear uses explicit per-branch writes; a `defer` would be
  strictly safer at zero cost.
- Settings-pane stdio probe is command-presence-only (`exec.LookPath`): a
  present-but-not-MCP binary reads "available" (documented Task-17 limit).

## Pre-existing consistency grab-bag (from the original 2026-07-03 diagnostic)

- Transport stragglers: the task-status-row 5s poller; `serf/task/updated` is
  defined but never emitted.
- Vocabulary: Working|Idle vs awaiting|processing|errored vs
  Current|Recent|Archived; ⟳/◆ glyph semantics (⟳N reads as "working" but means
  live-daemon count).
- Model picker is a raw uncurated API dump.
- Recent-prompts list renders untruncated walls of text.
- "tasks no tasks" copy; eternal "tasks loading…" on ended sessions.
- Spawn-failure UX: re-verify what remains now that WS5 removed the main
  MCP-fatal cause of buried-stderr HTTP 500s.
- WS1 leftovers: light-theme errored tint; settings copy staleness.
- Web details-panel context-pressure display.
- Project ordering feel check: WS3's LT addendum pinned last-touched ordering
  (`LastActivity`); verify it feels right live now that WS2's honest CreatedAt
  is in.
