package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// gatedCoordinator spins up a backgrounded coordinator delegate, drives it to a
// terminal-but-retained state, and returns the session, the coordinator's
// delegate handle, and its retained subagent. The adapter's drive turn (call 2)
// completes immediately because release is pre-closed.
func gatedCoordinator(t *testing.T) (*Session, delegateResult, *subagent, *driveBlockingSecondTurnAdapter) {
	t.Helper()
	release := make(chan struct{})
	close(release)
	adapter := &driveBlockingSecondTurnAdapter{
		name:            "openai",
		release:         release,
		secondTurnStart: make(chan struct{}),
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	coord := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "coordinate",
		Background: true,
	})
	if coord.Err != nil {
		t.Fatalf("createDelegate (coordinator): %v", coord.Err)
	}
	waitForShellDone(t, sess.jobManager, coord.JobID)

	_, coordID, err := decodeRef(coord.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	sub := sess.subagents.get(coordID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("coordinator subagent %q not retained", coordID)
	}
	return sess, coord, sub, adapter
}

// TestDisposeGateBlocksDriveAndClearRestores proves the wake-edge drive refusal:
// while sub.disposeGated is set, a concurrent notification cannot launch a drive
// turn (spec §P1 step 4, test 10); clearing the gate restores drives.
func TestDisposeGateBlocksDriveAndClearRestores(t *testing.T) {
	t.Parallel()
	sess, _, sub, adapter := gatedCoordinator(t)

	// Queue work so the ONLY thing keeping the drive from launching is the gate.
	enqueueCompletedDelegateNotification(t, sub.sess, "worker-1")

	if !sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate on a quiescent child returned false")
	}

	// A notify firing while gated must NOT launch a drive.
	if sess.driveSubagentNotificationTurn(sub) {
		t.Fatal("driveSubagentNotificationTurn launched a drive on a dispose-gated child")
	}
	if n := len(adapter.Requests()); n != 1 {
		t.Fatalf("gated drive launched a model call: requests=%d, want 1 (initial turn only)", n)
	}

	// Clearing the gate restores drives: the queued work now launches a drive turn.
	sub.clearDisposeGate()
	if !sess.driveSubagentNotificationTurn(sub) {
		t.Fatal("driveSubagentNotificationTurn refused after the gate was cleared")
	}
	waitForCondition(t, 5*time.Second, "drive goroutine to finish", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return !sub.driving
	})
	if n := len(adapter.Requests()); n != 2 {
		t.Fatalf("post-clear drive did not run: requests=%d, want 2", n)
	}
}

// TestTrySetDisposeGateRefusesActiveChild proves trySetDisposeGate declines a
// child that is running or driving — a raced drive/resume wins the gate.
func TestTrySetDisposeGateRefusesActiveChild(t *testing.T) {
	t.Parallel()
	_, _, sub, _ := gatedCoordinator(t)

	sub.mu.Lock()
	sub.running = true
	sub.mu.Unlock()
	if sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate armed the gate on a running child")
	}
	sub.mu.Lock()
	if sub.disposeGated {
		t.Fatal("failed trySetDisposeGate left disposeGated set")
	}
	sub.running = false
	sub.driving = true
	sub.mu.Unlock()
	if sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate armed the gate on a driving child")
	}
	sub.mu.Lock()
	sub.driving = false
	sub.mu.Unlock()
	if !sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate refused a now-quiescent child")
	}
}

// TestWatchOriginatedSendToGatedDelegateStaysBusy proves finding N2: a
// watch-originated send to a dispose-gated retained delegate is refused as
// watchSendBusy (retryable, frame kept), NOT the default watchSendHardFailure
// that would permanently drop the watch frame.
func TestWatchOriginatedSendToGatedDelegateStaysBusy(t *testing.T) {
	t.Parallel()
	sess, coord, sub, _ := gatedCoordinator(t)

	if !sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate on a quiescent child returned false")
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        coord.DelegateID,
		Message:       "watch frame",
		Background:    true,
		BackgroundSet: true,
		FromWatch:     true,
	})
	if res.Err != nil {
		t.Fatalf("gated watch send returned error (would be classified hard failure): %v", res.Err)
	}
	if !res.WatchSendDeliveryClassSet || res.WatchSendDeliveryClass != watchSendBusy {
		t.Fatalf("gated watch send classification = %+v, want retryable watchSendBusy (frame must survive, not dropped)", res)
	}
	if classifyWatchSendDelivery(res) == watchSendHardFailure {
		t.Fatal("gated watch send classified as hard failure; the frame would be permanently dropped")
	}
}

// TestPlainSendToGatedDelegateRefusesBusy proves a plain (non-watch) send to a
// dispose-gated retained delegate is refused with a busy/retry error.
func TestPlainSendToGatedDelegateRefusesBusy(t *testing.T) {
	t.Parallel()
	sess, coord, sub, _ := gatedCoordinator(t)

	if !sub.trySetDisposeGate() {
		t.Fatal("trySetDisposeGate on a quiescent child returned false")
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        coord.DelegateID,
		Message:       "please run",
		Background:    true,
		BackgroundSet: true,
	})
	if res.Err == nil {
		t.Fatal("plain send to a gated delegate did not refuse")
	}
	if !strings.Contains(res.Err.Error(), "being disposed") {
		t.Fatalf("plain send refusal error = %q, want it to name the disposing state", res.Err)
	}
	if res.WatchSendDeliveryClass == watchSendBusy && res.WatchSendDeliveryClassSet {
		t.Fatal("plain send refusal must not carry a watch classification")
	}
}
