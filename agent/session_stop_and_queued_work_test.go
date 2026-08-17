package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

// A Stop and a queued message can collide, and the collision used to eat the
// message (katas e519 + nss1).
//
// The setup is ordinary: a message is queued behind a turn, and the user
// presses Stop. WireState reports processing for an idle session whenever work
// is pending -- a non-empty input queue counts -- so the interrupt is accepted
// with no turn running, writes a fence naming the empty turn, and cancels
// nothing. popQueueHead then claimed the queued mutation regardless of the
// fence, and completeClientMutationTurnWithState silently no-ops on a record
// that never reached "incorporated", so the message left the queue, never ran,
// and its turn id pinned ActiveTurnID for the life of the process -- which
// makes every later turn/start fail with "turn is already active".

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

// TestClaimedQueuedTurnIsReturnedWhenItNeverRan is the loss itself, at the seam
// where it happens. The drain loop claims the queue head, then processOneInput
// returns before incorporating anything -- a cancelled context is the ordinary
// way, and a Stop is what cancels it -- and the completion call that follows
// used to no-op on a record that never reached "incorporated".
//
// Recovery for exactly this state already existed, but only at startup: nothing
// put the claim back within the life of the process.
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
