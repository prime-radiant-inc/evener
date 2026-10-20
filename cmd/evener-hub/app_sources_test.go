package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubKnowsRefDoesNotScanSavedTranscript pins that saved-session existence
// is answered from the past index's metadata alone. The retry gate must not
// hydrate a saved session to answer whether it exists, so a session whose
// cumulative usage is not persisted must still produce no transcript scan.
//
// Not parallel: it replaces the package-wide pastDerivedTotalsScan seam.
func TestHubKnowsRefDoesNotScanSavedTranscript(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t, completedExecution("turn_known"))

	original := pastDerivedTotalsScan
	scanCalls := 0
	pastDerivedTotalsScan = func(string, int) (*appwire.EvenerUsage, int, error) {
		scanCalls++
		return nil, 0, nil
	}
	defer func() { pastDerivedTotalsScan = original }()

	if !hubKnowsRef(cfg, localAppRef(entry.Meta.ID)) {
		t.Fatal("known session rejected")
	}
	if scanCalls != 0 {
		t.Fatalf("derived-total scans: %d", scanCalls)
	}
}

// TestHubKnowsRefKeepsKnownSessionWithoutTranscript pins that existence does
// not depend on transcript availability or size: once the past index has
// indexed a session, removing its transcript must not make the retry gate
// reject it. A known entry remains known even when its transcript cannot be
// read.
func TestHubKnowsRefKeepsKnownSessionWithoutTranscript(t *testing.T) {
	t.Run("small", func(t *testing.T) {
		cfg, entry, path := seedIndexedPastSession(t, completedExecution("turn_known"))
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fixture transcript still present: %v", err)
		}
		if !hubKnowsRef(cfg, localAppRef(entry.Meta.ID)) {
			t.Fatal("known session with no transcript rejected")
		}
	})

	t.Run("large", func(t *testing.T) {
		cfg, params, path := seedLargePastThread(t, 4_000)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fixture transcript still present: %v", err)
		}
		if !hubKnowsRef(cfg, params.Ref) {
			t.Fatal("known large session with no transcript rejected")
		}
	})
}

// TestHubKnowsRefRejectsUnknownReferences pins the references the retry gate
// must reject, including a remote ref that names an existing local session:
// the answer is about this hub's saved local past, not about any ref shape.
func TestHubKnowsRefRejectsUnknownReferences(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t, completedExecution("turn_known"))
	ref := localAppRef(entry.Meta.ID)
	if !hubKnowsRef(cfg, ref) {
		t.Fatalf("known local reference %q rejected", ref)
	}

	cases := []struct {
		name string
		cfg  hubcore.WebConfig
		ref  string
	}{
		{name: "nil past", cfg: hubcore.WebConfig{}, ref: ref},
		{name: "missing local id", cfg: cfg, ref: localAppRef("02wMz5Txv47YP64RR3B9YJ")},
		{name: "remote ref with existing local id", cfg: cfg, ref: "remote:" + entry.Meta.ID},
		{name: "malformed ref", cfg: cfg, ref: "not-a-ref"},
		{name: "empty ref", cfg: cfg, ref: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hubKnowsRef(tc.cfg, tc.ref) {
				t.Fatalf("unknown reference %q accepted", tc.ref)
			}
		})
	}
}

// BenchmarkHubKnowsRef times saved-session existence across transcript sizes
// with a warm past index. The saved transcripts are empty, small and large;
// every iteration asserts the known session is accepted.
func BenchmarkHubKnowsRef(b *testing.B) {
	cases := []struct {
		name   string
		rounds int
	}{
		{name: "empty", rounds: 0},
		{name: "small", rounds: 50},
		{name: "large", rounds: 4_000},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			cfg, params, path := seedLargePastThread(b, tc.rounds)
			if info, err := os.Stat(path); err == nil {
				b.Logf("transcript %s: %d bytes, %d rounds", filepath.Base(path), info.Size(), tc.rounds)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !hubKnowsRef(cfg, params.Ref) {
					b.Fatal("known session rejected")
				}
			}
		})
	}
}
