# Stable Session-Control Turn Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make AppWire publish the durable stable turn identity while a retry-safe user turn is processing so Steer and Stop target the actual active turn, including before `EventUserInput` is projected.

**Architecture:** The Session remains the durable authority and passes its runnable `StableTurnID` through the existing pre-claim callback. The daemon server atomically marks processing and reserves that exact identity in the AppWire projector; generic non-mutation processing retains numeric projector reservations.

**Tech Stack:** Go, Evener Session lifecycle, AppWire WebSocket protocol, deterministic channel handshakes, scripted `llm.ProviderAdapter`, Kata `c2ty`.

## Global Constraints

- Work only in `/Users/jesse/prime-radiant/toil-suite/evener/.worktrees/c2ty-session-control-id` on `wip/c2ty-session-control-id`.
- Do not restart session `0343wE3LB14m5xoC2CBMiD` or mutate any of its persisted artifacts.
- Preserve the intentional separation between durable `turn_mN` identities and numeric projector `turn_N` identities.
- Tests must exercise real Session persistence, AppWire routing, and lifecycle behavior with the fake boundary only at the scripted LLM provider.
- Tests must use channel/event handshakes rather than sleeps or polling races.
- Production code may be written only after both Steer and Stop regressions fail behaviorally against the existing implementation.
- Make the smallest coherent change; do not add backward compatibility or an ID translation layer.

---

### Task 1: Prove Steer and Stop fail against the projected temporary identity

**Files:**
- Modify: `cmd/evener/serve_state_test.go`

**Interfaces:**
- Consumes: `runServeWithDeps`, `server.Server`, `appwire.Client.TurnStart`, `appwire.Client.TurnSteer`, `appwire.Client.TurnInterrupt`, and the real Session event bridge.
- Produces: `TestRunServeRetrySafeTurnPublishesControllableStableIdentity` and test-only lifecycle helpers that hold the pre-claim window without changing production behavior.

- [ ] **Step 1: Add a real lifecycle test harness**

Add a wrapper around the real server. It forwards every operation and parks only
the post-processing `SetState(SessionProcessing)` boundary, after the active ID
has been published but before `ProcessClientMutationStart` can claim or emit
`EventUserInput`:

```go
type sessionControlIdentityServer struct {
	*server.Server

	processingStarted  chan struct{}
	releaseProcessing  chan struct{}
	processingFinished chan struct{}
	interruptEntered   chan struct{}
	releaseOnce        sync.Once
}

func (s *sessionControlIdentityServer) SetState(state string) {
	s.Server.SetState(state)
	if state == string(agent.SessionProcessing) {
		close(s.processingStarted)
		<-s.releaseProcessing
	}
}

func (s *sessionControlIdentityServer) SetProcessing(processing bool) {
	s.Server.SetProcessing(processing)
	if !processing {
		close(s.processingFinished)
	}
}

func (s *sessionControlIdentityServer) SetRetrySafeTurnFunctions(functions server.RetrySafeTurnFunctions) {
	interrupt := functions.Interrupt
	functions.Interrupt = func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		close(s.interruptEntered)
		return interrupt(ctx, params)
	}
	s.Server.SetRetrySafeTurnFunctions(functions)
}
```

Keep channel closure single-owner in the final helper and add cleanup that
releases a parked turn before shutting down the disposable daemon. Use the
existing `waitForServeTestRendezvous` and real WebSocket client setup from
`TestRunServe_StreamErrorPublishesIdleStatus`.

- [ ] **Step 2: Add the Steer and incorporation subtest**

Start a retry-safe turn and wait for `processingStarted`. While the lifecycle is
held:

```go
thread, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
if err != nil {
	t.Fatalf("ThreadRead: %v", err)
}
activeTurnID := thread.Thread.Evener.ActiveTurnID

observedServer.RecordAppEvent(events.SessionEvent{
	Kind:      events.EventWarning,
	SessionID: entry.SessionID,
	Data:      events.WarningData{Message: "pre-input projection"},
})

if err := client.TurnSteer(ctx, appwire.TurnSteerParams{
	ClientMutationID: "stable-steer",
	Ref:              ref,
	ExpectedTurnID:   activeTurnID,
	Input:            []appwire.InputItem{{Type: "text", Text: "steer accepted"}},
}); err != nil {
	t.Fatalf("TurnSteer with published active ID %q: %v", activeTurnID, err)
}
```

Call Steer first so the pre-fix run fails on the real durable precondition, then
assert the published ID equals `TurnStartResponse.Turn.ID`. Project the warning,
read again, and assert the same stable ID survives. Release the turn, await
`processingFinished`, read with
`IncludeTurns: true`, and assert a real `userMessage` item carries the original
start mutation ID and text.

- [ ] **Step 3: Add the Stop subtest**

Start a fresh disposable daemon and hold the same pre-claim window. Read its
published active ID, launch `TurnInterrupt` in a goroutine, await
`interruptEntered`, then release processing:

```go
stopDone := make(chan error, 1)
go func() {
	stopDone <- client.TurnInterrupt(ctx, appwire.TurnInterruptParams{
		ClientMutationID: "stable-stop",
		Ref:              ref,
		ExpectedTurnID:   activeTurnID,
	})
}()

<-observedServer.interruptEntered
observedServer.release()
if err := <-stopDone; err != nil {
	t.Fatalf("TurnInterrupt with published active ID %q: %v", activeTurnID, err)
}
```

Await the real processing completion and assert `thread/read` returns idle with
no active turn.

- [ ] **Step 4: Run the focused test and record behavioral RED evidence**

Run:

```bash
go test ./cmd/evener -run '^TestRunServeRetrySafeTurnPublishesControllableStableIdentity$' -count=1 -v
```

Expected: both subtests compile and fail because `thread/read` publishes a
numeric `turn_N`; Steer and Stop submit that value to the durable store, which
rejects it with `turn is not active`. A compile failure, timeout caused by test
setup, or `[no tests to run]` is not acceptable RED evidence.

- [ ] **Step 5: Commit the behavioral regression**

```bash
git add cmd/evener/serve_state_test.go
git commit -m "test: reproduce retry-safe session control ID split"
```

The commit body must record the exact RED command and both behavioral failures.

---

### Task 2: Publish the runnable durable identity atomically with processing

**Files:**
- Modify: `agent/session_client_mutation.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Modify: `server/server.go`
- Modify: `cmd/evener/serve.go`
- Modify: `server/appwire_server_test.go`

**Interfaces:**
- Consumes: the durable snapshot's `ActiveTurnID` and existing AppWire projector reservation lifecycle.
- Produces: `ProcessClientMutationStart(context.Context, func(string))`, `AppEventProjector.ReserveStableTurnID(string)`, `Server.SetProcessingTurn(string)`, and the serve-loop call that connects them.

- [ ] **Step 1: Return the runnable stable identity from Session selection**

Replace the boolean-only runnable scan with one that returns the durable active
identity without altering eligibility:

```go
func (s *Session) runnableClientMutationStartTurnID() (string, bool) {
	if s == nil || s.clientMutations == nil {
		return "", false
	}
	snapshot := s.clientMutations.snapshot()
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method == clientMutationMethodStart &&
			(pending.ExecutionState == "accepted" || pending.ExecutionState == "incorporated") {
			return snapshot.ActiveTurnID, true
		}
		if pending.Method == clientMutationMethodQueue &&
			pending.ExecutionState == "incorporated" &&
			pending.TurnID != "" &&
			snapshot.ActiveTurnID == pending.TurnID {
			return snapshot.ActiveTurnID, true
		}
	}
	if len(snapshot.InputQueue) > 0 {
		record := snapshot.Journal[snapshot.InputQueue[0].ClientMutationID]
		if record.Method == clientMutationMethodQueue &&
			record.StableTurnID != "" &&
			snapshot.ActiveTurnID == record.StableTurnID {
			return snapshot.ActiveTurnID, true
		}
	}
	return "", false
}
```

Keep `hasRunnableClientMutationStart` as a narrow boolean wrapper for its other
callers. Change `ProcessClientMutationStart` to accept `func(string)`, obtain the
ID before the callback, and pass it before claiming so cancellation/Stop remains
wired during claim persistence.

- [ ] **Step 2: Let the projector reserve an externally stable identity**

Add:

```go
func (p *AppEventProjector) ReserveStableTurnID(turnID string) {
	invariant.Hold(strings.TrimSpace(turnID) != "", "appprojector: reserve empty stable turn id")
	p.reservedTurnID = turnID
}
```

Do not increment `nextTurn`: `turn_mN` is deliberately outside the projector's
numeric `turn_N` namespace. Existing `startTurn` consumes this reservation when
`EventUserInput` arrives.

- [ ] **Step 3: Add the atomic server transition**

Add `SetProcessingTurn` beside `SetProcessing`:

```go
func (s *Server) SetProcessingTurn(turnID string) {
	invariant.Hold(strings.TrimSpace(turnID) != "", "server: process empty stable turn id")
	s.mu.Lock()
	s.processing = true
	s.ensureAppProjectorLocked("")
	s.appProjector.ReserveStableTurnID(turnID)
	s.appActiveTurnID = turnID
	s.appReservedTurnID = ""
	s.mu.Unlock()
}
```

Import the repository invariant package. Rewrite the adjacent generic
`SetProcessing` comment so it accurately distinguishes numeric generic
reservations from stable client-mutation processing; remove stale claims that
production `turn/start` still calls `reserveAppTurnIDForStart`.

- [ ] **Step 4: Wire the stable ID through serve**

Add `SetProcessingTurn(string)` to `serveServer`. Update only the durable-start
callback:

```go
result, processed, processErr = sess.ProcessClientMutationStart(turnCtx, func(turnID string) {
	srv.SetCancelFunc(cancelTurn)
	setMutationRunner(cancelTurn, runnerDone)
	srv.SetProcessingTurn(turnID)
	srv.SetState(string(agent.SessionProcessing))
})
```

Leave every other `SetProcessing(true)` call unchanged.

- [ ] **Step 5: Correct the stale generic-reservation test documentation**

Update the old c2ty comments and name in `server/appwire_server_test.go` so the
test describes the remaining generic-processing fallback rather than claiming
production retry-safe `turn/start` still calls `reserveAppTurnIDForStart`. Keep
its behavior assertion unchanged: generic processing must still publish a
non-empty active ID.

- [ ] **Step 6: Run focused GREEN verification**

Run:

```bash
gofmt -w agent/session_client_mutation.go internal/appprojector/appwire_projection.go server/server.go cmd/evener/serve.go cmd/evener/serve_state_test.go server/appwire_server_test.go
go test ./cmd/evener -run '^TestRunServeRetrySafeTurnPublishesControllableStableIdentity$' -count=1 -v
go test ./agent -run 'ClientMutationStart' -count=1
go test ./server -run 'Processing|TurnStart' -count=1
```

Expected: the lifecycle test passes both `steer_and_incorporate` and `stop`; all
focused Session and server tests pass with no warnings.

- [ ] **Step 7: Mutation-check the regression**

Temporarily change the durable-start callback back to `srv.SetProcessing(true)`,
run the focused lifecycle test, and confirm both control subtests fail with
`turn is not active`. Restore `SetProcessingTurn(turnID)` with `apply_patch` and
rerun the focused test green. Do not use `git checkout --` on touched files.

- [ ] **Step 8: Commit the minimal implementation**

```bash
git add agent/session_client_mutation.go internal/appprojector/appwire_projection.go server/server.go cmd/evener/serve.go server/appwire_server_test.go
git commit -m "fix: keep retry-safe session controls on the stable turn"
```

Document why the projector's numeric counter is not advanced and why the
callback stays before the durable claim.

---

### Task 3: Verify the complete change and close the Kata

**Files:**
- Modify only if verification exposes a real defect in the touched behavior.

**Interfaces:**
- Consumes: the two committed changes and repository gate contract.
- Produces: fresh focused, affected-package, lint/build/full-test evidence and a typed Kata close tied to the implementation commit.

- [ ] **Step 1: Inspect the exact committed diff and worktree state**

```bash
git status --short --branch
git diff --check HEAD~2..HEAD
git diff --stat HEAD~2..HEAD
```

Confirm only the design, plan, lifecycle test, and minimal identity wiring are
present.

- [ ] **Step 2: Run affected-package tests**

```bash
go test ./agent ./server ./cmd/evener -count=1
```

Capture the bare exit code. Any sighted failure requires root-cause diagnosis
before proceeding.

- [ ] **Step 3: Run the canonical merge-approval gate**

```bash
make merge-approval-gate
```

This serially runs lint, runtime build, full deterministic tests, and developer
tooling selftests. Report unavailable prerequisites as limitations; do not infer
success from partial logs.

- [ ] **Step 4: Run diff and repository-state checks**

```bash
git diff --check HEAD~2..HEAD
git status --short --branch
git log -3 --oneline --decorate
```

The worktree must be clean and remain on `wip/c2ty-session-control-id`.

- [ ] **Step 5: Close Kata c2ty with typed evidence**

```bash
implementation_commit=$(git rev-parse HEAD)
kata close c2ty --done \
  --message "Retry-safe turns now publish their durable stable identity while processing; real AppWire lifecycle coverage proves Steer and Stop target that identity and pending input is incorporated." \
  --commit "$implementation_commit"
```

Then run `kata show c2ty --agent` and verify the status and evidence before
reporting completion. Do not push or merge the worktree unless Jesse asks.
