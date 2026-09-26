package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/internal/appitempaging"
)

// realTranscriptsEnv names real transcripts for the opt-in baseline below, as
// a colon-separated list of paths. The same variable drives the transcript
// index's latency test (internal/transcriptindex), so one run measures both
// sides on the same files. Default runs skip it.
const realTranscriptsEnv = "EVENER_TRANSCRIPT_INDEX_REAL"

// TestRealTranscriptInMemoryReadLatency measures today's thread/read source for
// the latest window, the daemon's in-memory snapshot, on a real transcript:
// the baseline the transcript read model's latency gate compares against.
// "cached" reads with the item projection already built, as an idle daemon
// does; "invalidated" rebuilds it first, as the first read after any
// notification does. "page" adds regrouping and JSON encoding, the rest of a
// thread/read response.
func TestRealTranscriptInMemoryReadLatency(t *testing.T) {
	list := os.Getenv(realTranscriptsEnv)
	if list == "" {
		t.Skipf("set %s to a colon-separated list of transcript paths", realTranscriptsEnv)
	}
	for path := range strings.SplitSeq(list, ":") {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			turns, _, err := appTurnsFromTranscriptFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := &appTurnSnapshot{threadID: "th_latency"}
			snapshot.Seed(appTurnSeed{Turns: turns, ThreadRef: "local:th_latency"})
			t.Logf("project file=%s took=%v turns=%d", name, time.Since(started), len(turns))

			source := func() time.Duration {
				started := time.Now()
				if _, _, err := snapshot.LatestItemCandidates(40); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}
			page := func() time.Duration {
				started := time.Now()
				window, _, err := snapshot.LatestItemCandidates(40)
				if err != nil {
					t.Fatal(err)
				}
				fragments, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(window.Candidates))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := json.Marshal(fragments); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}
			invalidate := func() {
				snapshot.mu.Lock()
				snapshot.itemProjection = nil
				snapshot.mu.Unlock()
			}
			source() // builds the cached item projection
			report := func(label string, sample func() time.Duration, before func()) {
				samples := make([]time.Duration, 200)
				for i := range samples {
					if before != nil {
						before()
					}
					samples[i] = sample()
				}
				slices.Sort(samples)
				// The quantile rule internal/transcriptindex's latency test
				// uses, so the two sides report comparable figures.
				at := func(q float64) time.Duration {
					return samples[min(len(samples)-1, int(q*float64(len(samples))+0.5)-1)]
				}
				t.Logf("latency %s file=%s samples=%d p50=%v p99=%v", label, name, len(samples), at(0.50), at(0.99))
			}
			report("baseline-cached", source, nil)
			report("baseline-invalidated", source, invalidate)
			report("baseline-page-cached", page, nil)
			report("baseline-page-invalidated", page, invalidate)
		})
	}
}
