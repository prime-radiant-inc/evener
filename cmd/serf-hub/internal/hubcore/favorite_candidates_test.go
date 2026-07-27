package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
)

func TestFavoriteCandidatesExplicitArchiveDecisionExcludesLiveWithoutMeta(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "live-without-meta"
	tree := BuildTreeAt(nil, []LiveEntry{{
		SessionID: id,
		Status:    "active",
		Entry:     rendezvous.Entry{SessionID: id, StartedAt: now},
	}}, map[ArchiveKey]bool{{Kind: "session", ID: id}: true}, now)
	if candidates := tree.FavoriteCandidates(); len(candidates) != 0 {
		t.Fatalf("explicitly archived live orphan entered favorite candidates: %+v", candidates)
	}
}
