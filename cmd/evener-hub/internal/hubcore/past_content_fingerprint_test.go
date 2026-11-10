package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// A row replaced by another session with the same name and timestamp is still
// a content change the Hub must publish, so the fingerprint covers the ID.
func TestContentFingerprintTracksTheSessionID(t *testing.T) {
	meta := schema.SessionMeta{Name: "Session", UpdatedAt: time.Unix(1000, 0)}
	first := []PastEntry{{ID: "s1", Meta: meta}}
	second := []PastEntry{{ID: "s2", Meta: meta}}
	if contentFingerprint(first) == contentFingerprint(second) {
		t.Fatal("two sessions differing only by ID share a fingerprint")
	}
}
