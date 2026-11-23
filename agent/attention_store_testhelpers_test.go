package agent

import "testing"

// acceptAttentionForTest latches the trigger a reservation carries exactly the
// way a committed attention start does now: the row stays durably queued, and
// only the in-memory latch records that this generation carries it.
func acceptAttentionForTest(t *testing.T, sess *Session, reservation *delegateStartReservation) {
	t.Helper()
	if sess == nil || sess.delegateController == nil {
		t.Fatal("accept attention: session has no controller")
	}
	sess.delegateController.mu.Lock()
	record, err := sess.delegateController.reservationRecordLocked(reservation)
	attentionID := ""
	if err == nil && record != nil {
		attentionID = record.attentionID
	}
	sess.delegateController.mu.Unlock()
	if attentionID == "" {
		t.Fatalf("accept attention: reservation carries no attention row (%v)", err)
	}
	sess.holdAcceptedTrigger(attentionID)
}
