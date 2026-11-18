package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-queued-under-ask", "never mind that, do this instead")

	s.mu.Lock()
	s.askPending = []askQuestion{{Header: "pick", Question: "which one?"}}
	s.mu.Unlock()

	if _, _, _, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil {
		t.Fatalf("ProcessPendingUserInput: %v", err)
	}

	if depth := s.queueDepth(); depth != 0 {
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
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

	_, ran, outcome, err := s.ProcessPendingUserInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProcessPendingUserInput on an empty queue: %v", err)
	}
	if ran {
		t.Fatal("ProcessPendingUserInput reported a turn on an empty queue")
	}
	if outcome != PendingInputNothingRunnable {
		t.Fatalf("outcome on an empty queue = %v, want nothing-runnable", outcome)
	}
	// The routine idle wake must never read as accepted work left standing, or
	// every wake the daemon coalesces would be reported and the condition the
	// report exists for would be noise (#6468).
	if outcome.RefusedRunnableInput() {
		t.Fatal("an empty wake is reported as runnable input a gate refused")
	}
}

// TestProcessPendingUserInputReportsTheTurnItClaims gives the daemon the turn id at
// the moment the claim becomes real, which is what lets it publish the running
// turn and wire cancellation to it -- the same contract
// ProcessClientMutationStart has.
func TestProcessPendingUserInputReportsTheTurnItClaims(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-queued-reported", "run me")

	var reported string
	_, ran, outcome, err := s.ProcessPendingUserInput(context.Background(), func(turnID string) {
		reported = turnID
	})
	if err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if reported == "" {
		t.Fatal("ProcessPendingUserInput ran a turn without reporting its id; the daemon cannot publish or cancel it")
	}
	if outcome != PendingInputClaimed {
		t.Fatalf("outcome for a wake that ran its work = %v, want claimed", outcome)
	}
	if outcome.RefusedRunnableInput() {
		t.Fatal("a wake that claimed its work is reported as refused runnable input")
	}
	if got := s.clientMutations.snapshot().Journal["cm-queued-reported"].StableTurnID; got != reported {
		t.Fatalf("reported turn %q, but the mutation's durable turn is %q", reported, got)
	}
}

func TestQueuedInputWakeReachesTheDaemon(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

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
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-under-ask",
		Input:            []appwire.InputItem{{Type: "text", Text: "take this into account"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}

	s.mu.Lock()
	s.askPending = []askQuestion{{Header: "pick", Question: "which one?"}}
	s.mu.Unlock()

	if _, ran, _, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput with steering pending: ran=%v err=%v", ran, err)
	}
	if s.hasPendingSteering() {
		t.Fatal("the steer is still pending; it was accepted and never delivered, which is quieter than the refusal it replaced")
	}
}

// TestSteeringWakeReachesTheDaemon pins the routing: steering's wake has to go
// to the user-input path, not the notification one the entry gate refuses.
func TestSteeringWakeReachesTheDaemon(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

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

// pendingInputJournalFixture records nAccepted settled client mutations in the
// store's journal, the way a long session accumulates them: the journal is
// never pruned, so every accepted mutation for the life of the session stays in
// it.
func pendingInputJournalFixture(t *testing.T, s *Session, nAccepted int) {
	t.Helper()
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		for i := range nAccepted {
			id := fmt.Sprintf("cm-bulk-%05d", i)
			payload := []byte(`{"input":[{"type":"text","text":"older message"}]}`)
			sum := sha256.Sum256(payload)
			snapshot.Journal[id] = clientMutationRecord{
				ClientMutationID: id,
				Method:           clientMutationMethodQueue,
				Actor:            schema.SessionActor{Kind: "human"},
				// Settled history: this is what makes the journal grow.
				OperationState:    clientMutationOperationTerminal,
				ExecutionState:    "incorporated",
				ProjectionState:   "reflected",
				Payload:           payload,
				PayloadHash:       hex.EncodeToString(sum[:]),
				Result:            []byte(`{"receipt":{"disposition":"applied"}}`),
				AttemptGeneration: 1,
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("bulk journal fixture: %v", err)
	}
}

// TestHasPendingUserInputToRunDoesNotCloneTheJournal keeps the yes/no question
// cheap as a session ages.
//
// Every accepted client mutation stays in the journal for the life of the
// session, and the predicate runs once per serve-loop iteration since #6462.
// Answering it by taking a snapshot deep-copies that whole growing journal,
// which is the one thing this predicate must not do: it reads a few scalars and
// the head of two lists.
//
// The ceiling is bytes, not allocation count, because the failure is a large
// copy rather than many small ones. A 20,000-record fixture whose payloads and
// results are ~40 bytes each is several megabytes if it is cloned and a few
// hundred bytes if it is not, so the ceiling sits far from both. This test must
// stay serial (no t.Parallel): runtime.MemStats describes the whole process, and
// a concurrent test in this package would perturb the measurement.
func TestHasPendingUserInputToRunDoesNotCloneTheJournal(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	pendingInputJournalFixture(t, s, 20_000)

	// The fixture holds no runnable work: an empty queue and no steering. The
	// cheap answer must agree with the cloning one it replaces.
	if s.hasPendingUserInputToRun() {
		t.Fatal("a journal of settled history with an empty queue reported work to run")
	}

	_ = s.hasPendingUserInputToRun() // warm the call once
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const iters = 20
	for range iters {
		_ = s.hasPendingUserInputToRun()
	}
	runtime.ReadMemStats(&after)
	bytesPerOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(iters)
	t.Logf("hasPendingUserInputToRun over 20,000 journal records allocates %.0f bytes per call", bytesPerOp)
	// A clone of this fixture is megabytes; the predicate needs no allocation at
	// all. The ceiling is generous so it pins the clone's absence, not an exact
	// allocation count that ordinary refactoring could move.
	const ceiling = 64 * 1024
	if bytesPerOp > ceiling {
		t.Fatalf("hasPendingUserInputToRun allocates %.0f bytes per call over 20,000 journal records, ceiling %d; the predicate must not clone the journal", bytesPerOp, ceiling)
	}
}

// TestRunnableClientMutationStartTurnIDDoesNotCloneTheJournal is the same guard
// for the serve loop's other probe. It runs beside the queued-input probe on
// every iteration, and against the same committed generation, so cloning there
// pays the journal's whole size a second time for an answer the generation
// already holds.
//
// Serial for the same reason as the test above: runtime.MemStats describes the
// whole process.
func TestRunnableClientMutationStartTurnIDDoesNotCloneTheJournal(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	pendingInputJournalFixture(t, s, 20_000)

	// The fixture holds no runnable start; the cheap answer must agree.
	if _, runnable := s.runnableClientMutationStartTurnID(); runnable {
		t.Fatal("a journal of settled history with no pending execution reported a runnable start")
	}

	_, _ = s.runnableClientMutationStartTurnID() // warm the call once
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const iters = 20
	for range iters {
		_, _ = s.runnableClientMutationStartTurnID()
	}
	runtime.ReadMemStats(&after)
	bytesPerOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(iters)
	t.Logf("runnableClientMutationStartTurnID over 20,000 journal records allocates %.0f bytes per call", bytesPerOp)
	const ceiling = 64 * 1024
	if bytesPerOp > ceiling {
		t.Fatalf("runnableClientMutationStartTurnID allocates %.0f bytes per call over 20,000 journal records, ceiling %d; the probe must not clone the journal", bytesPerOp, ceiling)
	}
}
