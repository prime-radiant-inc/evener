package agent

import "testing"

// A repeating timer's tick records the timer that produced it, so a clear that
// lands after the append can still suppress the row at delivery time. That
// record answers a question only an UNRESOLVED row can still ask: once the
// attention resolves the row will never become a request, so nothing reads its
// timer again and the entry can never be deleted by the clear path either.
// Keeping it would grow the map by one entry per delivered tick for the life of
// the session.
func TestStableWatchTickMetadataIsDroppedWhenAttentionResolves(t *testing.T) {
	t.Parallel()
	sess, _ := drainingDelegateWithUpdate(t, nil)

	const watchID = "watch:tick-settle"
	tick := func(occurrence string) jobNotification {
		n := watchNotification("job:tick-settle", "repeat")
		n.WatchID = watchID
		n.Fires = 1
		n.IntervalSeconds = 60
		n.Occurrence = occurrence
		return n
	}
	settledID := stableWatchNoticeAttentionID(tick("tick:settle:1"))
	unsettledID := stableWatchNoticeAttentionID(tick("tick:settle:2"))
	if settledID == unsettledID {
		t.Fatal("the two ticks share an attention identity, so the test cannot resolve one without the other")
	}
	for _, id := range []string{settledID, unsettledID} {
		if _, err := sess.appendDelegateNotificationDurably(id, "repeating tick"); err != nil {
			t.Fatalf("append tick %q: %v", id, err)
		}
	}
	sess.pendingJobNotifsMu.Lock()
	sess.stableWatchTickWatch = map[string]string{settledID: watchID, unsettledID: watchID}
	sess.pendingJobNotifsMu.Unlock()

	if err := sess.resolveAttentionDurably([]string{settledID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("resolve the settled tick: %v", err)
	}

	sess.pendingJobNotifsMu.Lock()
	_, settledHeld := sess.stableWatchTickWatch[settledID]
	_, unsettledHeld := sess.stableWatchTickWatch[unsettledID]
	sess.pendingJobNotifsMu.Unlock()
	if settledHeld {
		t.Fatal("the resolved tick kept its timer record: the map grows by one entry per delivered tick and nothing ever deletes it")
	}
	if !unsettledHeld {
		t.Fatal("the unresolved tick lost its timer record, so a clear landing before its delivery can no longer suppress it")
	}
}
