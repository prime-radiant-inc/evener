package schema

import "testing"

// A labeled fork's parent-meta write carries a snapshot loaded before the live
// session minted a later wait stamp. Saving that stale copy must not roll the
// counter back: it is a high-water mark, and rolling it back would let a
// restore mint a stamp a pane already saw.
func TestSavingAStaleQuotaWaitStampDoesNotRollTheCounterBack(t *testing.T) {
	const id = "02QuotaWaitStampHighWater01"
	dir := t.TempDir()
	live := SessionMeta{ID: id, Name: "live", QuotaWaitStamps: 5}
	if err := SaveSessionMeta(dir, live); err != nil {
		t.Fatalf("save the live meta: %v", err)
	}
	stale := live
	stale.QuotaWaitStamps = 3
	if err := SaveSessionMeta(dir, stale); err != nil {
		t.Fatalf("save the stale fork copy: %v", err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("load the saved meta: %v", err)
	}
	if got.QuotaWaitStamps != 5 {
		t.Fatalf("persisted QuotaWaitStamps = %d after a stale copy carrying 3 was saved, want 5: the counter is a high-water mark", got.QuotaWaitStamps)
	}
}
