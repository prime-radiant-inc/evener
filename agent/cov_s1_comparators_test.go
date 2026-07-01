package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// s1cov_baseKey is a fully-populated key whose fields tests mutate one at a time
// to walk every tie-break branch of watchSendKeyLess.
func s1cov_baseKey() jobstore.WatchSendKey {
	return jobstore.WatchSendKey{
		VisibleSessionID:        "sess",
		WatchTarget:             "target",
		ResolvedWatchedIdentity: "ident",
		ResolvedSendTo:          "sendto",
		WatchID:                 "watch",
		WatchGeneration:         "gen",
	}
}

func TestS1Cov_watchSendKeyLess(t *testing.T) {
	tests := []struct {
		name string
		mut  func(k *jobstore.WatchSendKey)
	}{
		{"visible_session", func(k *jobstore.WatchSendKey) { k.VisibleSessionID = "zzz" }},
		{"watch_target", func(k *jobstore.WatchSendKey) { k.WatchTarget = "zzz" }},
		{"watched_identity", func(k *jobstore.WatchSendKey) { k.ResolvedWatchedIdentity = "zzz" }},
		{"send_to", func(k *jobstore.WatchSendKey) { k.ResolvedSendTo = "zzz" }},
		{"watch_id", func(k *jobstore.WatchSendKey) { k.WatchID = "zzz" }},
		{"generation", func(k *jobstore.WatchSendKey) { k.WatchGeneration = "zzz" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := s1cov_baseKey()
			b := s1cov_baseKey()
			tc.mut(&b)
			if !watchSendKeyLess(a, b) {
				t.Fatalf("watchSendKeyLess(a,b) expected true when b's %s is larger", tc.name)
			}
			if watchSendKeyLess(b, a) {
				t.Fatalf("watchSendKeyLess(b,a) expected false when b's %s is larger", tc.name)
			}
		})
	}
	// Fully equal keys tie-break to the final generation compare returning false.
	if watchSendKeyLess(s1cov_baseKey(), s1cov_baseKey()) {
		t.Fatal("equal keys must not be less")
	}
}

func TestS1Cov_watchSendStateLess(t *testing.T) {
	t0 := time.Unix(1000, 0).UTC()
	t1 := t0.Add(time.Second)
	mk := func(created, updated time.Time, seq uint64, k jobstore.WatchSendKey) *jobstore.WatchSendState {
		return &jobstore.WatchSendState{CreatedAt: created, UpdatedAt: updated, UpdateSeq: seq, Key: k}
	}
	key := s1cov_baseKey()

	// CreatedAt differs.
	if !watchSendStateLess(mk(t0, t0, 0, key), mk(t1, t0, 0, key)) {
		t.Fatal("earlier CreatedAt must be less")
	}
	// CreatedAt equal, UpdatedAt differs.
	if !watchSendStateLess(mk(t0, t0, 0, key), mk(t0, t1, 0, key)) {
		t.Fatal("earlier UpdatedAt must be less")
	}
	// CreatedAt/UpdatedAt equal, UpdateSeq differs.
	if !watchSendStateLess(mk(t0, t0, 1, key), mk(t0, t0, 2, key)) {
		t.Fatal("lower UpdateSeq must be less")
	}
	// All equal but key differs → delegates to watchSendKeyLess.
	bigKey := s1cov_baseKey()
	bigKey.WatchGeneration = "zzz"
	if !watchSendStateLess(mk(t0, t0, 0, key), mk(t0, t0, 0, bigKey)) {
		t.Fatal("must delegate to key compare")
	}
	// Fully equal → false.
	if watchSendStateLess(mk(t0, t0, 0, key), mk(t0, t0, 0, key)) {
		t.Fatal("equal states must not be less")
	}
}
