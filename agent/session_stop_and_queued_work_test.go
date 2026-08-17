package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
)

// A Stop and a message queued behind it collide in an ordinary way, and the
// message has to survive it.
//
// WireState reports processing for an idle session whenever work is pending --
// a non-empty input queue counts -- so a Stop is accepted with no turn running,
// writes a fence naming the empty turn, and cancels nothing. Three things have
// to hold through that: the queue head is not claimed into a turn that is
// already dying, a claim that never incorporated its input is returned rather
// than settled, and ActiveTurnID is left empty. A turn id stranded there makes
// every later turn/start fail with "turn is already active" for the life of the
// process.

// queueOneMutation durably queues a client mutation, the way a client's
// turn/queue does, and returns its id.
func queueOneMutation(t *testing.T, sess *Session, clientMutationID, text string) {
	t.Helper()
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: clientMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: text}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue(%q): %v", clientMutationID, err)
	}
}

// TestClaimedQueuedTurnIsReturnedWhenItNeverRan covers the seam where a queued
// message can be lost. The drain loop claims the queue head, then
// processOneInput returns before incorporating anything -- a cancelled context
// is the ordinary way, and a Stop is what cancels it. The completion call that
// follows must return the claim, because the entry is already out of the input
// queue and the only other recovery for this state runs at startup.
func TestClaimedQueuedTurnIsReturnedWhenItNeverRan(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queue-behind-stop", "please still run me")

	claimed := sess.popQueueHead()
	if claimed.ClientMutationID != "queue-behind-stop" {
		t.Fatalf("popQueueHead claimed %#v, want the queued message", claimed)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got == "" {
		t.Fatal("the claim did not take an ActiveTurnID; this test is not in the state it means to be")
	}

	// The turn never incorporated its input. This is the call the drain loop
	// makes next, unconditionally, at session_lifecycle.go:653.
	if err := sess.completeClientMutationTurnWithState("queue-behind-stop", "terminal"); err != nil {
		t.Fatalf("completeClientMutationTurnWithState: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != "queue-behind-stop" {
		t.Fatalf("the message did not return to the queue: %#v", snapshot.InputQueue)
	}
	if record := snapshot.Journal["queue-behind-stop"]; record.OperationState == clientMutationOperationTerminal {
		t.Fatalf("a message that never ran was settled as terminal: %#v", record)
	}

	// And the stop must not strand a turn id. A non-empty ActiveTurnID naming a
	// turn that will never run makes AcceptClientMutationStart refuse every
	// later turn with "turn is already active", for the life of the process.
	if got := snapshot.ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID after the unrun turn = %q, want empty; a stranded id wedges every later turn/start", got)
	}
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "next turn"}},
	}); err != nil {
		t.Fatalf("turn/start after the unrun turn: %v", err)
	}

	// Durably back is not enough: QueueDepth, QueuePreview and WireState all
	// read the process-local queue, and wakeForPendingQueuedInput gates on that
	// same depth. A message restored only in the snapshot is invisible in the
	// queue strip and cannot wake the session that owes it a turn.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the return = %d, want 1; the runtime queue is stale", got)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "please still run me" {
		t.Fatalf("QueuePreview after the return = %#v, want the returned message", preview)
	}
}

// TestStopWakesTheSessionForWorkItLeftQueued closes the strand the fence guard
// would otherwise create. popQueueHead declines while an interrupt is pending,
// and finalize then clears the fence without scheduling anything -- so a Stop
// with a message queued behind it would leave the session reporting itself as
// working with a queue nobody drains.
func TestStopWakesTheSessionForWorkItLeftQueued(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-through-stop", "run me after the stop")

	var mu sync.Mutex
	notifies := 0
	sess.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})
	mu.Lock()
	before := notifies
	mu.Unlock()

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued-work",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == before {
		t.Fatal("the stop left work queued and woke nothing; the session reports itself active with a queue nobody drains")
	}
}

// TestStopIsHonestAboutAQueuedMessage covers the state the user is actually
// looking at: no turn is running, but a queued message keeps WireState
// reporting processing, so Stop is on screen. Accepting the Stop is right --
// refusing while the UI says "working" is the failure the session-scoped rule
// exists to prevent -- but it must not cost the queued message.
func TestStopIsHonestAboutAQueuedMessage(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queue-behind-stop", "please still run me")

	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID before the stop = %q, want empty (no turn running)", got)
	}
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with a queued message = %q, want %q", got, SessionProcessing)
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-with-queued-work",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("the stop consumed the queued message: queue=%#v", snapshot.InputQueue)
	}
	if snapshot.InterruptFence != nil {
		t.Fatalf("the stop left a fence behind: %#v", snapshot.InterruptFence)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID after the stop = %q, want empty", snapshot.ActiveTurnID)
	}
}

// TestQueueClaimRespectsAPendingInterruptFence is the narrower invariant the
// loss above depends on. AcceptClientMutationStart and claimClientMutationStart
// both refuse while an interrupt is pending; popQueueHead did not, so a Stop
// that had been accepted but not yet finalized could still have the queue head
// claimed out from under it.
func TestQueueClaimRespectsAPendingInterruptFence(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-under-fence", "do not claim me yet")
	armTestInterruptFence(t, sess, "interrupt-in-flight")

	if claimed := sess.popQueueHead(); claimed.ClientMutationID != "" {
		t.Fatalf("popQueueHead claimed %q while an interrupt was pending", claimed.ClientMutationID)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("input queue depth = %d, want the message still queued", len(snapshot.InputQueue))
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("a refused claim still set ActiveTurnID = %q", snapshot.ActiveTurnID)
	}
}
