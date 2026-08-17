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

	turnID, refusal := s.mintRunningTurnID()
	if !strings.HasPrefix(turnID, "turn_m") {
		t.Fatalf("minted %q, want the turn_m<n> family", turnID)
	}
	if refusal != turnNameMinted {
		t.Fatalf("refusal = %v for a successful mint, want turnNameMinted", refusal)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != turnID {
		t.Fatalf("durable ActiveTurnID = %q, want the minted %q", got, turnID)
	}

	s.releaseRunningTurnID(turnID)
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("durable ActiveTurnID after release = %q, want empty", got)
	}
}

// TestGoalContinuationTurnReleasesItsTurnID is the leak guard for the goal
// path, the sibling of TestNotificationTurnReleasesItsTurnID. The minted id
// gates every later turn: AcceptClientMutationStart refuses while ActiveTurnID
// is set and mintRunningTurnID refuses to name the next agent turn, so an id
// held past its turn wedges the session for the life of the process.
func TestGoalContinuationTurnReleasesItsTurnID(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)

	if _, err := s.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the goal turn ended, want it released", got)
	}
}

// TestReleaseRunningTurnIDLeavesAnotherOwnersTurnAlone pins the identity guard,
// which is load-bearing on a real interrupt path rather than defensive: an
// interrupted agent turn has its slot cleared by finalizeClientMutationInterrupt
// while the turn is still unwinding, and a turn/start accepted in that window
// writes its own id. An unconditional release would then wipe that client
// mutation's compare-and-commit target, and every control aimed at the user's
// turn would die with Conflict("turn is not active") -- the exact bug class this
// work exists to close.
func TestReleaseRunningTurnIDLeavesAnotherOwnersTurnAlone(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)

	mine, _ := s.mintRunningTurnID()
	if mine == "" {
		t.Fatal("mintRunningTurnID returned empty; the test needs a minted id to release")
	}
	// The interrupt clears the slot mid-unwind, and a client turn/start claims it.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("hand the slot to a client mutation: %v", err)
	}
	theirs := s.clientMutations.snapshot().ActiveTurnID

	s.releaseRunningTurnID(mine)

	if got := s.clientMutations.snapshot().ActiveTurnID; got != theirs {
		t.Fatalf("releasing %q left ActiveTurnID = %q, want the client mutation's %q untouched", mine, got, theirs)
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

	got, refusal := s.mintRunningTurnID()
	if got != "" {
		t.Fatalf("mintRunningTurnID = %q, want empty (refuse; the slot is owned)", got)
	}
	// The reason matters as much as the refusal: the notification stand-down
	// tells "someone holds the name" from "the store would not write" and waits
	// differently for each (kata ajg5).
	if refusal != turnNameHeld {
		t.Fatalf("refusal = %v for an owned slot, want turnNameHeld", refusal)
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
	got, refusal := s.mintRunningTurnID()
	if got != "" {
		t.Fatalf("mintRunningTurnID = %q under a pending interrupt, want empty", got)
	}
	// Its OWN refusal, not turnNameHeld: a fence over a turn the daemon never
	// named carries the empty name, and runningTurnNameHasOwner answers false
	// for that, so the stand-down needs to tell the two apart (kata ajg5).
	if refusal != turnNameFenced {
		t.Fatalf("refusal = %v under a pending interrupt, want turnNameFenced", refusal)
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
