package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

// System steering must not provoke a turn of its own.
//
// A session injects daemon-authored steering during startup -- the current-task
// reminder, hook context, a transcript pointer -- and that steering is meant to
// ride whatever turn happens next, not to manufacture one. A turn started for
// it takes the durable ActiveTurnID, and the first turn/start the user sends is
// then refused with "turn is already active" while the session visibly runs.
//
// Only the user's own steering justifies provoking a turn: that is input they
// gave that nothing else will deliver.

func TestSystemSteeringDoesNotProvokeATurn(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	s.SteerKind("<SYSTEM-REMINDER>startup context</SYSTEM-REMINDER>", events.SteeringKindCurrentTask)
	if !s.hasPendingSteering() {
		t.Fatal("system steering did not queue; this test is not in the state it means to be")
	}

	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil {
		t.Fatalf("ProcessPendingUserInput: %v", err)
	} else if ran {
		t.Fatal("system steering provoked a user turn of its own; it is meant to ride the next real turn")
	}

	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after startup steering, want empty", got)
	}
	// The symptom this protects: the user's first turn/start is refused because
	// something else already took the turn.
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-first-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user's opening message"}},
	}); err != nil {
		t.Fatalf("the user's first turn/start was refused: %v", err)
	}
}

// TestSystemSteeringDoesNotWakeThePendingUserInputPath is the same rule one
// level up: the wake itself must not fire, or the daemon submits work the
// session then declines to do, once per registration.
func TestSystemSteeringDoesNotWakeThePendingUserInputPath(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	s.SteerKind("<SYSTEM-REMINDER>startup context</SYSTEM-REMINDER>", events.SteeringKindCurrentTask)

	wakes := 0
	s.SetPendingUserInputWakeFunc(func() { wakes++ })
	if wakes != 0 {
		t.Fatalf("registering the wake fired %d times for system steering, want 0", wakes)
	}
}

// TestUserSteeringStillProvokesATurn keeps the fix from swallowing the case it
// was built for: a steer the user sent, with no turn to land in, still runs.
func TestUserSteeringStillProvokesATurn(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-user-steer",
		Input:            []appwire.InputItem{{Type: "text", Text: "take this into account"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}

	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("user steering did not run: ran=%v err=%v", ran, err)
	}
	if s.hasPendingSteering() {
		t.Fatal("the user's steer is still pending after the turn that should have carried it")
	}
}
