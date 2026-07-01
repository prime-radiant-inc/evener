package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestS1Cov_watchSendEventMatchesState(t *testing.T) {
	key := jobstore.WatchSendKey{VisibleSessionID: "s", WatchTarget: "job_a", WatchID: "w"}
	other := key
	other.WatchID = "w2"

	base := jobstore.WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 5}

	// A nil event state never matches.
	if watchSendEventMatchesState(nil, base) {
		t.Fatal("nil event state must not match")
	}
	// A different key never matches.
	if watchSendEventMatchesState(&jobstore.WatchSendState{Key: other}, jobstore.WatchSendState{Key: key}) {
		t.Fatal("key mismatch must not match")
	}
	// A delivery-id constraint that differs fails.
	if watchSendEventMatchesState(&jobstore.WatchSendState{Key: key, DeliveryID: "dX"}, base) {
		t.Fatal("delivery id mismatch must not match")
	}
	// An update-seq constraint that differs fails.
	if watchSendEventMatchesState(&jobstore.WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 6}, base) {
		t.Fatal("update seq mismatch must not match")
	}
	// A fully consistent event state matches; a zero-valued constraint is a
	// wildcard.
	if !watchSendEventMatchesState(&jobstore.WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 5}, base) {
		t.Fatal("consistent event state must match")
	}
	if !watchSendEventMatchesState(&jobstore.WatchSendState{Key: key, DeliveryID: "anything", UpdateSeq: 99}, jobstore.WatchSendState{Key: key}) {
		t.Fatal("unconstrained state must match on key alone")
	}
}
