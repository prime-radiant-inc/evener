package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

// Control is session-scoped: steer, queue and stop apply to whatever the
// session is running, not to a turn the client names. By the time a user's
// intent reaches the daemon the session may well be on a later turn, and that
// is fine -- the intent should apply as soon as possible rather than bounce.
//
// Each test here seeds a running turn the client never saw and issues control
// that does not name it.

func seedRunningTurn(t *testing.T, s *Session) string {
	t.Helper()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	var running string
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		running = snapshot.ActiveTurnID
		return nil
	}); err != nil {
		t.Fatalf("seed a running turn: %v", err)
	}
	return running
}

func TestSteerLandsIntoWhateverTurnIsRunning(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	seedRunningTurn(t, s)

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-session-scoped",
		Input:            []appwire.InputItem{{Type: "text", Text: "apply this now"}},
	}); err != nil {
		t.Fatalf("steer against the running turn: %v", err)
	}

	snapshot := s.clientMutations.snapshot()
	if len(snapshot.SteeringOrder) != 1 || snapshot.SteeringOrder[0] != "cm-steer-session-scoped" {
		t.Fatalf("steering order = %v, want the steer queued for delivery", snapshot.SteeringOrder)
	}
}

func TestQueueLandsWhateverTurnIsRunning(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	seedRunningTurn(t, s)

	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queue-session-scoped",
		Input:            []appwire.InputItem{{Type: "text", Text: "after this one"}},
	}); err != nil {
		t.Fatalf("queue against the running turn: %v", err)
	}

	snapshot := s.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("input queue = %v, want the message queued", snapshot.InputQueue)
	}
}

func TestInterruptStopsWhateverTurnIsRunning(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()

	running := runningStartTurn(t, s, "start-session-scoped-stop", "work on this")

	cancels := 0
	response, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-interrupt-session-scoped",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("interrupt the running turn: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("cancelled %d times, want 1", cancels)
	}
	if response.Receipt.TurnID != running {
		t.Fatalf("receipt turn = %q, want the running turn %q", response.Receipt.TurnID, running)
	}
}
