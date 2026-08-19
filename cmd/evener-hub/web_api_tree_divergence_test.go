package main

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestAppThreadTreeEntries_ForkedRemoteThreadStampsDivergenceTurn pins issue
// #152: a remote thread with a ParentRef (forked) must not leave DivergenceTurn
// at zero. The kata 96cp invariant is ParentSessionID != "" implies DivergenceTurn
// >= 1 (every fork writer validates divergenceTurn >= 1; a zero with a parent
// implies a spawned delegate). A hub-synthesized meta with ParentSessionID set
// and DivergenceTurn 0 would be misclassified as a delegate by any consumer
// applying the invariant.
func TestAppThreadTreeEntries_ForkedRemoteThreadStampsDivergenceTurn(t *testing.T) {
	thread := appwire.Thread{
		Source:   "remote",
		ID:       "child-thread-1",
		SessionID: "child-session-1",
		Evener: appwire.EvenerThread{
			Ref:       "remote:child-thread-1",
			ParentRef: "remote:parent-thread-1",
		},
	}
	meta, _, ok := appThreadTreeEntries(thread)
	if !ok {
		t.Fatalf("appThreadTreeEntries returned ok=false for a thread with a valid ref")
	}
	if meta.ParentSessionID == "" {
		t.Fatalf("ParentSessionID is empty; the thread's ParentRef did not resolve (setup error)")
	}
	if meta.DivergenceTurn == 0 {
		t.Fatalf("issue #152: DivergenceTurn is 0 while ParentSessionID=%q is set; "+
			"the kata 96cp invariant (ParentSessionID != \"\" implies DivergenceTurn >= 1, i.e. a fork not a delegate) "+
			"is violated. A hub-synthesized meta with a parent and zero divergence would be "+
			"misclassified as a delegate by any consumer applying the invariant.", meta.ParentSessionID)
	}
}

// TestAppThreadTreeEntries_NoParentLeavesDivergenceTurnZero pins the flip
// side: a thread with no ParentRef must leave DivergenceTurn at zero (it's not
// a fork).
func TestAppThreadTreeEntries_NoParentLeavesDivergenceTurnZero(t *testing.T) {
	thread := appwire.Thread{
		Source:   "remote",
		ID:       "standalone-thread",
		SessionID: "standalone-session",
		Evener: appwire.EvenerThread{
			Ref: "remote:standalone-thread",
		},
	}
	meta, _, ok := appThreadTreeEntries(thread)
	if !ok {
		t.Fatalf("appThreadTreeEntries returned ok=false for a thread with a valid ref")
	}
	if meta.ParentSessionID != "" {
		t.Fatalf("ParentSessionID=%q should be empty for a thread with no ParentRef", meta.ParentSessionID)
	}
	if meta.DivergenceTurn != 0 {
		t.Fatalf("DivergenceTurn=%d should be 0 for a thread with no parent (not a fork)", meta.DivergenceTurn)
	}
}
