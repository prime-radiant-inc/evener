package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// A stable delegate's watch notice is durable attention, so it never reaches the
// notification queue and peekNotifications cannot see it. The successor
// attention drive refuses a delegate that is already finalizing, and the
// parent's drive skips a child whose only undelivered work is watch attention.
// So while the delegate is parked in its finalization drain, the drain's own
// turn gate is the only thing that can present the notice -- without it the
// model waits for the owned shell to exit before receiving the reminder that
// lets it decide whether to stop the shell.
//
// The assertion is made while the shell is still held, so a turn carrying the
// notice can only come from the drain's own gate.
func TestDrainRunsANotificationTurnForAPendingStableWatchNotice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fixture := newOwnedJobDrainFixture(t)
		child := fixture.child.sess

		notice := watchSendDiagnosticNotification("watch:drain-notice", fixture.shellJobID, "watch send failed: delivery_id=drain-notice")
		child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
		if queued := child.peekNotifications(); queued != 0 {
			t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 0: the ingress did not take the durable rail", queued)
		}
		if !child.hasPendingStableWatchAttention() {
			t.Fatal("the enqueued notice does not read as pending watch attention")
		}

		// Let the bubble settle: with the shell still held, everything that can
		// run without a new wake has run.
		synctest.Wait()
		requests := fixture.adapter.Requests()
		if !requestsContain(requests, "delivery_id=drain-notice") {
			t.Fatalf("no provider request carried the watch notice while the owned shell was still running (%d request(s)): the drain left the reminder for the shell's exit", len(requests))
		}
		if !requestsContain(requests, "child shell complete") {
			// The shell is still held, so its completion cannot have arrived:
			// reaching here with the notice means the drain ran its own turn.
			t.Log("the notice was presented before the shell completed")
		}

		fixture.env.releaseJob()
		select {
		case <-fixture.runDone:
		case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
			t.Fatal("stable delegate did not finish after its owned shell exited")
		}
	})
}

// A leased turn is admitted for a watch notice already resolved into the
// session's transcript: the notice is in the history the request projects, and
// the turn consumes what it presents. Without an admission rule for durable
// watch attention the leased turn stands down and the row is presented by
// nobody, because only a leased generation may run it.
func TestStableWatchAttentionIsAdmittedForALeasedTurn(t *testing.T) {
	t.Parallel()
	child, _, _ := stableWatchSendChild(t)
	// newTestJM discards notifications; the production ingress is the session's
	// own enqueue, which is what routes a stable child's watch notice onto the
	// durable rail.
	child.jobManager.enqueue = child.enqueueJobNotificationAndNotify

	notice := watchSendDiagnosticNotification("watch:accept-notice", "job:accept-notice", "watch send failed: delivery_id=accept-notice")
	child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 0: the ingress did not take the durable rail", queued)
	}
	if !child.hasPendingStableWatchAttention() {
		t.Fatal("the enqueued notice does not read as pending watch attention")
	}

	if child.acceptNotificationInput(context.Background(), "unleased-turn") {
		t.Fatal("an unleased notification drive was admitted for a watch notice it cannot consume")
	}
	leased := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: child.owningDelegateID, generation: 1})
	if !child.acceptNotificationInput(leased, "leased-turn") {
		t.Fatal("the delegate's own leased turn was not admitted for its watch notice")
	}
}
