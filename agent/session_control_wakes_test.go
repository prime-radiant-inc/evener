package agent

import (
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
)

// Accepting a control mutation is only half of landing it. Control is
// session-scoped now, so queue, drain and promote are accepted against an idle
// session -- and an idle session is parked awaiting input, so nothing runs what
// they just accepted. The receipt says Applied and the message sits there
// (kata r6sk).
//
// Steer already learned this the hard way. Its wake is deliberately
// unconditional rather than gated on "is a turn running", because that gate
// loses a race it cannot win: a turn can pass its final drain and still own the
// turn id, so a mutation arriving in that window would be skipped and never
// looked at again. These follow the same shape, including the replay branch --
// a retry of something accepted but never delivered has to provoke delivery
// too, because replay is idempotent in the store and useless to the user.

// countingWake installs a notify func and returns a reader for its count.
func countingWake(t *testing.T, s *Session) func() int {
	t.Helper()
	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return notifies
	}
}

func TestQueueWakesAnIdleSessionToRunIt(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	notifies := countingWake(t, s)

	params := appwire.TurnQueueParams{
		ClientMutationID: "cm-queue-wake",
		Input:            []appwire.InputItem{{Type: "text", Text: "run me"}},
	}
	if _, err := s.AcceptClientMutationQueue(params); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if notifies() == 0 {
		t.Fatal("a queue accepted with no turn running woke nothing; the session stays parked and the message never runs")
	}

	// The retry path is the stickier half: it returns the replayed receipt
	// before reaching any delivery, so a client retrying gets Applied again and
	// still provokes nothing.
	before := notifies()
	if _, err := s.AcceptClientMutationQueue(params); err != nil {
		t.Fatalf("replayed AcceptClientMutationQueue: %v", err)
	}
	if notifies() == before {
		t.Fatal("the replayed queue woke nothing; a message stranded by a crash stays stranded however often the client retries")
	}
}

func TestDrainWakesAnIdleSessionToDeliverIt(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queued-for-drain",
		Input:            []appwire.InputItem{{Type: "text", Text: "drain me"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}

	notifies := countingWake(t, s)
	// Registering the callback wakes on its own when input is already queued,
	// so measure the increase this drain causes, not the total.
	before := notifies()
	revision := s.clientMutations.snapshot().QueueRevision
	if _, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "cm-drain-wake",
		ExpectedQueueRevision: revision,
	}); err != nil {
		t.Fatalf("AcceptClientMutationDrainAsSteer: %v", err)
	}
	if notifies() == before {
		t.Fatal("a drain accepted with no turn running woke nothing; the steering it produced has no drain")
	}
}

func TestPromoteWakesAnIdleSessionToDeliverIt(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queued-for-promote",
		Input:            []appwire.InputItem{{Type: "text", Text: "promote me"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	entryID := s.clientMutations.snapshot().InputQueue[0].ID

	notifies := countingWake(t, s)
	// Same as the drain case: registration wakes on the already-queued message.
	before := notifies()
	if _, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
		ClientMutationID: "cm-promote-wake",
		Index:            0,
		ExpectedEntryID:  entryID,
	}); err != nil {
		t.Fatalf("AcceptClientMutationPromoteQueuedAsSteer: %v", err)
	}
	if notifies() == before {
		t.Fatal("a promote accepted with no turn running woke nothing; the steering it produced has no drain")
	}
}

// TestRestoredQueuedInputWakesWhenTheDaemonAttaches is the crash case, and the
// same one TestRestoredSteeringWakesWhenTheDaemonAttaches covers for steering:
// the process dies after a queue's durable commit, restore rebuilds the input
// queue, and nothing asks the session to run. Registering the notify callback
// is the moment a wake can provably be delivered.
func TestRestoredQueuedInputWakesWhenTheDaemonAttaches(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queued-before-crash",
		Input:            []appwire.InputItem{{Type: "text", Text: "queued before the crash"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if s.QueueDepth() == 0 {
		t.Fatal("the queue is empty; this test is not in the state it means to be")
	}

	notifies := countingWake(t, s)
	if notifies() == 0 {
		t.Fatal("a session with input already queued did not wake when the daemon attached; the message waits for unrelated input")
	}
}
