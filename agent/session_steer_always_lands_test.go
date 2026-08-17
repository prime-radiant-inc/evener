package agent

import (
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestSteerLandsWhenItsTurnAlreadyEnded is Jesse's 2026-08-16 ruling: a steer
// always lands.
//
// The reachable case is a race, not an exotic one. You press Steer while the
// agent is working; between the click and the request arriving, the turn ends.
// The daemon then compares your expectedTurnId against an ActiveTurnID that is
// now empty and rejects with Conflict("turn is not active") -- and per kata
// 2f41 you are told nothing. The thing you typed is simply gone.
//
// A steer typed while the agent works means "take this into account", and the
// user cannot see turn boundaries, so the daemon accepts it into the steering
// queue instead. The steering queue is the existing delivery path: a wake
// proceeds on pending steering alone, with no job notifications involved.
func TestSteerLandsWhenItsTurnAlreadyEnded(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	// No turn is running: the one the client aimed at has ended.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-1",
		ExpectedTurnID:   "turn_m1",
		Input:            []appwire.InputItem{{Type: "text", Text: "also check the tests"}},
	}); err != nil {
		t.Fatalf("a steer whose turn ended under it was refused: %v", err)
	}

	if !s.hasPendingSteering() {
		t.Fatal("the steer was accepted but is not queued for delivery; it would be silently lost")
	}
}

// TestSteerWakesAnIdleSessionToDeliverItself is the other half of the ruling.
// Accepting the steer is not enough: if no turn ever runs, the steering queue
// is never drained and the message is lost more quietly than a rejection would
// have been. The daemon must provoke a turn of its own.
func TestSteerWakesAnIdleSessionToDeliverItself(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-2",
		ExpectedTurnID:   "turn_m1",
		Input:            []appwire.InputItem{{Type: "text", Text: "also check the tests"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a steer accepted with no turn running woke nothing; the steering queue has no drain, so the message waits forever")
	}
}

// TestSteerStillRefusesADifferentRunningTurn keeps the relaxation narrow. When
// a turn IS running and the client names a different one, its view is stale in
// a way that matters -- it is steering something it is not looking at -- and
// the compare-and-commit precondition still applies.
func TestSteerStillRefusesADifferentRunningTurn(t *testing.T) {
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
		t.Fatalf("seed a running turn: %v", err)
	}

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-3",
		ExpectedTurnID:   "turn_m99",
		Input:            []appwire.InputItem{{Type: "text", Text: "steer the wrong turn"}},
	}); err == nil {
		t.Fatal("a steer naming a turn that is not the running one was accepted; the precondition still has a job while a turn is in flight")
	}
}
