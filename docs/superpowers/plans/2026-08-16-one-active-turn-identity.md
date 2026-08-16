# One Active Turn Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every turn — however it started — exactly one identity, so the
`activeTurnId` the daemon publishes is always the one its mutation
preconditions accept, and Steer / Send / Stop work on every turn.

**Architecture:** The session becomes the sole minter and announcer of a
turn's identity. At the one place a turn begins (`Session.processOneInput`)
the session reserves a durable turn id if a client mutation has not already
reserved one, announces it through a callback, and releases it at turn end if
it was the reserver. `serve.go` publishes whatever the session announces; the
`server` package stops minting turn ids for live turns entirely.

**Tech Stack:** Go 1.25 multi-module workspace (`agent`, root module's
`server` / `internal/appprojector` / `cmd/serf`), `appwire` JSON-RPC.

**Spec:** this document — the Diagnosis section below is the spec.

## Global Constraints

- **No backward compatibility.** Jesse's call. Delete the superseded path;
  do not keep it behind a flag or a fallback.
- Every turn id that reaches a client for a *live* turn is
  `appwire.ClientMutationTurnID(n)` — `turn_m<n>`. The projector's own
  `turn_<n>` family survives only for synthetic prelude/gap ids and for
  transcript replay.
- The client-mutation **journal** stays a record of client mutations. An
  agent-started turn reserves an id and nothing else: no journal record, no
  budget reservation, no `AcceptedTurns` bump.
- Gates, in order, from the repo root: `make lint`, `make build`,
  `go test ./...`, and the module suites (`cd agent && go test ./...`).
- Never `git stash`; never `git checkout <file>` to undo.

---

## Diagnosis (the spec)

The daemon keeps two "active turn id"s that answer different questions:

- **`turn_m<n>` — durable.** Minted at *accept* time, before the turn runs,
  and returned synchronously in the `turn/start` response
  (`agent/session_client_mutation.go:216,235`). Persisted to
  `<state-dir>/mutations/<SID>.json`. `clientMutationSnapshot.ActiveTurnID`
  is documented as "the sole durable authority used by retry-safe mutation
  preconditions" (`agent/session_client_mutation.go:128-131`), and it is the
  value every mid-turn mutation is compared against:
  `session_client_mutation_queue.go:123,325,392,497` and
  `session_client_mutation.go:411`.
- **`turn_<n>` — the projector's.** Minted in memory when the first event of
  a round arrives (`internal/appprojector/appwire_projection.go:1652,1724`),
  re-derived from the persisted transcript entry count on resume
  (`SeedPersistedTurns`). It is a stream-attachment label and must exist for
  every round, including ones no client asked for.

`server.Server` publishes the projector's value on the wire —
`thread.Serf.ActiveTurnID = projection.activeTurnID`
(`server/appwire_runtime.go:551,652`).

`954e5ff93` ("publish durable turn identity to session controls") collapsed
the two **for client-started turns**: `Server.SetProcessingTurn` calls
`AppEventProjector.ReserveStableTurnID`, so the projector adopts the durable
id. Its own message records that it kept "generic SetProcessing behavior for
non-mutation continuations".

Those non-mutation turns are the bug. `cmd/serf/serve.go:990` `processMessage`
forks on `msg.ClientMutationStart`; the `else` branch calls
`srv.SetProcessing(true)`, which mints `turn_<n>` (`server/server.go:713-716`)
and never touches the durable authority. The affected turn kinds are
`EntryNotification` (job / watch / delegate-attention wakes),
`EntryContinuation` (goal continuations), and the post-interrupt queue-drain
re-arm at `serve.go:1001`.

Result: the UI reads `turn_6` off the wire, sends it as `expectedTurnId`, the
daemon compares it with `turn_m6`, and `turn/steer`, `turn/queue`,
`turn/drainAsSteer`, `turn/promoteQueuedAsSteer` and `turn/interrupt` are all
rejected with `Conflict("turn is not active")`. The composer surfaces no
error; the draft just stays in the box.

**Evidence.** Jesse's live session `0348HuXSlWRtoLEoQ4EOE8`: its mutation
journal holds 3× `turn/steer`, 1× `turn/queue`, 1× `turn/interrupt`, every one
with `expected_turn_id: turn_5`, every one `rejected — "turn is not active"`,
while the daemon's own active turn was `turn_m6`. Reproduced deterministically
on a disposable stack: `goal/set` on an idle session publishes
`activeTurnId: turn_2`, and Steer and Stop from the real browser UI both land
in the journal as `rejected | turn is not active`.

**Decision.** `clientMutationSnapshot.ActiveTurnID` means *the turn that is
running*, not *the turn a client mutation reserved*. The session mints and
announces it for every turn; nothing else mints turn ids for live turns.

### Why not the alternatives

- *Route agent-started turns through `AcceptClientMutationStart`.* Drags
  journal records, budget reservations and `MaxTurns` accounting onto turns
  no client requested.
- *Accept either id family at the precondition.* Keeps two authorities alive
  forever, and on the interrupt path
  (`session_client_mutation.go:537`, `record.StableTurnID = fence.ExpectedTurnID`)
  writes a projector-minted id into the durable journal as if it were a
  reservation.
- *Publish an empty id when the turn has no durable identity.* Honest, but
  the UI loses Steer and Stop during goal and notification turns — no way to
  stop a runaway delegate-notification turn from the web UI.

### Known follow-up, deliberately out of scope

A `Conflict` on steer / queue / interrupt is invisible in the web composer —
no toast, the draft simply stays put. That silence is why this bug went
unnoticed. Worth its own change; not this one.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `agent/session_active_turn.go` (create) | Reserve, announce and release the running turn's durable identity. One responsibility, so the client-mutation files stay about client mutations. |
| `agent/session_active_turn_test.go` (create) | Unit coverage for reserve / adopt / release across turn kinds. |
| `agent/session_lifecycle.go` (modify) | Call the reserve/announce at turn begin and the release at turn end, in `processOneInput`. |
| `agent/session.go` (modify) | `SetTurnStartedFunc`, mirroring `SetNotifyFunc`. |
| `cmd/serf/serve.go` (modify) | Wire the announcement to `srv.SetProcessingTurn`; delete the two `SetProcessing(true)` calls, the duplicated ask-gate guess, and the now-redundant publish inside the `ProcessClientMutationStart` callback. |
| `server/server.go` (modify) | `SetProcessing(bool)` becomes `ClearProcessing()`; the id-minting branch goes. |
| `internal/appprojector/appwire_projection.go` (modify) | Delete `ReserveTurnID` — nothing mints projector ids for live turns any more. |
| `cmd/serf-hub/e2e_turn_control_test.go` (modify) | Live-stack regression: a goal-continuation turn accepts steer and interrupt. |

---

### Task 1: The session owns the running turn's identity

**Files:**
- Create: `agent/session_active_turn.go`
- Create: `agent/session_active_turn_test.go`
- Modify: `agent/session.go` (add `SetTurnStartedFunc` beside `SetNotifyFunc` at :710)
- Modify: `agent/session_lifecycle.go:995-1004` (`processOneInput`)

**Interfaces:**
- Produces: `func (s *Session) SetTurnStartedFunc(f func(turnID string))` —
  serve.go wires this in Task 2.
- Produces: `func (s *Session) beginActiveTurn() (turnID string, reserved bool)` —
  package-private; `reserved` is true only when this turn minted the id, so
  the caller knows whether to release it.
- Produces: `func (s *Session) releaseActiveTurn(turnID string)` — package-private.

- [ ] **Step 1: Write the failing test**

Add to `agent/session_active_turn_test.go`. `newTestSessionForEnvctx`
(`agent/session_envctx_test.go:38`) already builds a Session with a StateDir,
which the client-mutation store needs.

```go
package agent

import (
	"context"
	"strings"
	"testing"
)

// TestAgentStartedTurnReservesAndAnnouncesADurableID pins the contract every
// mid-turn control depends on: a turn nobody requested through a client
// mutation still owns a durable turn_m<n>, the session announces that exact
// id, and the id is gone again once the turn ends. Without it the daemon
// publishes a projector id the mutation preconditions reject, which is how
// steer/send/stop silently died on goal and notification turns.
func TestAgentStartedTurnReservesAndAnnouncesADurableID(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	var announced []string
	var duringTurn string
	s.SetTurnStartedFunc(func(turnID string) {
		announced = append(announced, turnID)
		duringTurn = s.clientMutations.snapshot().ActiveTurnID
	})

	if _, err := s.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	if len(announced) != 1 {
		t.Fatalf("announced turn ids = %v, want exactly one", announced)
	}
	if !strings.HasPrefix(announced[0], "turn_m") {
		t.Fatalf("announced %q, want the durable turn_m<n> family", announced[0])
	}
	if duringTurn != announced[0] {
		t.Fatalf("durable ActiveTurnID during the turn = %q, want the announced %q", duringTurn, announced[0])
	}
	if after := s.clientMutations.snapshot().ActiveTurnID; after != "" {
		t.Fatalf("durable ActiveTurnID after the turn = %q, want it released", after)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd agent && go test ./ -run TestAgentStartedTurnReservesAndAnnouncesADurableID -v`
Expected: FAIL — `s.SetTurnStartedFunc undefined`.

- [ ] **Step 3: Add the reserve / announce / release seam**

Create `agent/session_active_turn.go`:

```go
package agent

import "primeradiant.com/serf/appwire"

// A turn's identity has one owner: this file. Whatever starts a turn — a
// client's turn/start, a queued message claimed off the input queue, a job or
// delegate notification wake, a goal continuation — the running turn carries
// exactly one durable id, and that id is what the daemon publishes as
// serf.activeTurnId. Mutation preconditions compare against the same value,
// so an id a client can see is always an id a client can name.

// SetTurnStartedFunc registers the callback the daemon uses to publish the
// running turn's identity. It mirrors SetNotifyFunc: the agent module must
// not import server, so serve.go wires this to Server.SetProcessingTurn.
func (s *Session) SetTurnStartedFunc(f func(turnID string)) {
	s.mu.Lock()
	s.turnStartedFunc = f
	s.mu.Unlock()
}

// beginActiveTurn settles the identity of the turn that is about to run and
// reports whether this call is the one that minted it.
//
// A client mutation reserves its turn id before the serve loop ever wakes —
// that is what makes turn/start retry-safe — so a non-empty ActiveTurnID here
// means the turn is already named and this call adopts it. Everything else
// mints now. reserved=false keeps the existing settle paths solely
// responsible for clearing a mutation's own id, including the interrupt
// fence they finalize.
func (s *Session) beginActiveTurn() (turnID string, reserved bool) {
	if err := s.ensureClientMutationStore(); err != nil {
		// A session with no durable store (an in-process subagent, a
		// one-shot CLI run) has no client to name a turn to. It runs
		// unnamed rather than failing the turn.
		return "", false
	}
	if existing := s.clientMutations.snapshot().ActiveTurnID; existing != "" {
		return existing, false
	}
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.ActiveTurnID != "" {
			turnID = snapshot.ActiveTurnID
			return nil
		}
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		turnID = snapshot.ActiveTurnID
		reserved = true
		return nil
	})
	if err != nil {
		return "", false
	}
	return turnID, reserved
}

// releaseActiveTurn clears an id minted by beginActiveTurn. It is a no-op for
// any other id, so a turn that ended after a client mutation took ownership
// cannot clear that mutation's identity out from under its settle path.
func (s *Session) releaseActiveTurn(turnID string) {
	if turnID == "" || s.clientMutations == nil {
		return
	}
	_ = s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.ActiveTurnID == turnID {
			snapshot.ActiveTurnID = ""
		}
		return nil
	})
}

func (s *Session) announceTurnStarted(turnID string) {
	if turnID == "" {
		return
	}
	s.mu.Lock()
	f := s.turnStartedFunc
	s.mu.Unlock()
	if f != nil {
		f(turnID)
	}
}
```

Add the field to the `Session` struct in `agent/session.go`, beside
`notifyFunc`:

```go
	turnStartedFunc func(turnID string)
```

- [ ] **Step 4: Call it from the one place a turn begins**

In `agent/session_lifecycle.go`, `processOneInput` sets `s.turnStartedAt`
under `s.mu` at :996 and unlocks at :1003. The store's `mutate` writes to
disk, so it must run **after** that unlock. Insert immediately after
`s.delegateDeliveryMu.Unlock()` (:1004):

```go
	// The turn's identity is settled before any event of it is emitted, so
	// the projector adopts it rather than minting one of its own, and the
	// id the daemon publishes is the id mutation preconditions accept.
	activeTurnID, reservedActiveTurn := s.beginActiveTurn()
	if reservedActiveTurn {
		defer s.releaseActiveTurn(activeTurnID)
	}
	s.announceTurnStarted(activeTurnID)
```

- [ ] **Step 5: Run the test and watch it pass**

Run: `cd agent && go test ./ -run TestAgentStartedTurnReservesAndAnnouncesADurableID -v`
Expected: PASS

- [ ] **Step 6: Add the adopt-don't-mint case**

Append to `agent/session_active_turn_test.go`:

```go
// TestClientMutationTurnKeepsItsReservedID pins the other half: a turn a
// client mutation already named keeps that name, and the settle path — not
// the turn's own release — is what clears it.
func TestClientMutationTurnKeepsItsReservedID(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed an already-reserved turn: %v", err)
	}
	reserved := s.clientMutations.snapshot().ActiveTurnID

	turnID, minted := s.beginActiveTurn()
	if turnID != reserved {
		t.Fatalf("beginActiveTurn adopted %q, want the already-reserved %q", turnID, reserved)
	}
	if minted {
		t.Fatal("beginActiveTurn claimed to mint an id that a client mutation had already reserved")
	}
}
```

Add `"primeradiant.com/serf/appwire"` to the test file's imports.

- [ ] **Step 7: Run both tests**

Run: `cd agent && go test ./ -run 'TestAgentStartedTurn|TestClientMutationTurnKeepsItsReservedID' -v`
Expected: PASS, both.

- [ ] **Step 8: Run the agent module suite**

Run: `cd agent && go test ./...`
Expected: PASS. `ActiveTurnID` is now set during agent-started turns, so
`AcceptClientMutationStart`'s `Conflict("turn is already active")` guard
(`session_client_mutation.go:206`) fires in cases where it previously did
not. That is the intended meaning of the field; if a test asserted the old
behaviour, update the test's expectation and say so in the commit.

- [ ] **Step 9: Commit**

```bash
git add agent/session_active_turn.go agent/session_active_turn_test.go agent/session.go agent/session_lifecycle.go
git commit -m "fix(agent): every turn owns a durable turn identity"
```

---

### Task 2: The daemon publishes what the session announces

**Files:**
- Modify: `cmd/serf/serve.go:97-98` (the `ServerLike` interface), `:990-1035`
  (`processMessage`), `:1173-1175` (delete `holdServeStateForAwaitingWake`),
  and the session wiring block near `:686`
- Modify: `server/server.go:679-718`
- Modify: `internal/appprojector/appwire_projection.go:1720-1727`
- Test: `cmd/serf/serve_state_test.go`, `server/appwire_server_test.go`

**Interfaces:**
- Consumes: `Session.SetTurnStartedFunc(func(turnID string))` from Task 1.
- Produces: `Server.ClearProcessing()` replaces `Server.SetProcessing(bool)`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/serf/serve_state_test.go`:

```go
// TestServeWiresTurnAnnouncementToTheProjector pins that the daemon publishes
// the identity the session announces, rather than minting one of its own. A
// server that still minted would answer with a turn_<n>, which no mutation
// precondition accepts.
func TestServeWiresTurnAnnouncementToTheProjector(t *testing.T) {
	srv := server.New(server.Config{})
	srv.SetProcessingTurn("turn_m7")
	if got := srv.ActiveTurnID(); got != "turn_m7" {
		t.Fatalf("ActiveTurnID = %q, want the announced turn_m7", got)
	}
	srv.ClearProcessing()
	if got := srv.ActiveTurnID(); got != "" {
		t.Fatalf("ActiveTurnID after ClearProcessing = %q, want empty", got)
	}
}
```

If `server.New`'s signature or an `ActiveTurnID()` accessor differs, match the
existing helpers in `server/appwire_server_test.go` rather than inventing new
ones; the assertion is what matters, not the constructor spelling.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/serf/ -run TestServeWiresTurnAnnouncementToTheProjector -v`
Expected: FAIL — `srv.ClearProcessing undefined`.

- [ ] **Step 3: Collapse the server's two processing entry points into one**

In `server/server.go`, replace `SetProcessing` and `setProcessingLocked` with:

```go
// ClearProcessing marks the session idle and releases an announced turn id
// the projector never consumed. Turn identity comes from the session
// (SetProcessingTurn); the server does not mint turn ids.
func (s *Server) ClearProcessing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processing = false
	if s.appProjector == nil || s.appReservedTurnID != "" {
		return
	}
	reservedTurnID := s.appProjector.ReservedTurnID()
	if reservedTurnID != "" && s.appActiveTurnID == reservedTurnID {
		s.appProjector.ReleaseReservedTurnID(reservedTurnID)
		s.appActiveTurnID = ""
	}
}
```

Leave `SetProcessingTurn` (`:691-699`) exactly as it is — it is now the only
way a turn becomes active.

- [ ] **Step 4: Delete the projector's live-turn minter**

In `internal/appprojector/appwire_projection.go`, delete `ReserveTurnID`
(:1720-1727). `ReserveStableTurnID`, `ReservedTurnID` and
`ReleaseReservedTurnID` all stay. Run `grep -rn "ReserveTurnID(" --include=
"*.go" .` and remove the callers you find; there should be exactly one
(`server/server.go`), plus any test that exercised it directly — delete those
tests, they cover a path that no longer exists.

- [ ] **Step 5: Rewire serve.go**

Beside the other session callbacks (near `:686`, where
`SetClientMutationStartWakeFunc` is wired), add:

```go
		// One announcement per turn, whatever started it. This is the only
		// thing that makes a turn active on the wire.
		s.SetTurnStartedFunc(func(turnID string) {
			srv.SetProcessingTurn(turnID)
			srv.SetState(string(agent.SessionProcessing))
		})
```

In `processMessage` (`:990`):
- Delete `srv.SetProcessing(true)` at `:1001` inside `nextTurnCtx`.
- Delete the whole `if !holdServeStateForAwaitingWake(...) { ... }` block at
  `:1013-1016`. The agent already refuses a wake while a question is pending
  (`session_lifecycle.go:604`), and it now announces exactly the turns it
  actually runs — so this branch was serve.go re-deriving the agent's own
  rule and guessing.
- In the `ProcessClientMutationStart` callback (`:1022-1027`), delete
  `srv.SetProcessingTurn(turnID)` and `srv.SetState(...)`; keep
  `srv.SetCancelFunc(cancelTurn)` and `setMutationRunner(cancelTurn, runnerDone)`.
  The session announces the same id from `processOneInput`.
- Change `srv.SetProcessing(false)` at `:1031` to `srv.ClearProcessing()`.

In the `ServerLike` interface (`:97-98`), replace `SetProcessing(bool)` with
`ClearProcessing()`.

- [ ] **Step 6: Delete the duplicated ask gate**

Delete `holdServeStateForAwaitingWake` (`:1173-1175`) and any test naming it.

- [ ] **Step 7: Run the tests**

Run: `go test ./cmd/serf/ ./server/ ./internal/appprojector/`
Expected: PASS. A test asserting that a generic processing flip mints a
`turn_<n>` is asserting the bug; delete it and note the deletion in the commit.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf/serve.go server/server.go internal/appprojector/appwire_projection.go cmd/serf/serve_state_test.go server/appwire_server_test.go
git commit -m "fix(daemon): publish the session's turn identity, stop minting a second one"
```

---

### Task 3: Live-stack regression — a goal turn takes steer and stop

**Files:**
- Modify: `cmd/serf-hub/e2e_turn_control_test.go`

**Interfaces:**
- Consumes: `fakellm.Server`, `startHubStack`, `awaitActiveTurn`,
  `awaitThread`, `clientRequest`, `newMutationID`, `communicateArgs` — all
  already in that file.

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/e2e_turn_control_test.go`:

```go
// TestE2E_TurnControlReachesAGoalContinuationTurn is the regression for the
// bug this file's sibling test could not see: a turn the agent starts itself.
// A goal continuation is the cheapest one to provoke, and it took the same
// path as every job, watch and delegate-attention wake — the daemon published
// a projector-minted turn_<n> while its mutation preconditions still held a
// turn_m<n>, so steer and stop were rejected with "turn is not active" and
// the composer showed nothing at all.
func TestE2E_TurnControlReachesAGoalContinuationTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const (
		openingPrompt = "SERF-E2E-GOAL-OPENING"
		steerText     = "SERF-E2E-GOAL-STEER"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "serf",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Serf.Ref
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	// Settle the opening turn so the goal's idle kick is what starts the next
	// one.
	opening, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")
	opening.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Serf.ActiveTurnID == ""
	})

	if _, err := clientRequest[appwire.GoalSetResponse](ctx, client, appwire.MethodGoalSet, appwire.GoalSetParams{
		Ref:       ref,
		Objective: "count to ten, one number per message",
	}); err != nil {
		t.Fatalf("goal/set: %v", err)
	}

	// The goal continuation turn: nobody sent a turn/start for it.
	goalRound, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the goal continuation's model request: %v", err)
	}
	goalTurn := awaitActiveTurn(ctx, t, client, ref, firstTurn)

	steerReceipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		ExpectedTurnID:   goalTurn,
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer against the goal continuation turn %q: %v", goalTurn, err)
	}
	if steerReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", steerReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	goalRound.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})
	afterSteer, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the model request after the tool round: %v", err)
	}
	if !afterSteer.Contains(steerText) {
		t.Fatalf("the steer never reached the goal continuation's loop; messages:\n%s",
			strings.Join(afterSteer.Texts(), "\n"))
	}

	interruptReceipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		ExpectedTurnID:   goalTurn,
	})
	if err != nil {
		t.Fatalf("turn/interrupt against the goal continuation turn %q: %v", goalTurn, err)
	}
	if interruptReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", interruptReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitThread(ctx, t, client, ref, "the interrupted goal turn to stop", func(thread appwire.Thread) bool {
		return thread.Serf.ActiveTurnID != goalTurn
	})
}
```

- [ ] **Step 2: Run it against the pre-fix daemon to confirm it catches the bug**

If Tasks 1 and 2 are already committed, verify the test's teeth by stashing
them into a temporary commit and checking out the parent — do **not** use
`git stash`. Otherwise run it now:

Run: `go test ./cmd/serf-hub/ -run TestE2E_TurnControlReachesAGoalContinuationTurn -v`
Expected before the fix: FAIL — `turn/steer against the goal continuation
turn "turn_2": ... turn is not active`.

- [ ] **Step 3: Run it against the fixed daemon**

Run: `go test ./cmd/serf-hub/ -run TestE2E_TurnControl -v`
Expected: PASS, both tests in the family.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/e2e_turn_control_test.go
git commit -m "test(e2e): pin steer and stop on an agent-started turn"
```

---

### Task 4: Full gates

**Files:** none — this task only runs gates and fixes what they catch.

- [ ] **Step 1: Lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 2: Build**

Run: `make build`
Expected: clean.

- [ ] **Step 3: Root suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Module suites**

Run: `cd agent && go test ./...` then the same for `auth`, `envvars`, `fuzz`,
`identifier`, `invariant`, `llm`.
Expected: PASS.

- [ ] **Step 5: Frontend gate**

Run: `make test-web`
Expected: PASS. Nothing in this change touches the frontend — it reads the
same `serf.activeTurnId` field — so a failure here is a signal that something
was mis-scoped.

- [ ] **Step 6: Verify against a real browser**

Run: `./scripts/e2e-webui-turn-controls.sh --hold 25 --rounds 60`, spawn the
session it prints, set a goal on it, then use Steer and Stop from the UI.
Confirm in the run directory's mutation journal
(`$run/home/.local/state/serf/projects/*/mutations/<SID>.json`) that both
mutations are `terminal`, not `rejected`. Stop the stack with `--stop`.

- [ ] **Step 7: Commit any gate fixes**

```bash
git add -u
git commit -m "fix: <what the gate caught>"
```

## Self-Review

**Spec coverage.** Diagnosis names three broken turn kinds — notification
wakes, goal continuations, and the post-interrupt queue-drain re-arm. Task 1
covers all three at once, because all three reach `processOneInput` and none
of them arrives with a reserved id. Task 2 removes the second minter so the
divergence cannot come back by another route. Task 3 pins one of the three
end-to-end; the other two share the same code path and the same unit test.

**Placeholders.** None: every step names exact files, exact commands, exact
expected output, and carries the real code.

**Type consistency.** `beginActiveTurn`, `releaseActiveTurn`,
`announceTurnStarted`, `SetTurnStartedFunc`, `turnStartedFunc`,
`ClearProcessing` are spelled identically everywhere they appear.
