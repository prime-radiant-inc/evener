package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// The interrupted-turn recovery in session_lifecycle.go is the one place that
// takes the queue head away. Whether it may take it is decided entirely by this
// turn's context and error -- interruptDrainConfig reads no queue state at all
// -- so the decision must be made BEFORE the claim.
//
// Claiming first and asking afterwards costs one durable commit to remove the
// head and a second to put it back, and between them the durable queue is
// observably missing an entry that is about to return. turn/promoteQueuedAsSteer
// is a compare-and-commit against exactly that state: a promote naming index 0
// lands on the SECOND queued message, which no client ever saw at index 0
// (kata 9f5x, found while root-causing n1zs).
//
// The oracle is the queue revision, because the revision is what makes a
// transient state observable in the first place. Every durable queue change
// bumps it and publishes the result. Zero bumps means no state existed for
// anything to sample.

// interruptClaimSession builds a session whose queue is durable, and returns it
// alongside a collector of every queue state it publishes. The collector is the
// client's-eye view: reflectDurableInputQueue emits one QUEUE_CHANGED per
// durable commit, so a phantom state that reaches a client reaches this slice.
//
// Collecting closes the session first and joins the reader, so the result is
// every event the session ever published rather than however many the reader
// happened to have picked up -- a test that raced its own event stream could
// only ever be green by accident.
func interruptClaimSession(t *testing.T) (*Session, func() [][]string) {
	t.Helper()
	sess := newQueuePersistTestSession(t, t.TempDir())

	var mu sync.Mutex
	var published [][]string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			data, ok := ev.Data.(events.QueueChangedData)
			if !ok {
				continue
			}
			mu.Lock()
			published = append(published, append([]string(nil), data.Preview...))
			mu.Unlock()
		}
	}()

	var closeOnce sync.Once
	closeSession := func() {
		closeOnce.Do(func() {
			sess.Close()
			<-done
		})
	}
	t.Cleanup(closeSession)
	return sess, func() [][]string {
		closeSession()
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), published...)
	}
}

// interruptedTurnContext returns the context an interrupted turn runs under,
// wired the way cmd/serf serve wires it (a marked turn context with a
// next-turn factory). rootLive=false cancels the root as well, which is what
// makes the drain unavailable: interruptDrainConfig refuses a root that is
// already done, and that refusal is the branch that used to restore the head.
func interruptedTurnContext(t *testing.T, rootLive bool) context.Context {
	t.Helper()
	root, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	turnCtx, cancelTurn := context.WithCancel(root)
	t.Cleanup(cancelTurn)
	var nextCtx func(context.Context) (context.Context, context.CancelFunc)
	nextCtx = func(parent context.Context) (context.Context, context.CancelFunc) {
		next, cancel := context.WithCancel(parent)
		t.Cleanup(cancel)
		return WithQueuedInputDrainOnInterruptHandler(next, root, nextCtx), cancel
	}
	marked := WithQueuedInputDrainOnInterruptHandler(turnCtx, root, nextCtx)
	cancelTurn()
	if !rootLive {
		cancelRoot()
	}
	return marked
}

// TestUndrainableInterruptMakesNoDurableQueueTransition is the kata's stop
// condition stated directly: an interrupted turn that cannot run what it would
// claim must leave the durable queue untouched, so no state exists for a
// racing compare-and-commit to sample.
func TestUndrainableInterruptMakesNoDurableQueueTransition(t *testing.T) {
	sess, publishedQueues := interruptClaimSession(t)

	queueOneMutation(t, sess, "queue-alpha", "alpha")
	queueOneMutation(t, sess, "queue-bravo", "bravo")

	before, _ := sess.ClientMutationProjection()
	if before.Depth != 2 {
		t.Fatalf("durable queue depth before the interrupt = %d, want 2; this test is not in the state it means to be", before.Depth)
	}

	// The drain is unavailable (dead root), so the recovery reaches the branch
	// that used to pop the head and push it back.
	_, _ = sess.ProcessInput(interruptedTurnContext(t, false), "interrupted turn", nil)

	after, _ := sess.ClientMutationProjection()
	if len(after.IDs) != 2 || after.IDs[0] != before.IDs[0] || after.IDs[1] != before.IDs[1] {
		t.Fatalf("durable queue after the interrupt = %#v, want %#v unchanged", after.IDs, before.IDs)
	}
	if after.Revision != before.Revision {
		t.Errorf("the queue revision moved from %d to %d across an interrupt that consumed nothing: the recovery claimed the head durably and then put it back, and every state between those two commits was a queue no client ever saw (kata 9f5x)",
			before.Revision, after.Revision)
	}

	// The revision assertion proves no phantom state was durably committed;
	// this proves no client was told one existed. "bravo alone" is the phantom
	// by name: it is the queue with the head claimed and not yet returned, and
	// it is the state a turn/promoteQueuedAsSteer naming index 0 would land on.
	for _, preview := range publishedQueues() {
		if len(preview) == 1 && preview[0] == "bravo" {
			t.Errorf("the queue state %#v reached clients during the interrupt: a promote naming index 0 against it steers \"bravo\", which no client ever saw at index 0", preview)
		}
	}
}

// TestDrainableInterruptClaimsQueueHeadInOneDurableTransition is the other half
// of the contract, and guards the obvious wrong fix: a recovery that closes the
// window by never draining at all. A live root means the head runs, and it must
// cost exactly one durable commit.
func TestDrainableInterruptClaimsQueueHeadInOneDurableTransition(t *testing.T) {
	sess, _ := interruptClaimSession(t)

	queueOneMutation(t, sess, "queue-alpha", "alpha")
	queueOneMutation(t, sess, "queue-bravo", "bravo")

	before, _ := sess.ClientMutationProjection()
	if before.Depth != 2 {
		t.Fatalf("durable queue depth before the interrupt = %d, want 2", before.Depth)
	}

	_, _ = sess.ProcessInput(interruptedTurnContext(t, true), "interrupted turn", nil)

	after, _ := sess.ClientMutationProjection()
	if len(after.IDs) != 1 || after.IDs[0] != before.IDs[1] {
		t.Fatalf("durable queue after the drain = %#v, want only %q: the interrupted turn must run the head it claimed", after.IDs, before.IDs[1])
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("the drain cost %d durable queue transitions (revision %d -> %d), want exactly 1: claiming the head is one commit, and any second commit is a state a racing promote can sample",
			after.Revision-before.Revision, before.Revision, after.Revision)
	}
}
