package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// Caller frames address a session, never a parent's child-manager token slot.
func TestWatchFrameRequiresExactReceiver(t *testing.T) {
	s, _ := notificationReceiverForTest(t, nil)
	jm := newTestJM(t)
	jm.retirementOwner = s
	state := jobstore.WatchSendState{DeliveryID: "exact-caller-7", UpdateSeq: 1, ReceiverSessionID: "missing", Frame: "exact-frame-7"}
	if err := jm.acceptWatchFrame(state); err == nil {
		t.Fatal("accepted a frame addressed to another receiver")
	}
	assertNotificationPending(t, s, []string{})
	state.ReceiverSessionID = s.ID()
	if err := jm.acceptWatchFrame(state); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{stableWatchAttentionID(state)})
}
