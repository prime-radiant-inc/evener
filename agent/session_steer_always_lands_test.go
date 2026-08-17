package agent

import (
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestRestoredSteeringWakesWhenTheDaemonAttaches closes the crash case the
// retry path cannot reach on its own.
//
// If the process dies after a steer's durable commit and the client never
// retries, restoreDurableClientMutationQueues rebuilds the steering queue at
// startup -- and then nothing asks the session to run. The steer waits for
// whatever the user happens to do next.
//
// The case has two halves and this test covers both, because either one alone
// strands the steer: restore must reconstitute the durable steering into the
// runtime queue, and registering the wake callback must then fire on it.
// SetNotifyFunc is the hook for the second half -- it is the moment the wake
// callback provably exists, and it already fires for every other kind of
// pending work (job notifications, delegate deliveries, root and stable
// attention, watch settlement retries). Steering was simply missing from that
// list.
//
// The crash is real, not simulated: the steer is committed through the
// production durable path, the session is closed, and a fresh one is rebuilt
// from the same state dir through RestoreSessionFromMetaWithConfig -- the same
// function `serf serve --resume` calls.
func TestRestoredSteeringWakesWhenTheDaemonAttaches(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-crashed",
		Input:            []appwire.InputItem{{Type: "text", Text: "restored steer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	// The process dies here. The steer is durably committed, no turn ever ran
	// it, and the client never retries.
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	serveSession(t, restored)

	if !restored.hasPendingSteering() {
		t.Fatal("restore did not put the durably-committed steer back in the runtime steering queue; nothing downstream can deliver a steer the queue does not hold")
	}

	var mu sync.Mutex
	notifies := 0
	restored.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a restored session with steering already queued did not wake when the daemon attached; a steer that survived a crash waits for unrelated input")
	}
}

// TestSteerLandsWhenItsTurnAlreadyEnded pins the rule that a steer always
// lands.
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

// TestSteerWakesEvenWhileATurnIsRunning covers the race that killed the
// obvious optimisation. Gating the wake on "no turn is running" looks free --
// the round loop drains steering between rounds, so why kick? -- but a turn can
// pass its FINAL steering drain and still own the turn id. A steer arriving in
// that window is skipped by the gate and then never looked at again, because
// the turn releases its id without rechecking steering.
//
// An unneeded kick is cheap: the wake finds nothing and no-ops. A missed one
// loses what the user typed.
func TestSteerWakesEvenWhileATurnIsRunning(t *testing.T) {
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

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-running",
		Input:            []appwire.InputItem{{Type: "text", Text: "mid-turn steer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer against the running turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a steer accepted during a turn woke nothing; if that turn had already passed its final drain, the steer is never looked at again")
	}
}

// TestSteerRetryStillProvokesDelivery is the crash case. The store commits, the
// process dies before the wake, and the client retries with the same
// clientMutationId. The store replays idempotently and returns early -- which
// is correct for the record and useless for the user, because the steer is
// still sitting undelivered. Replay is idempotent in the store; the wake is
// what makes it idempotent in effect.
func TestSteerRetryStillProvokesDelivery(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	params := appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-retry",
		Input:            []appwire.InputItem{{Type: "text", Text: "retried steer"}},
	}
	if _, err := s.AcceptClientMutationSteer(params); err != nil {
		t.Fatalf("first accept: %v", err)
	}

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(params); err != nil {
		t.Fatalf("retry of the same steer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("the retry replayed the record and woke nothing; a steer stranded by a crash stays stranded no matter how often the client retries")
	}
}
