package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

// serveSession marks the session as one a daemon is actually draining, which
// is what makes its turns addressable by a client. ConsumeEventsLossless is
// deliberately the only writer of authoritativeConsumer (session.go:109-123),
// so tests register a real drain rather than setting the flag.
func serveSession(t *testing.T, s *Session) {
	t.Helper()
	s.ConsumeEventsLossless(func(events.SessionEvent) {}, func() {})
}

// TestGoalContinuationTurnCarriesItsNameOnTheOpeningEvent is the wiring test:
// minting a name is useless unless the event that opens the turn carries it,
// because that event is the only thing the AppWire projection reads. Without
// this, replacing processOneInput's mint with "" leaves every other test green
// while every mid-turn control silently breaks on goal turns.
func TestGoalContinuationTurnCarriesItsNameOnTheOpeningEvent(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	var mu sync.Mutex
	var continuations []events.GoalContinuationData
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if ev.Kind != events.EventGoalContinuation {
			return
		}
		data, ok := ev.Data.(events.GoalContinuationData)
		if !ok {
			return
		}
		mu.Lock()
		continuations = append(continuations, data)
		mu.Unlock()
	}, func() { close(drained) })

	if _, err := s.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(continuations) != 1 {
		t.Fatalf("EventGoalContinuation count = %d, want 1", len(continuations))
	}
	if !strings.HasPrefix(continuations[0].StableTurnID, "turn_m") {
		t.Fatalf("the goal continuation opened its turn with StableTurnID %q, want a turn_m<n> the daemon's preconditions accept",
			continuations[0].StableTurnID)
	}
}

// TestMintRunningTurnIDNamesAnAgentStartedTurn pins the contract every
// mid-turn control depends on: a turn no client mutation reserved an id for
// still gets one from the single mint site, and it is the durable value the
// preconditions compare against.
func TestMintRunningTurnIDNamesAnAgentStartedTurn(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)

	turnID := s.mintRunningTurnID()
	if !strings.HasPrefix(turnID, "turn_m") {
		t.Fatalf("minted %q, want the turn_m<n> family", turnID)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != turnID {
		t.Fatalf("durable ActiveTurnID = %q, want the minted %q", got, turnID)
	}

	s.releaseRunningTurnID(turnID)
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("durable ActiveTurnID after release = %q, want empty", got)
	}
}

// TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed pins the race an
// adversarial review of this change's first draft found: a client turn/start
// accepted between the serve loop dequeuing an agent wake and this mint would
// otherwise have its identity adopted by the agent turn — and a later Stop
// aimed at it would cancel the agent turn while marking the user's never-run
// message "interrupted".
func TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed an already-named turn: %v", err)
	}
	owned := s.clientMutations.snapshot().ActiveTurnID

	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q, want empty (refuse; the slot is owned)", got)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != owned {
		t.Fatalf("ActiveTurnID = %q, want the owner's %q left untouched", got, owned)
	}
}

// TestMintRunningTurnIDRefusesUnderAnInterruptFence matches every other
// durable entry point (session_client_mutation.go:198,407;
// session_client_mutation_queue.go:119,322,389,494), which all refuse while
// an interrupt is pending.
func TestMintRunningTurnIDRefusesUnderAnInterruptFence(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "cm-1", ExpectedTurnID: "turn_m1",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed an interrupt fence: %v", err)
	}
	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q under a pending interrupt, want empty", got)
	}
}

// TestMintRunningTurnIDSkipsUnservedSessions keeps in-process subagents off
// the durable store entirely. They share the parent's StateDir
// (subagents.go:581) and drive a turn per delegate wake; a turn no client can
// address needs no name and must not cost two fsyncs.
func TestMintRunningTurnIDSkipsUnservedSessions(t *testing.T) {
	s := newTestSessionForEnvctx(t) // no daemon draining this one
	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q for an unserved session, want empty", got)
	}
}

// TestLoadClearsARunningTurnNoPendingExecutionOwns is the crash guard: an
// ungraceful exit mid-turn must not brick every future turn/start. Without
// it a record-less ActiveTurnID survives restart with nothing left that can
// clear it, and AcceptClientMutationStart's "turn is already active" guard
// (session_client_mutation.go:206) rejects every later turn forever.
func TestLoadClearsARunningTurnNoPendingExecutionOwns(t *testing.T) {
	const sessionID = "sess-crash"
	dir := t.TempDir()
	fs := afero.NewMemMapFs()

	seeded := newEmptyClientMutationSnapshot(sessionID)
	seeded.NextTurnSequence = 7
	seeded.ActiveTurnID = appwire.ClientMutationTurnID(7)
	if _, err := saveClientMutationSnapshotFS(fs, dir, sessionID, seeded, clientMutationWriteEffect, clientMutationFaults{}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	loaded, err := loadClientMutationSnapshotFS(fs, dir, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID survived a restart as %q; a running turn is not durable state", loaded.ActiveTurnID)
	}
	if loaded.NextTurnSequence != 7 {
		t.Fatalf("NextTurnSequence = %d, want 7 — the counter must stay monotonic", loaded.NextTurnSequence)
	}
}

// TestLoadKeepsARunningTurnAPendingExecutionOwns is the other half: an id a
// pending execution still names is a turn/start the restore path reclaims and
// re-runs, so clearing it would lose the client's compare-and-commit target.
func TestLoadKeepsARunningTurnAPendingExecutionOwns(t *testing.T) {
	const sessionID = "sess-owned"
	dir := t.TempDir()
	fs := afero.NewMemMapFs()

	turnID := appwire.ClientMutationTurnID(3)
	seeded := newEmptyClientMutationSnapshot(sessionID)
	seeded.NextTurnSequence = 3
	seeded.ActiveTurnID = turnID
	seeded.PendingExecutions["cm-owner"] = appwire.PendingMutation{
		ClientMutationID: "cm-owner",
		Method:           clientMutationMethodStart,
		ExecutionState:   "accepted",
		TurnID:           turnID,
		ProjectionState:  appwire.MutationProjectionPending,
	}
	if _, err := saveClientMutationSnapshotFS(fs, dir, sessionID, seeded, clientMutationWriteEffect, clientMutationFaults{}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	loaded, err := loadClientMutationSnapshotFS(fs, dir, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveTurnID != turnID {
		t.Fatalf("ActiveTurnID = %q, want the pending execution's %q preserved", loaded.ActiveTurnID, turnID)
	}
}
