package appwire

import "testing"

// FuzzTranscriptItemLimitNormalization keeps the public item-page default and
// maximum deterministic across arbitrary request values.
func FuzzTranscriptItemLimitNormalization(f *testing.F) {
	for _, seed := range []int{-1, 0, 1, TranscriptItemPageLimit, TranscriptItemPageLimit + 1} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, limit int) {
		got, err := NormalizeTranscriptItemLimit(limit)
		if limit <= 0 {
			if err != nil || got != TranscriptItemPageLimit {
				t.Fatalf("limit %d = (%d, %v), want default %d", limit, got, err, TranscriptItemPageLimit)
			}
			return
		}
		if limit > TranscriptItemPageLimit {
			if err == nil || got != 0 {
				t.Fatalf("limit %d = (%d, %v), want invalid zero result", limit, got, err)
			}
			return
		}
		if err != nil || got != limit {
			t.Fatalf("limit %d = (%d, %v), want unchanged valid limit", limit, got, err)
		}
	})
}

func TestTranscriptItemPagingHasNoTurnWindowHelpers(t *testing.T) {
	if got, err := NormalizeTranscriptItemLimit(0); err != nil || got != TranscriptItemPageLimit {
		t.Fatalf("default item limit = (%d, %v), want %d", got, err, TranscriptItemPageLimit)
	}
}
