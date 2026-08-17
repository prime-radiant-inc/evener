package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

// A message the user queued has to run even when the agent is holding an
// unanswered question.
//
// The entry gate refuses every non-user kind while a question is pending, so
// that a delegate or a job finishing cannot silently resolve a question the
// user is still reading. Queued input is not that: it is the user speaking, and
// a user who types instead of answering has moved past the question. The drain
// loop already agrees -- selectDrainNextAction runs queued input ahead of
// everything else while awaiting -- so the only thing in the way is the kind
// the wake arrives as.

func TestQueuedInputRunsWhileAQuestionIsPending(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, s, "cm-queued-under-ask", "never mind that, do this instead")

	s.mu.Lock()
	s.askPending = []askQuestion{{Header: "pick", Question: "which one?"}}
	s.mu.Unlock()

	if _, _, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil {
		t.Fatalf("ProcessPendingUserInput: %v", err)
	}

	if depth := s.QueueDepth(); depth != 0 {
		t.Fatalf("queue depth = %d, want the message consumed; a message accepted and never run is lost more quietly than a refusal", depth)
	}
	// Jesse's ruling: the user's message supersedes the question. Leaving the
	// ask pending would show a question the daemon has already moved past.
	s.mu.Lock()
	pending := len(s.askPending)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("askPending = %d after the queued message ran, want it cleared", pending)
	}
}

// TestProcessPendingUserInputIsANoOpWithAnEmptyQueue keeps the wake cheap and safe:
// it fires unconditionally on acceptance and on replay, so it lands on an empty
// queue routinely and must not manufacture a turn out of nothing.
func TestProcessPendingUserInputIsANoOpWithAnEmptyQueue(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, ran, err := s.ProcessPendingUserInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProcessPendingUserInput on an empty queue: %v", err)
	}
	if ran {
		t.Fatal("ProcessPendingUserInput reported a turn on an empty queue")
	}
}

// TestProcessPendingUserInputReportsTheTurnItClaims gives the daemon the turn id at
// the moment the claim becomes real, which is what lets it publish the running
// turn and wire cancellation to it -- the same contract
// ProcessClientMutationStart has.
func TestProcessPendingUserInputReportsTheTurnItClaims(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, s, "cm-queued-reported", "run me")

	var reported string
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), func(turnID string) {
		reported = turnID
	}); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if reported == "" {
		t.Fatal("ProcessPendingUserInput ran a turn without reporting its id; the daemon cannot publish or cancel it")
	}
	if got := s.clientMutations.snapshot().Journal["cm-queued-reported"].StableTurnID; got != reported {
		t.Fatalf("reported turn %q, but the mutation's durable turn is %q", reported, got)
	}
}

func TestQueuedInputWakeReachesTheDaemon(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	wakes := 0
	s.SetPendingUserInputWakeFunc(func() { wakes++ })

	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queued-wake",
		Input:            []appwire.InputItem{{Type: "text", Text: "run me"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if wakes == 0 {
		t.Fatal("accepting a queued message did not wake the daemon's queued-input path")
	}
}

// TestSteeringIsDeliveredWhileAQuestionIsPending is the other half of the same
// defect. Steering has no turn of its own to run -- it is drained into whatever
// turn is starting -- so an empty user turn is what carries it, and that turn
// has to get past the same gate a queued message does.
func TestSteeringIsDeliveredWhileAQuestionIsPending(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-under-ask",
		Input:            []appwire.InputItem{{Type: "text", Text: "take this into account"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}

	s.mu.Lock()
	s.askPending = []askQuestion{{Header: "pick", Question: "which one?"}}
	s.mu.Unlock()

	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput with steering pending: ran=%v err=%v", ran, err)
	}
	if s.hasPendingSteering() {
		t.Fatal("the steer is still pending; it was accepted and never delivered, which is quieter than the refusal it replaced")
	}
}

// TestSteeringWakeReachesTheDaemon pins the routing: steering's wake has to go
// to the user-input path, not the notification one the entry gate refuses.
func TestSteeringWakeReachesTheDaemon(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	wakes := 0
	s.SetPendingUserInputWakeFunc(func() { wakes++ })

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-wake",
		Input:            []appwire.InputItem{{Type: "text", Text: "steer me"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if wakes == 0 {
		t.Fatal("accepting a steer did not wake the daemon's pending-user-input path")
	}
}
