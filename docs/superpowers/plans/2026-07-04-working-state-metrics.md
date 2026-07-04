# Working State & Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the already-collected working-state and token-usage data end-to-end — persist per-session work time and cumulative token totals, surface them (plus current-turn elapsed) in one consolidated status row across web and TUI, relocate the floating liveness line into that row, and delete the ghost header poller.

**Architecture:** The daemon (agent module) holds `createdAt`/`workMillis`/cumulative-usage in-memory, seeds them from `SessionMeta` on restore, accumulates work at the single per-turn terminal boundary (`finishProcessingAtBoundary`, including the `Close()`-mid-turn case), and persists them via `Meta()`/autosave. A new `EventTurnEnded` carries per-turn duration to the appwire projector; metrics ride the wire on `appwire.SerfThread` (pointer `Usage`), on the REST `StatusInfo`, and (for ended sessions) through the `WorkspaceData`→`SessionDetail` carrier chain. The hub web UI renders one status row fed by a lean `/state` fetch, refreshed event-driven (`serf-hub:status-refresh`) with a 30s fallback and a client 10s tick; quiet/stall liveness moves into that row's spans. The TUI mirrors the fields onto `hubSessionDetail`.

**Tech Stack:** Go (agent module: session/events/contextmgr/schema; root module: server, internal/appprojector, cmd/serf-hub, cmd/serf-tui; appwire wire types), htmx + vanilla JS (status row, liveness relocation), jstest (JSDOM).

---

## Conventions used throughout this plan

- **Modules:** root (`.`), `agent/`, `llm/`, `auth/` under `go.work`. Run tests per-module.
  - agent package: `cd agent && go test ./... -run '<Name>' -count=1`; module-wide `cd agent && go test ./...`.
  - root packages (server, internal/appprojector, appwire, cmd/serf-hub, cmd/serf-tui): from repo root `go test ./<pkg>/... -run '<Name>' -count=1`.
  - lint slice: agent → `cd agent && golangci-lint run ./...`; root → `golangci-lint run ./...` from repo root.
  - appwire decode goldens (if a golden drifts after adding wire fields): `make fuzz-goldens` then re-run `go test -run '^Test.*Golden$' ./appwire`.
  - jstest (not in CI/Makefile; agent-run): `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node <file>.js` (one-time jsdom install per `jstest/README.md`).
- **Never** `git add -A`. Stage only the exact paths listed in each task's commit step (after a `git status`).
- Repeat code rather than cross-referencing between tasks. No placeholders.

---

## Phase A — Daemon, wire & projector (agent + root modules)

### Task A1 — `schema.CumulativeUsage` + `SessionMeta` metric fields + golden regen

Adds the persisted shape. Leaf task, no Session dependencies. **Coordination note:** the parallel sidebar-rebuild plan's Task 15 adds `SessionMeta.Origin` to this same struct and the same `snapshot_golden_test.go` golden; whichever branch merges second rebases its meta fixtures (the golden string + `goldenMeta()`) on the first — the field ordering below assumes this spec lands first (append after `WorktreeRestoreRoot`).

- [ ] **Failing test** — in `agent/schema/cov_s5_snapshot_test.go` (new `Test` function, real marshal), add a test that a `SessionMeta{CumulativeUsage: CumulativeUsage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50, TotalTokens: 300}, WorkMillis: 45000}` marshals to JSON containing `"cumulative_usage":{"input_tokens":100,"output_tokens":200,"cache_read_tokens":50,"total_tokens":300}` and `"work_millis":45000`, and that a zero-valued `SessionMeta{}` (legacy) marshals with NEITHER key present (the `omitzero` round-trip). Run: `cd agent && go test ./schema/... -run 'TestSessionMeta_CumulativeUsageOmitzero' -count=1` → expect a compile failure (`CumulativeUsage`/`WorkMillis` undefined).
- [ ] **Implement** — in `agent/schema/snapshot.go`:
  - Add the struct beside `GoalSnapshot`:
    ```go
    // CumulativeUsage is a deliberately lossy snapshot of an llm.Usage kept in
    // SessionMeta so per-session token totals survive daemon restart and resume.
    // Conversion from llm.Usage drops Raw and the reasoning/cache-write pointers;
    // nil pointers map to 0. Tagged omitzero so legacy metas round-trip untouched.
    type CumulativeUsage struct {
    	InputTokens     int64 `json:"input_tokens,omitzero"`
    	OutputTokens    int64 `json:"output_tokens,omitzero"`
    	CacheReadTokens int64 `json:"cache_read_tokens,omitzero"`
    	TotalTokens     int64 `json:"total_tokens,omitzero"`
    }
    ```
  - Append to `SessionMeta` (after `WorktreeRestoreRoot`, line ~87):
    ```go
    	// CumulativeUsage carries the session's running self-only token totals so
    	// they survive restart/resume. omitzero: legacy metas without it round-trip
    	// unchanged (WS2 working-state-metrics).
    	CumulativeUsage CumulativeUsage `json:"cumulative_usage,omitzero"`
    	// WorkMillis is the accumulated wall-clock work time (sum of every turn's
    	// duration, interrupted and failed included), persisted so the total
    	// survives restart/resume. omitzero for legacy round-trip.
    	WorkMillis int64 `json:"work_millis,omitzero"`
    ```
- [ ] **Run** the A1 test → pass. Then run the golden characterization: `cd agent && go test ./... -run 'TestSessionMeta_Golden' -count=1` → expect **failure** at `snapshot_golden_test.go` (wire drift), because `goldenMeta()` does not yet set the new fields but the const is stale.
- [ ] **Regenerate golden** — in `agent/snapshot_golden_test.go`: in `goldenMeta()` (line ~57) add `CumulativeUsage: schema.CumulativeUsage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50, TotalTokens: 300}, WorkMillis: 45000,` to the literal; append `,"cumulative_usage":{"input_tokens":100,"output_tokens":200,"cache_read_tokens":50,"total_tokens":300},"work_millis":45000` to `goldenMetaJSON` (line ~107) immediately before the closing `}`. Run `cd agent && go test ./... -run 'TestSessionMeta_Golden' -count=1` → pass (both `TestSessionMeta_GoldenWireFormat` and `TestSessionMeta_GoldenRoundTrip`).
- [ ] **Run** `cd agent && go test ./schema/... ./... -run 'Golden|CumulativeUsage' -count=1` and `cd agent && golangci-lint run ./schema/...` → green.
- [ ] **Commit** — `git add agent/schema/snapshot.go agent/schema/cov_s5_snapshot_test.go agent/snapshot_golden_test.go` → `feat(schema): persist CumulativeUsage + WorkMillis on SessionMeta (omitzero)`.

### Task A2 — `Session.createdAt` home + seeding + `Meta()` CreatedAt fix

Fixes the "found en route" bug: `Meta()` stamps `CreatedAt: now` (session_state.go:102) so every autosave clobbers creation time. Uses only the existing `CreatedAt` field — no schema dependency.

- [ ] **Failing test** — in `agent/session_state_test.go`, add `TestMeta_CreatedAtStableAcrossCalls`: build a session via `NewSession` with an injected fake clock, capture `s.Meta().CreatedAt`, advance the fake clock, assert a second `s.Meta().CreatedAt` equals the first (and `UpdatedAt` advanced). Run `cd agent && go test ./... -run 'TestMeta_CreatedAtStableAcrossCalls' -count=1` → expect failure (CreatedAt currently == now each call).
- [ ] **Implement**:
  - `agent/session.go`: add `createdAt time.Time` to the `Session` struct beside `state`/`turns`/`modelResponses` (line ~135–138). Ensure `time` is imported (it is, via other fields).
  - `agent/session_init.go` `NewSession` (line ~79): after the struct literal is assigned to `s` (line ~136), add `s.createdAt = s.sclock().Now().UTC()`.
  - `agent/session_init.go` `RestoreSessionFromMetaWithConfig` (line ~344 literal): add `createdAt: meta.CreatedAt,` to the `&Session{...}` literal (beside `modelResponses: meta.TurnCount`).
  - `agent/session_state.go` `Meta()` (line ~102): change `CreatedAt: now,` to `CreatedAt: s.createdAt,`. Leave `UpdatedAt: now`.
- [ ] **Run** `cd agent && go test ./... -run 'TestMeta_CreatedAtStableAcrossCalls' -count=1` → pass.
- [ ] **Run** the restore/fork fuzz + state suites that touch meta: `cd agent && go test ./... -run 'TestMeta|Restore|Fork|Snapshot' -count=1` and `cd agent && golangci-lint run ./...` → green.
- [ ] **Commit** — `git add agent/session.go agent/session_init.go agent/session_state.go agent/session_state_test.go` → `fix(agent): stamp SessionMeta.CreatedAt from a held createdAt, not now (was clobbered every autosave)`.

### Task A3 — `Session.workMillis` + `contextmgr.SetCumulativeUsage` + `Meta()` maps totals + restore seeding (K2 seeding half)

Depends on A1 (schema fields) and A2 (createdAt home). Gives the Session the two remaining homes and seeds them on restore before the first autosave.

- [ ] **Failing test 1 (contextmgr)** — in `agent/internal/contextmgr/cov_w3sub_context_manager_test.go`, add `TestSetCumulativeUsage`: `cm := NewManager(profile, nil); cm.SetCumulativeUsage(llm.Usage{InputTokens: 10, OutputTokens: 20})`; assert `cm.CumulativeUsage()` returns those; then `cm.AddUsage(llm.Usage{InputTokens: 5})` and assert input==15 (seed then accumulate). Run `cd agent && go test ./internal/contextmgr/... -run 'TestSetCumulativeUsage' -count=1` → fail (undefined `SetCumulativeUsage`).
- [ ] **Failing test 2 (restore seed → Meta round-trip, the K2 seeding half)** — in `agent/session_state_test.go`, add `TestRestoreSeedsMetricsIntoMeta`: build a `schema.SessionMeta` with `WorkMillis: 5000`, `CumulativeUsage: schema.CumulativeUsage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50, TotalTokens: 300}`, `CreatedAt` set; `RestoreSessionFromMeta(...)`; assert `s.Meta().WorkMillis == 5000` and `s.Meta().CumulativeUsage == meta.CumulativeUsage` (NOT zero). Run → fail.
- [ ] **Implement**:
  - `agent/internal/contextmgr/context_manager.go`: add beside `CumulativeUsage()` (line ~104):
    ```go
    // SetCumulativeUsage seeds the running total, used on restore to re-hydrate
    // persisted per-session token counts before the first response is recorded.
    func (cm *Manager) SetCumulativeUsage(u llm.Usage) {
    	cm.mu.Lock()
    	defer cm.mu.Unlock()
    	cm.cumUsage = u
    }
    ```
  - `agent/session.go`: add `workMillis int64` to the `Session` struct beside `createdAt` (from A2).
  - `agent/session_state.go`: add an unexported converter near `Meta()`:
    ```go
    // cumulativeUsageSnapshot converts the context manager's llm.Usage total to
    // the lossy schema.CumulativeUsage persisted in SessionMeta (nil pointers→0,
    // Raw dropped).
    func cumulativeUsageSnapshot(u llm.Usage) schema.CumulativeUsage {
    	cacheRead := int64(0)
    	if u.CacheReadTokens != nil {
    		cacheRead = int64(*u.CacheReadTokens)
    	}
    	return schema.CumulativeUsage{
    		InputTokens:     int64(u.InputTokens),
    		OutputTokens:    int64(u.OutputTokens),
    		CacheReadTokens: cacheRead,
    		TotalTokens:     int64(u.TotalTokens),
    	}
    }
    ```
    (import `primeradiant.com/serf/llm` in session_state.go if not already present.)
  - `agent/session_state.go` `Meta()`: add `WorkMillis: s.workMillis,` and `CumulativeUsage: cumulativeUsageSnapshot(s.contextMgr.CumulativeUsage()),` to the returned `schema.SessionMeta{...}`. Guard the `contextMgr` nil case (Meta is called under `s.mu`; `s.contextMgr` is set by init and non-nil in normal operation — mirror the existing `s.contextMgr.LastInputTokens()` call at line 105 which already assumes non-nil).
  - `agent/session_init.go` `RestoreSessionFromMetaWithConfig`: add `workMillis: meta.WorkMillis,` to the `&Session{...}` literal (beside `createdAt`). After `initSessionState` returns and the `RecordInputTokens` seed (line ~439), add:
    ```go
    	// Seed persisted cumulative token totals before the first autosave can
    	// overwrite them (WS2 K2 regression).
    	if s.contextMgr != nil {
    		s.contextMgr.SetCumulativeUsage(llm.Usage{
    			InputTokens:     int(meta.CumulativeUsage.InputTokens),
    			OutputTokens:    int(meta.CumulativeUsage.OutputTokens),
    			TotalTokens:     int(meta.CumulativeUsage.TotalTokens),
    			CacheReadTokens: cacheReadPtr(meta.CumulativeUsage.CacheReadTokens),
    		})
    	}
    ```
    Add a small helper `cacheReadPtr(n int64) *int { if n == 0 { return nil }; v := int(n); return &v }` in session_init.go (or reuse an existing int-pointer helper if one exists — grep first). Confirm `llm` is imported in session_init.go.
- [ ] **Run** both A3 tests → pass. Run `cd agent && go test ./internal/contextmgr/... ./... -run 'TestSetCumulativeUsage|TestRestoreSeedsMetricsIntoMeta|TestMeta' -count=1`.
- [ ] **Run** `cd agent && golangci-lint run ./... && go test ./...` (full agent module — heavy; scope with `-run 'Restore|Meta|Fork|contextmgr|Snapshot'` if time-boxed, then full before phase end).
- [ ] **Commit** — `git add agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/cov_w3sub_context_manager_test.go agent/session.go agent/session_state.go agent/session_init.go agent/session_state_test.go` → `feat(agent): hold + restore-seed workMillis and cumulative usage in Session/contextmgr`.

### Task A4 — Terminal-boundary work accumulation + `Close()` amendment (Decision 4 / L3) + fork zeroing + K2 integration

Adds `turnStartedAt` capture, accumulation at the single boundary choke point, and the `Close()`-before-boundary amendment.

- [ ] **Failing test — per-turn accumulation** — in `agent/session_lifecycle_test.go` (or a new `agent/session_workmillis_test.go`), add tests driving a session with a fake clock:
  - `TestWorkMillis_CompletedTurnCounts`: run one clean turn where the fake clock advances by a known delta between the processing-begin transition and the terminal boundary; assert `s.Meta().WorkMillis == delta.Milliseconds()`.
  - `TestWorkMillis_InterruptedTurnCounts`: interrupt a turn mid-flight (cancel ctx); assert `WorkMillis` advanced by the elapsed delta (interrupted turns count — Decision 4).
  - `TestWorkMillis_FailedTurnCounts`: drive a terminal model error (recoverable, no Close); assert `WorkMillis` advanced.
  - `TestWorkMillis_MultiTurnDrainEachCounts`: user input + a follow-up (two `processOneInput` calls); assert `WorkMillis` == sum of both turns' deltas (each drained entry counts, not just the last).
  - `TestWorkMillis_CloseMidTurnCounts` (the L3 regression): while a session is `SessionProcessing` with `turnStartedAt` set, call `s.Close()`; assert `s.Meta().WorkMillis` includes the dying turn's elapsed (Close accumulates before flipping to `SessionClosed`).
  Run these → fail (no accumulation yet).
- [ ] **Failing test — K2 integration** — `TestRestoreThenTurnAutosaveKeepsPriorTotals`: restore a session from a meta with `WorkMillis: 5000` and non-zero `CumulativeUsage`; run one turn (fake clock advances 2000ms; a stubbed model response carries `llm.Usage`); trigger autosave (`maybeAutoSave`); reload the meta from disk (or read `s.Meta()`); assert `WorkMillis == 5000 + 2000` and token totals == prior + turn (NOT turn-only). Run → fail.
- [ ] **Implement**:
  - `agent/session.go`: add `turnStartedAt time.Time` to the `Session` struct beside `workMillis`.
  - `agent/session_lifecycle.go` `processOneInput` (the processing-begin transition, line ~778): immediately after `s.setStateIfOpenLocked(SessionProcessing)` (still under `s.mu`), add `s.turnStartedAt = s.sclock().Now()`.
  - `agent/session_state.go` `finishProcessingAtBoundary` (line ~166): accumulate under the lock on the `transitioned` edge, and emit `EventTurnEnded` after (see A5 for the event type — sequence A5 before wiring this emit, OR land A5's type first). Rewrite:
    ```go
    func (s *Session) finishProcessingAtBoundary(ctx context.Context, state SessionState) {
    	transitioned := false
    	var turnMS int64
    	s.mu.Lock()
    	if s.state == SessionProcessing && !s.closingOrClosedLocked() {
    		s.state = state
    		turnMS = s.accumulateWorkLocked()
    		transitioned = true
    	}
    	s.mu.Unlock()
    	if transitioned {
    		s.emit(events.EventTurnEnded, events.TurnEndedData{TurnDurationMS: turnMS})
    		if err := s.drainPendingWatchSends(ctx); err != nil {
    			s.emit(events.EventWarning, events.WarningData{Message: "watch send retry at processing boundary failed: " + err.Error()})
    		}
    		s.finishActiveProvenance()
    	}
    }

    // accumulateWorkLocked adds the just-ended turn's wall-clock to workMillis and
    // returns that turn's duration in ms. Caller holds s.mu; a zero turnStartedAt
    // (no turn was timed) contributes nothing.
    func (s *Session) accumulateWorkLocked() int64 {
    	if s.turnStartedAt.IsZero() {
    		return 0
    	}
    	ms := s.sclock().Now().Sub(s.turnStartedAt).Milliseconds()
    	if ms < 0 {
    		ms = 0
    	}
    	s.workMillis += ms
    	s.turnStartedAt = time.Time{}
    	return ms
    }
    ```
    (add `time` import to session_state.go.)
  - `agent/session_lifecycle.go` `close(cleanupEnv bool)` (line ~80–85), the L3 amendment: between `s.mu.Lock()` and `s.state = SessionClosed`, accumulate for a still-processing session BEFORE flipping:
    ```go
    		s.mu.Lock()
    		turns := s.modelResponses
    		emitEnd := !s.sessionEndEmitted
    		s.sessionEndEmitted = true
    		if s.state == SessionProcessing {
    			s.accumulateWorkLocked() // dying turn's work counts (Decision 4/L3)
    		}
    		s.closing = true
    		s.state = SessionClosed
    ```
    `Close()` does **not** emit `EventTurnEnded` — its own `EventSessionEnd{closed}` owns the projector turn completion (see A6). The accumulation is the only work here.
- [ ] **Pin test — fork zeroing (expected GREEN, write after implement)** — in the same test file, add `TestForkStartsMetricsAtZero`: give a parent session non-zero metrics (restore it from a meta with `WorkMillis: 5000` + non-zero `CumulativeUsage`, per the A3 test setup), fork it (`ForkSession`), assert the child's `Meta().WorkMillis == 0` and `Meta().CumulativeUsage == schema.CumulativeUsage{}`. The spec validated zeroing is automatic (fork.go:174-187 builds a fresh child meta), so this test pins existing behavior rather than driving new code. If it FAILS, forks inherit parent metrics through a path the spec missed — report BLOCKED with the failing path; do not patch it ad hoc.
- [ ] **Run** the A4 tests → pass. Also run `cd agent && go test ./... -run 'TestWireState|Awaiting|Interrupt|Close|Drain' -count=1` to confirm no lifecycle regression.
- [ ] **Run** `cd agent && golangci-lint run ./... && go test ./...` → green (the K2 integration test is load-bearing; do not skip).
- [ ] **Commit** — `git add agent/session.go agent/session_state.go agent/session_lifecycle.go agent/session_workmillis_test.go agent/session_state_test.go` → `feat(agent): accumulate per-turn work at the terminal boundary, incl. interrupt/fail/close-mid-turn`.

### Task A5 — `EventTurnEnded` event type

Adds the sealed event + payload the boundary emits (A4 references it; land the type before A4's emit compiles, or bundle A5 immediately before finishing A4's build — treat A5 as a prerequisite of A4's `s.emit(events.EventTurnEnded, ...)` line).

- [ ] **Failing test** — in `agent/events/events_test.go`, assert `events.New(events.TurnEndedData{TurnDurationMS: 1234}).Kind == events.EventTurnEnded` and that the payload round-trips its `TurnDurationMS`. Run `cd agent && go test ./events/... -run 'TurnEnded' -count=1` → fail.
- [ ] **Implement**:
  - `agent/events/events.go`: add `// EventTurnEnded marks a single turn reaching its terminal boundary, carrying the turn's wall-clock duration.` + `EventTurnEnded EventKind = "TURN_ENDED"` in the const block.
  - `agent/events/payloads.go`: add `// TurnEndedData is the payload for an EventTurnEnded event. type TurnEndedData struct { TurnDurationMS int64 \`json:"turn_duration_ms"\` }`.
  - `agent/events/eventdata.go`: add `func (TurnEndedData) eventKind() EventKind { return EventTurnEnded }` and `_ EventData = TurnEndedData{}` in the compile-assertion block.
- [ ] **Run** `cd agent && go test ./events/... -run 'TurnEnded' -count=1` → pass. `cd agent && golangci-lint run ./events/...` → green.
- [ ] **Commit** — `git add agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/events/events_test.go` → `feat(events): add EventTurnEnded{TurnDurationMS}`.

> Ordering note: implement **A5 before A4's emit line compiles**. If executing strictly sequentially, do A5's type first, then A4.

### Task A6 — Projector turn-timing + replay + transcript (with the interrupt-status refinement)

**Design deviation from the spec, flagged:** the spec says "on `EventTurnEnded` [the projector] completes the active turn … the legacy completion sites … no-op idempotently." Extraction shows that ordering **regresses three tested turn statuses** — `EventTurnEnded` fires *before* `EventSessionEnd{Interrupted|closed}` (session_lifecycle.go:510→535) and before `EventSessionEnd{closed}` (Close), and `EventError` already fires before it on the failed path. Completing+clearing the turn on `EventTurnEnded` would strand interrupted/closed/failed turns as `completed`. Pinned by `TestAppEventProjectorMarksInterruptedTurnCanceled` (appwire_projection_test.go:407), `TestAppEventProjectorLetsInterruptedSessionEndCancelAfterContextCanceledError` (:450), and the failed-turn assertion at :1319. **Resolution:** `EventTurnEnded` *records pending turn timing*; the existing completion sites (which already own status) attach it. This preserves every status while stamping `CompletedAt`/`DurationMS`. (Adopted into the spec — see the "Plan-stage amendment" entry in the spec's Review log; spec and plan now agree.)

- [ ] **Failing test — projector timing** — in `internal/appprojector/appwire_projection_test.go`:
  - `TestProjectorTurnEndedStampsTiming`: `EventUserInput` → `EventTurnEnded{TurnDurationMS: 4200}` (with `Timestamp` = a known time `ts`) → `EventSessionEnd{State: "idle"}`; assert the `NotifyTurnCompleted` from the SessionEnd carries `Turn.Status == completed`, `Turn.CompletedAt == &ts.Unix()`, `Turn.DurationMS == &int64(4200)`.
  - `TestProjectorTurnEndedPreservesInterruptStatus`: `EventUserInput` → `EventTurnEnded{TurnDurationMS: 100}` → `EventSessionEnd{Interrupted: true, State: "idle"}`; assert the completed turn is `interrupted` and carries the recorded `DurationMS` (interrupt status wins; timing still attached).
  Run `go test ./internal/appprojector/... -run 'TestProjectorTurnEnded' -count=1` → fail.
- [ ] **Failing test — replay** — in `server/appwire_turns_test.go` (or the projection test that feeds `appTurnsFromNotifications`), assert that a `NotifyTurnStarted` carrying `Turn.StartedAt` and a `NotifyTurnCompleted` carrying `Turn.CompletedAt`/`Turn.DurationMS` reconstruct a `Turn` with those three fields set (today they are dropped). Run `go test ./server/... -run 'AppTurnsFromNotifications|Replay' -count=1` → fail.
- [ ] **Implement — projector** (`internal/appprojector/appwire_projection.go`):
  - Add projector fields `pendingTurnID string`, `pendingCompletedAtUnix int64`, `pendingDurationMS int64` to `AppEventProjector`.
  - Add `case events.EventTurnEnded:` in `Project`:
    ```go
    	case events.EventTurnEnded:
    		if p.activeTurnID == "" {
    			return nil // turn already completed (e.g. failed via EventError)
    		}
    		data := eventData[events.TurnEndedData](event.Data)
    		p.pendingTurnID = p.activeTurnID
    		p.pendingCompletedAtUnix = event.Timestamp.Unix()
    		p.pendingDurationMS = data.TurnDurationMS
    		return nil
    ```
  - Add a helper that stamps + clears pending timing onto a completing `appwire.Turn` when the id matches:
    ```go
    func (p *AppEventProjector) applyPendingTiming(turnID string, turn *appwire.Turn) {
    	if p.pendingTurnID == "" || p.pendingTurnID != turnID {
    		return
    	}
    	if !turn.CompletedAt ... // set turn.CompletedAt = &completed, turn.DurationMS = &dur
    	c := p.pendingCompletedAtUnix
    	d := p.pendingDurationMS
    	turn.CompletedAt = &c
    	turn.DurationMS = &d
    	p.pendingTurnID = ""
    	p.pendingCompletedAtUnix = 0
    	p.pendingDurationMS = 0
    }
    ```
  - Call `applyPendingTiming(turnID, &turn)` at each site that builds a completing `appwire.Turn` for the active turn, immediately before it is placed in the `NotifyTurnCompleted` params:
    - `EventUserInput` prior-turn completion (line ~113–117).
    - `EventGoalContinuation` prior-turn completion (line ~161–165).
    - `EventError` failed-turn completion (line ~466–481) — pending is unset here (EventTurnEnded runs after EventError), so this is a no-op but keeps the timing path uniform.
    - `EventSessionEnd` completion (line ~646–650).
    (These construct `appwire.Turn{ID: turnID, Status: ...}` inline; refactor each to a local `turn := appwire.Turn{...}; p.applyPendingTiming(turnID, &turn)` then use `turn`.)
- [ ] **Implement — replay** (`server/appwire_turns.go` `appTurnsFromNotifications`):
  - In the `NotifyTurnStarted` branch (line ~116–125), after setting Status, add `if params.Turn.StartedAt != nil { turn.StartedAt = params.Turn.StartedAt }`.
  - In the `NotifyTurnCompleted` branch (line ~185–191), add `if params.Turn.CompletedAt != nil { turn.CompletedAt = params.Turn.CompletedAt }` and `if params.Turn.DurationMS != nil { turn.DurationMS = params.Turn.DurationMS }`.
- [ ] **Implement — transcript reconstruction** — `apptranscript` sets no turn timing today (verified: zero non-test `StartedAt` references anywhere in `internal/apptranscript`). In the turn-projection path used by `appTurnsFromTranscriptFile`, stamp `StartedAt` from the entry's timestamp and leave `DurationMS` nil (message-records cannot span a duration). Add an `internal/apptranscript` test asserting a reconstructed turn has `StartedAt != nil && DurationMS == nil`.
- [ ] **Run** the A6 tests → pass; then the full existing projector + interrupt suite: `go test ./internal/appprojector/... ./internal/apptranscript/... ./server/... -run 'Projector|Interrupt|AppTurns|Transcript' -count=1` → green (the interrupt/failed tests at :407/:450/:1319 MUST stay green).
- [ ] **Run** `golangci-lint run ./...` (root) → green.
- [ ] **Commit** — `git add internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go server/appwire_turns.go server/appwire_turns_test.go internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go` → `feat(appprojector): stamp turn CompletedAt/DurationMS from EventTurnEnded; carry timing through replay`.

### Task A7 — Wire the metrics onto `SerfThread` + `StatusInfo` (pull-callback-fed) and daemon accessors

- [ ] **Failing test — appwire type** — in `appwire/types_test.go`, assert a `SerfThread{Usage: &SerfUsage{InputTokens: 1}, WorkMillis: 2, ActiveTurnStartedAt: 3}` marshals with `usage`, `workMillis`, `activeTurnStartedAt`, and that a `SerfThread{}` (nil Usage, zero scalars) omits all three (pointer + omitempty). Run `go test ./appwire/... -run 'SerfUsage|SerfThreadMetrics' -count=1` → fail.
- [ ] **Failing test — daemon appThread + status** — in `server/appwire_runtime_test.go` and `server/server_handlers_test.go` (or the existing status test), wire a `SetWorkMetricsFunc` returning known values and assert `appThread().Serf.{Usage,WorkMillis,ActiveTurnStartedAt}` and the `/status` `StatusInfo.{Usage,WorkMillis,ActiveTurnStartedAt}` carry them. Run → fail.
- [ ] **Implement — appwire** (`appwire/types.go`):
  - Add after `GoalState` (line ~206):
    ```go
    // SerfUsage carries a serf session's cumulative self-only token totals for
    // the status row. A nil *SerfUsage on SerfThread means no token data (old
    // daemon, Codex thread, or a session with zero usage) — the clusters hide
    // rather than render ↑0 ↓0.
    type SerfUsage struct {
    	InputTokens     int64 `json:"inputTokens,omitempty"`
    	OutputTokens    int64 `json:"outputTokens,omitempty"`
    	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
    	TotalTokens     int64 `json:"totalTokens,omitempty"`
    }
    ```
  - Add to `SerfThread` (after `Goal`, line ~196): `Usage *SerfUsage \`json:"usage,omitempty"\``, `WorkMillis int64 \`json:"workMillis,omitempty"\``, `ActiveTurnStartedAt int64 \`json:"activeTurnStartedAt,omitempty"\``. (Pointer for Usage per L5 — a value struct's `omitempty` never omits.)
  - **No catalog change:** the catalog (`TestDaemonRouterMatchesCatalog`) enumerates `Method*`/`Notify*` constants only; adding struct fields does not touch it. If A7 surfaces a need for a new appwire *method*, STOP — that is a surfaced ambiguity and a one-commit atom across the catalog + both routers (server + serf-hub) with the bidirectional cross-checks; do not fold it in silently.
- [ ] **Implement — daemon accessors** (`agent`): add three Session methods (in `agent/session_state.go` or a small `agent/session_metrics.go`):
  ```go
  func (s *Session) WorkMillisSnapshot() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.workMillis }
  func (s *Session) ActiveTurnStartedAtUnix() int64 {
  	s.mu.Lock(); defer s.mu.Unlock()
  	if s.state == SessionProcessing && !s.turnStartedAt.IsZero() { return s.turnStartedAt.Unix() }
  	return 0
  }
  func (s *Session) CumulativeUsageSnapshot() llm.Usage {
  	if s.contextMgr == nil { return llm.Usage{} }
  	return s.contextMgr.CumulativeUsage()
  }
  ```
  Add an agent test that a mid-turn session reports `ActiveTurnStartedAtUnix() > 0` and an idle one reports `0`.
- [ ] **Implement — server StatusInfo + callbacks** (`server`):
  - `server/server.go`: add to `StatusInfo` (line ~80): `Usage *appwire.SerfUsage \`json:"usage,omitempty"\``, `WorkMillis int64 \`json:"work_millis,omitempty"\``, `ActiveTurnStartedAt int64 \`json:"active_turn_started_at,omitempty"\``. Add a `workMetricsFn func() (workMillis int64, usage *appwire.SerfUsage, activeTurnStartedAt int64)` field to `Server` (beside `pressureFn`), and `SetWorkMetricsFunc` mirroring `SetContextPressureFunc` (line ~368).
  - `server/server_handlers.go` `handleStatus` (line ~285): after the `cmfn`/`dfn` blocks, `if wmfn := s.workMetricsFn (read under RLock); wmfn != nil { wm, usage, at := wmfn(); status.WorkMillis = wm; status.Usage = usage; status.ActiveTurnStartedAt = at }`.
  - `server/appwire_runtime.go` `appThread` (line ~491): populate `Serf.Usage`, `Serf.WorkMillis`, `Serf.ActiveTurnStartedAt` from the same `workMetricsFn` (read it under the RLock alongside `pfn`/`cmfn`).
  - Add a wire helper `serfUsageFromLLM(u llm.Usage) *appwire.SerfUsage` (in server): returns nil when all four totals are zero (so fresh/old/codex all hide); else maps input/output/total and `CacheReadTokens` from the `*int`.
  - `cmd/serf/serve.go` (~line 344, beside `SetContextPressureFunc`): `srv.SetWorkMetricsFunc(func() (int64, *appwire.SerfUsage, int64) { sess := getSession(); return sess.WorkMillisSnapshot(), serfUsageFromLLM(sess.CumulativeUsageSnapshot()), sess.ActiveTurnStartedAtUnix() })`. **Re-verify serve.go's `getSession()` shape against current code (ask_user touched serve.go).**
- [ ] **Run** the A7 tests + `go test ./appwire/... ./server/... -count=1`. If an appwire decode golden drifts (`make fuzz` reports `Test*Golden`), run `make fuzz-goldens` and re-verify. Run `golangci-lint run ./...` (root) + `cd agent && golangci-lint run ./...`.
- [ ] **Commit** — `git add appwire/types.go appwire/types_test.go agent/session_metrics.go agent/session_state_test.go server/server.go server/server_handlers.go server/appwire_runtime.go cmd/serf/serve.go server/appwire_runtime_test.go server/server_handlers_test.go` (adjust to the files actually touched; `git status` first) → `feat(wire): carry cumulative usage + workMillis + activeTurnStartedAt on SerfThread and /status`.

---

## Phase B — Web (cmd/serf-hub)

> The web UI lives in `cmd/serf-hub/templates/` (html/template) + `cmd/serf-hub/assets/` (JS). Corrections from extraction: `/state` **already exists** (it is the `renderInputStrip` input-strip route, dispatched at `web.go:329`); the "ghost" is the `/meta` poller; there is no `<title>`-tag OOB swap — the ghost swaps the visible `#workspace-session-title` span.

### Task B1 — Delete the ghost `/meta` poller; port the OOB-title assertion to `/state`

- [ ] **Failing/porting test** — in `cmd/serf-hub/web_test.go`, port the load-bearing assertions of `TestWeb_MetaPartialRefreshesGeneratedSessionTitle` (lines ~3146–3187) into the existing `/state` test path (`TestWeb_State_RendersInputStatusPartial`, ~3096, or a new `TestWeb_StatePartialRefreshesGeneratedSessionTitle`): fetch `/_partials/s/<id>/state`, assert the body contains `id="workspace-session-title"` and `hx-swap-oob="true"` and the fresh generated `Name` (not the long `OriginalPrompt`). Keep `TestWeb_WorkspaceInitialMetaDoesNotDuplicateTitleOOB` (~3189–3221) — it must stay green: the initial `/workspace` render must render exactly **one** `#workspace-session-title` and **no** `hx-swap-oob="true"`. Run → fail (state partial has no OOB title yet).
- [ ] **Implement — delete the ghost**:
  - `cmd/serf-hub/templates/partials/workspace.html`: delete the `<div class="workspace-meta workspace-meta-poll" hx-get=".../meta" hx-trigger="load, every 2s" ...>` block (lines ~27–33); delete the orphaned `{{define "workspace_meta_content"}}` (line ~115); delete `{{define "workspace_meta"}}` (line ~117).
  - `cmd/serf-hub/web.go`: delete the `case "meta": s.renderWorkspaceMeta(...)` dispatch (lines ~331–332).
  - `cmd/serf-hub/web_workspace.go`: delete `renderWorkspaceMeta` (lines ~261–285) and the `case "meta": http.NotFound(...)` in `handleSession` (lines ~49–50).
  - Delete `TestWeb_MetaPartialRefreshesGeneratedSessionTitle` (its assertions now live on `/state`).
- [ ] **Implement — OOB title on `/state` only (guarded)**: in `cmd/serf-hub/templates/partials/input_strip.html`, add at the top of `{{define "input_status"}}` a guarded OOB span: `{{if .OOBTitle}}<span id="workspace-session-title" class="title" hx-swap-oob="true">{{.Title}}</span>{{end}}`. The `OOBTitle` flag is set **only** by `renderInputStrip` (Task B3), never by the inline initial workspace render — this keeps `TestWeb_WorkspaceInitialMetaDoesNotDuplicateTitleOOB` green (inline render → no OOB span, count stays 1) while the polled `/state` response carries the OOB swap that keeps the header title fresh (the header `#workspace-session-title` at workspace.html:18 is the swap target).
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'State|Meta|WorkspaceInitial|InputStatus' -count=1` → green. `golangci-lint run ./...` → green.
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/templates/partials/input_strip.html cmd/serf-hub/web.go cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go` → `refactor(hub-web): delete the ghost /meta poller; move the OOB title to /state`.

### Task B2 — Carry metrics on `WorkspaceData`, `daemonStatus`, `hubapi.SessionDetail`

- [ ] **Failing test** — `cmd/serf-hub/web_api_tree_test.go`: build an ended session (Past index) whose `SessionMeta` carries `WorkMillis: 7000` + non-zero `CumulativeUsage`; assert `apiSessionDetail(id)` returns a `SessionDetail` with `WorkMillis == 7000` and a non-nil `Usage` matching. Add a `web_format` test that `workspaceDataFromAppThread` maps `thread.Serf.{Usage,WorkMillis,ActiveTurnStartedAt}` onto `WorkspaceData`. Run → fail.
- [ ] **Implement**:
  - `cmd/serf-hub/web_types.go`: add to `WorkspaceData` (struct ~160): `WorkMillis int64`, `Usage *appwire.SerfUsage`, `ActiveTurnStartedAt int64`. Add to `daemonStatus` (~255): `WorkMillis int64 \`json:"work_millis,omitempty"\``, `Usage *appwire.SerfUsage \`json:"usage,omitempty"\``, `ActiveTurnStartedAt int64 \`json:"active_turn_started_at,omitempty"\``.
  - `hubapi/types.go`: add to `SessionDetail` (~84): `WorkMillis int64 \`json:"work_millis,omitempty"\``, `ActiveTurnStartedAt int64 \`json:"active_turn_started_at,omitempty"\``, and a flattened usage — define `type Usage struct { InputTokens, OutputTokens, CacheReadTokens, TotalTokens int64 (snake_case json,omitempty) }` and `Usage *Usage \`json:"usage,omitempty"\`` (hubapi must not depend on appwire — mirror the GoalStatus flattening precedent). Add a hub-side converter `hubUsageFromAppwire(*appwire.SerfUsage) *hubapi.Usage`.
  - `cmd/serf-hub/web_workspace.go` `workspaceData`:
    - Roster/live-local path (~315–327): from `status` (`daemonStatus`) map `data.WorkMillis = status.WorkMillis`, `data.Usage = status.Usage`, `data.ActiveTurnStartedAt = status.ActiveTurnStartedAt`. Also populate the context-gauge fields here so the lean `/state` path (B3) needs no turns fetch: `data.ContextPercent = int(status.ContextPressure*100)`, `data.ContextWindow = status.ContextWindow`, `data.ContextNumbers = formatContextNumbers(status.ContextUsed, status.ContextWindow, status.ContextRemaining)`.
    - Past-meta literal (`web_workspace.go` ~353–364, the ended-only branch): map `WorkMillis: pe.Meta.WorkMillis` and `Usage: serfUsageFromCumulative(pe.Meta.CumulativeUsage)` (a hub helper: nil when all-zero, else `*appwire.SerfUsage`). `ActiveTurnStartedAt` stays 0 (ended).
  - `cmd/serf-hub/web_format.go` `workspaceDataFromAppThread` (~27, remote/appwire path): map `WorkMillis: thread.Serf.WorkMillis`, `Usage: thread.Serf.Usage`, `ActiveTurnStartedAt: thread.Serf.ActiveTurnStartedAt`.
  - `cmd/serf-hub/web_api_tree.go`:
    - `hubDetailFromAppThread` (~306 literal): map `WorkMillis`, `Usage: hubUsageFromAppwire(thread.Serf.Usage)`, `ActiveTurnStartedAt` from `thread.Serf` (the live branch keeps this mapper).
    - `apiSessionDetail` ended-branch literal (~492–507): add `WorkMillis: wd.WorkMillis`, `Usage: hubUsageFromAppwire(wd.Usage)`, `ActiveTurnStartedAt: wd.ActiveTurnStartedAt` (the fields flow from the same `wd` seed; the live branch's `detail = appDetail` replacement at ~525 keeps the `hubDetailFromAppThread` values — no clobber).
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'SessionDetail|WorkspaceData|Format|apiSession' -count=1` → green. `golangci-lint run ./...`.
- [ ] **Commit** — `git add cmd/serf-hub/web_types.go cmd/serf-hub/web_workspace.go cmd/serf-hub/web_format.go cmd/serf-hub/web_api_tree.go hubapi/types.go cmd/serf-hub/web_api_tree_test.go cmd/serf-hub/web_format_test.go` → `feat(hub-web): carry work time + token totals through WorkspaceData/StatusInfo/SessionDetail`.

### Task B3 — Lean `/state` path + consolidated status row (state + metrics clusters)

- [ ] **Failing test** — `cmd/serf-hub/web_test.go`: a `/state` render for a session with non-nil usage shows the metrics cluster: work-time (from `WorkMillis`), uncached `↑`/`↓` **labeled uncached**, and a hover/`title=` breakdown carrying cache-read + total; a session with nil usage shows **no** token cluster (no `↑0 ↓0`). Also assert the `/state` render does NOT trigger a turns fetch (TurnCount comes from `StatusInfo.Turns`) and that `TestWeb_State_RendersInputStatusPartial` plus the four other `apiSessionDetail` JSON-API callers (`web_api_tree.go:446`, `:457`, `web_session.go:356` `ensureSessionActionAvailable`, and the shared detail render) keep a correct `TurnCount` (the L6 regression). Run → fail.
- [ ] **Implement — lean path**:
  - Add `func (s *WebServer) apiSessionState(id string) (hubapi.SessionDetail, bool)` in `cmd/serf-hub/web_api_tree.go`: calls `s.workspaceData(id)` **once**; seeds `SessionDetail` from `wd` (State/TurnCount=`wd.TurnCount` which the roster path set from `StatusInfo.Turns`; WorkingDir/Branch/context/metrics); for a live session does a **lean** `ReadThread(appwire.ThreadReadParams{Ref: appRef, IncludeTurns: false})` and maps `thread.Serf.{context, goal, usage, workMillis, activeTurnStartedAt, ActiveTurnID}` via `hubDetailFromAppThread` then **overrides `TurnCount = wd.TurnCount`** (because `completedTurnCount(thread.Turns)` is 0 without turns — the L6 point). Ended sessions keep the `wd` metrics. This drops the second `workspaceData` call and the `IncludeTurns` transcript fetch.
  - `cmd/serf-hub/web_workspace.go` `renderInputStrip` (~478): replace the `workspaceData` + `apiSessionDetail` pair with a single `apiSessionState(id)` call; add `"Title": detail.Title` and `"OOBTitle": true` to the map (OOBTitle is set only here); add `"WorkMillis": detail.WorkMillis`, `"Usage": detail.Usage`, `"ActiveTurnStartedAt": detail.ActiveTurnStartedAt`. Keep the existing State/TurnCount/context/goal keys sourced from `detail`.
  - The shared `apiSessionDetail` (and its `IncludeTurns: true`) is untouched — its four JSON-API/action callers keep full TurnCount.
- [ ] **Implement — status row template** (`cmd/serf-hub/templates/partials/input_strip.html`): after the context cluster, add the metrics cluster, all guarded:
  - work-time: `{{if or .WorkMillis .ActiveTurnStartedAt}}<span class="status-item work" data-work-millis="{{.WorkMillis}}" data-active-turn-started="{{.ActiveTurnStartedAt}}"><span class="status-key">work</span> <span class="status-value work-time">{{formatWorkMillis .WorkMillis}}</span></span>{{end}}` (the `data-*` attributes feed the client 10s tick in B4; add a `formatWorkMillis` template func + Go helper rendering e.g. `45s`/`3m`/`1h 4m`, mirroring the TUI `compactDuration`).
  - tokens: `{{if .Usage}}<span class="status-item tokens" title="uncached ↑{{.Usage.InputTokens}} ↓{{.Usage.OutputTokens}} · cache-read {{.Usage.CacheReadTokens}} · total {{.Usage.TotalTokens}}"><span class="status-key">tok</span> <span class="status-value">↑{{formatTokenCount .Usage.InputTokens}} ↓{{formatTokenCount .Usage.OutputTokens}}</span></span>{{end}}` — uncached ↑/↓ labeled via the hover `title`; nil Usage hides the whole cluster (no `↑0 ↓0`). Reuse/add a `formatTokenCount` template func matching `renderer-format.js:formatTokenCount` and `web_format.formatTokenCount`.
  - Add a liveness placeholder span the client will drive (B5): `<span class="status-item liveness-inline" data-liveness hidden></span>`.
- [ ] **Implement — CSS** (`cmd/serf-hub/assets/style.css`): add `.input-status .work`, `.input-status .tokens`, `.input-status .liveness-inline` (+ its `[data-level="quiet"]`/`[data-level="concern"]` amber/glyph rules) mirroring the dying `.liveness` block's visual language.
- [ ] **Run** `go test ./cmd/serf-hub/... -run 'State|InputStatus|SessionDetail|TurnCount' -count=1` → green. `golangci-lint run ./...`.
- [ ] **Commit** — `git add cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_workspace.go cmd/serf-hub/templates/partials/input_strip.html cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go` → `feat(hub-web): lean /state fetch + consolidated status row with work time and tokens`.

### Task B4 — Event-driven refresh wiring (dispatch + 30s fallback + 10s active tick)

- [ ] **Failing test (jstest)** — add `cmd/serf-hub/jstest/test-status-refresh.js` (plain Node/JSDOM script, `pass()`/`process.exit`): stub htmx (`window.htmx = { trigger: (el, name) => { triggers.push([el, name]); } }`); drive the renderer's `THREAD_STATUS_CHANGED`, `turn/started`, `turn/completed` paths; assert each fires `htmx.trigger(document.body, "serf-hub:status-refresh")` (NOT `document.dispatchEvent` — an `every … from:body` listener never sees a `dispatchEvent`). Assert the 10s tick fires a refresh only while a turn is active. Run `cd cmd/serf-hub/jstest && NODE_PATH=… node test-status-refresh.js` → fail.
- [ ] **Implement — template trigger** (`cmd/serf-hub/templates/partials/workspace.html` ~104–110): change `#input-status` `hx-trigger` from `load, every 2s` to `load, serf-hub:status-refresh from:body, every 30s`.
- [ ] **Implement — dispatch** (`cmd/serf-hub/assets/renderer.js`):
  - In `THREAD_STATUS_CHANGED` (~899–910), alongside the existing `document.dispatchEvent(serf-hub:thread-status)`, add `if (window.htmx) window.htmx.trigger(document.body, "serf-hub:status-refresh");`.
  - At the `turn/started` and `turn/completed` handling sites (the frame handlers that process `NotifyTurnStarted`/`NotifyTurnCompleted`), fire the same `htmx.trigger(document.body, "serf-hub:status-refresh")`.
  - Add a client 10s tick: while `this.state === "active"`, `setInterval(() => window.htmx && window.htmx.trigger(document.body, "serf-hub:status-refresh"), 10000)` (context-gauge + work-time freshness), started on entering active and cleared on leaving/teardown (mirror `startLivenessTimer` lifecycle at ~2088–2091 and the teardown at ~106–109).
- [ ] **Run** the new jstest → pass; sanity-run existing renderer jstests to confirm no break. Run `go test ./cmd/serf-hub/... -run 'State|Workspace' -count=1` (template still parses).
- [ ] **Commit** — `git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-status-refresh.js` → `feat(hub-web): event-driven status-row refresh (serf-hub:status-refresh + 30s fallback + 10s active tick)`.

### Task B5 — Relocate liveness into the status row; re-bind across swaps; port both jstest suites

- [ ] **Port the two jstest suites first (they define the contract)** — copy `cmd/serf-hub/jstest/test-renderer-liveness.js` and `test-renderer-liveness-selfheal.js` to drive the NEW inline spans (`.liveness-inline[data-liveness]` inside `#input-status`) instead of the `.liveness` sibling. Keep every case: frame-stamps-lastFrameAt; stall band (`no updates for`, `data-stalled="true"` on `#conversation`, dot loses pulse); recovery; idle-never-stalls; calm-quiet `~30s`/`~1m` buckets (coarse, not per-second; pulse survives); concern band (glyph, `data-stalled`, pulse dropped); recovery-from-concern; self-heal: calm-quiet no heal, entering-concern heals once, staying-in-concern no re-fire, new-episode re-arms, no-live-stream no heal. Run both → fail (renderer still targets `.liveness`).
- [ ] **Implement** (`cmd/serf-hub/assets/renderer.js`):
  - Replace `ensureLivenessEl` (~2074–2086): instead of creating a `.liveness` sibling after `#conversation`, **re-acquire** the inline span via `this.livenessEl = document.querySelector('#input-status [data-liveness]')` (may be null when the row isn't mounted). Call this re-acquire on every `htmx:afterSwap` whose target is `#input-status` (add a listener beside the existing swap listeners at ~5316–5342, patterned on `sidebar.js:678` which inspects `e.target.id`).
  - `refreshLiveness` (~2104–2157): guard `const el = this.livenessEl; if (!el || !el.isConnected) return;` so the 3s ticker no-ops on a detached node (the row is an innerHTML-swap target). Keep writing `this.conversation.dataset.stalled` (its consumers — the pulse gate + CSS — are unchanged). Keep `attemptLivenessSelfHeal` and its coupling unchanged. Render into the inline span (set `data-level`, text/glyph, `hidden`).
  - Delete the dynamic `.liveness` creation entirely; delete its CSS block (`cmd/serf-hub/assets/style.css` ~2134–2165). The inline liveness CSS now lives on `.liveness-inline` (added in B3).
  - **Note the stale spec anchor:** the spec cites `renderer.js:4691` as the "pulse gate reading `data-stalled` on `#conversation`"; the current pulse gate is `applyStatusDotPulse` at `renderer.js:~5230–5249` (reads `.conversation[data-stalled="true"]` at ~5237). It is unchanged by this task — only verify it still reads correctly after the relocation.
- [ ] **Run** both ported jstest suites → pass. Re-run `test-status-refresh.js` and the context-pressure jstest → pass. Run `go test ./cmd/serf-hub/... -run 'State|InputStatus' -count=1`.
- [ ] **Commit** — `git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-liveness.js cmd/serf-hub/jstest/test-renderer-liveness-selfheal.js` → `feat(hub-web): move quiet/stall liveness into the status row with afterSwap re-bind`.

---

## Phase C — TUI, end-to-end, full-repo gates

### Task C1 — TUI status: fields + chip strip + details drawer

- [ ] **Failing test** — `cmd/serf-tui/hub_status_test.go` (or `hub_types_test.go`): assert `hubDetailFromThread` maps `thread.Serf.{Usage,WorkMillis,ActiveTurnStartedAt}` onto `hubSessionDetail`; assert `renderHubSessionStatus` output contains a work-time line and a token breakdown line including cache-read when `Usage` is non-nil, and neither when nil. Run `go test ./cmd/serf-tui/... -run 'HubDetail|HubSessionStatus|Chip' -count=1` → fail.
- [ ] **Implement**:
  - `cmd/serf-tui/hub_types.go`: add to `hubSessionDetail` (~56): `WorkMillis int64`, `Usage *appwire.SerfUsage`, `ActiveTurnStartedAt int64` (reuse the appwire type, consistent with the existing `*appwire.GoalState`/`appwire.QueueState` fields). Map them in the `hubDetailFromThread` return literal (~214) from `thread.Serf`.
  - `cmd/serf-tui/hub_status.go`: in `renderHubSessionStatus` (~12) add a `Work:` line (`formatWorkMillis`, mirror `compactDuration`) and, when `detail.Usage != nil`, a `Tokens:` line with the full breakdown `↑<in> ↓<out> · cache-read <cr> · total <tot>` (reuse `formatTokens`). Add the compact work-time + `↑/↓` to the chip strip (`formatContextFragment`'s caller / meta strip) — the drawer carries the full breakdown incl. cache-read; the chip strip stays compact. Dashboard render is unchanged.
  - Update `cmd/serf-tui/tui_samples.go` sample details if a golden/snapshot test references them.
- [ ] **Run** `go test ./cmd/serf-tui/... -count=1` → green. `golangci-lint run ./...`.
- [ ] **Commit** — `git add cmd/serf-tui/hub_types.go cmd/serf-tui/hub_status.go cmd/serf-tui/hub_status_test.go cmd/serf-tui/tui_samples.go` → `feat(tui): render session work time + token totals (chip strip + details drawer)`.

### Task C2 — End-to-end scenario cards

Use the e2e-scenario-testing skill. Build fresh binaries (`go build -o /tmp/serf ./cmd/serf`, `serf-hub`, `serf-tui`), run a live model per `reference_serf_live_run` (`--model oai-work/<model>`), and author falsifiable scenario cards:

- [ ] **Card: turn completes → metrics advance across surfaces.** Start a session, send one prompt to completion; assert the web status row and the TUI show a non-zero work time and non-zero `↑/↓` tokens that increased from before the turn.
- [ ] **Card: interrupt → work time advances.** Start a long turn, interrupt it; assert work time increased (interrupted turns count) and the turn renders `interrupted` (not `completed`) — validates the A6 status-preservation refinement.
- [ ] **Card: daemon restart → totals survive AND next-turn autosave doesn't reset.** Run a turn, note totals, restart the daemon (resume the session), assert totals are preserved; run one more turn; assert totals == prior + new turn (the K2 guarantee end-to-end).
- [ ] **Card: ended session renders totals.** Let a session end; open it from the Past index; assert the status row shows the persisted work time + tokens.
- [ ] **Card: pre-feature ended session → clusters absent, not zero.** Point the hub at a session meta written before this feature (no `work_millis`/`cumulative_usage`); assert the metrics clusters are absent (no `↑0 ↓0`, no `work 0s`), not rendered as zero (the L9 legacy bound + nil-usage-hides).
- [ ] **Record** the card outcomes; do not commit binaries. If a card fails, fix the root cause (not the card).

### Task C3 — Full-repo gates

- [ ] `cd agent && golangci-lint run ./... && go test ./...` → green.
- [ ] From repo root: `golangci-lint run ./...` and `go test ./server/... ./internal/... ./cmd/... ./appwire/... ./hubapi/... -count=1` → green. Then `make test` (all modules) and `make vet`.
- [ ] `make fuzz` (confirms the appwire decode goldens still hold after the new `SerfThread`/`Turn` fields; if `Test*Golden` drift, `make fuzz-goldens` and re-verify).
- [ ] Run the jstest suite: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` → both liveness suites + `test-status-refresh` + context-pressure green.
- [ ] `make lint` (naming, internal, docs, golangci, generated, secret-scan) → green.
- [ ] **Commit** any gate-driven fixups with a focused message; do not `git add -A`.

---

## Self-review — Review-log folds mapped to tasks

| Review item | Mechanism | Task · named test |
| --- | --- | --- |
| K1 (no turn-end event; completion sites duration-less) | new `EventTurnEnded` from the boundary + projector timing | A5 (`TurnEnded`), A6 (`TestProjectorTurnEndedStampsTiming`) |
| K2 (first post-restore autosave overwrote totals) | Session/contextmgr homes + restore seeding | A3 (`TestRestoreSeedsMetricsIntoMeta`), A4 (`TestRestoreThenTurnAutosaveKeepsPriorTotals`) |
| K3 (CreatedAt "fix" had no mechanism) | `Session.createdAt` + capture sites + `Meta()` maps it | A2 (`TestMeta_CreatedAtStableAcrossCalls`) |
| K4/L4 (WS3 reciprocal coordination) | golden/fixture rebase note | A1 coordination note |
| K5/L1 (ended chain routed through a live-only mapper) | `WorkspaceData` both-path seed → `SessionDetail` ended literal | B2 (`apiSessionDetail` ended test) |
| K6 (CumUsage shape/tag; value-struct omitempty never omits; golden churn) | `schema.CumulativeUsage` + `omitzero` + golden regen | A1 (`TestSessionMeta_CumulativeUsageOmitzero`, `TestSessionMeta_Golden*`) |
| K7 (estimate omissions) | — | Estimate note below |
| L2 (cached handle into swap-destroyed spans) | afterSwap re-bind + `isConnected` detached-node guard | B5 (ported liveness suites) |
| L3 (terminal-error Close before boundary drops dying turn) | `Close()` accumulates before flipping state | A4 (`TestWorkMillis_CloseMidTurnCounts`) |
| L5 (nested value usage renders ↑0 ↓0) | pointer `Usage *SerfUsage`; nil when all-zero | A7 (`SerfThreadMetrics` omit test), B3 (nil-usage-hides test) |
| L6 (dropping IncludeTurns zeroed four other callers) | lean `/state`-only path; shared detail keeps IncludeTurns | B3 (`TurnCount` four-caller test) |
| L7 (liveness suites / data-stalled / CSS dispositions) | both suites ported; `data-stalled` kept on `#conversation`; `.liveness` + CSS die | B5 |
| L8 (cache-read had no web surface) | uncached ↑/↓ labeled + hover/title breakdown w/ cache-read + total | B3 (hover-breakdown test), C1 (drawer) |
| L9 (legacy ended sessions zero forever) | stated bound; nil-usage hides | C2 (pre-feature ended card) |
| Decision 4 (every outcome counts) | accumulate at single boundary incl. interrupt/fail | A4 (completed/interrupted/failed/multi-turn tests) |
| Replay drops StartedAt; transcript can't span | `appTurnsFromNotifications` copies timing; transcript StartedAt-only | A6 (replay test, transcript test) |

**Type/name consistency:** `CumulativeUsage` (schema, persistence), `SerfUsage` (appwire), `hubapi.Usage` (hub JSON API), all four-field `int64` (input/output/cache-read/total); `WorkMillis`/`workMillis` and `ActiveTurnStartedAt`/`activeTurnStartedAt`/`active_turn_started_at` consistent per layer's json convention; `EventTurnEnded`/`TurnEndedData`/`TurnDurationMS`; `formatWorkMillis`/`formatTokenCount` shared web+TUI grammar.

**Estimate check:** the plan is ~15 tasks. Rough loc incl. tests — Phase A ~480–620 (schema+golden, Session homes+seeds, accumulation+Close, EventTurnEnded, projector+replay+transcript, wire+callbacks); Phase B ~300–420 (ghost delete+port, WorkspaceData/StatusInfo/SessionDetail chain, lean `/state`+row, refresh wiring, liveness relocation + 2 suite ports); Phase C ~110–170 (TUI + e2e cards). Total ~890–1,210, in line with the spec's 800–1,150 (slightly above the top on the pessimistic end, driven by the timing-stamp projector refinement and the extra hubapi.Usage flattening — flagged, not a scope change).
