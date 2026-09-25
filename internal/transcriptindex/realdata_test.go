package transcriptindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/internal/appitempaging"
)

// realTranscriptsEnv names real transcripts for the opt-in tests below, as a
// colon-separated list of paths. Each is copied before use; the original is
// only read. Default runs skip these tests.
const realTranscriptsEnv = "EVENER_TRANSCRIPT_INDEX_REAL"

// Latency gate, from the transcript read model spec's acceptance criteria.
const (
	latencySamples     = 200
	idleP99Limit       = 50 * time.Millisecond
	appendingP99Limit  = 100 * time.Millisecond
	appendInterval     = 100 * time.Millisecond
	appendingReadEvery = 10 * time.Millisecond
	windowLimit        = 40
)

// realTranscripts copies each opted-in transcript into a temp dir.
func realTranscripts(t *testing.T) []string {
	t.Helper()
	list := os.Getenv(realTranscriptsEnv)
	if list == "" {
		t.Skipf("set %s to a colon-separated list of transcript paths", realTranscriptsEnv)
	}
	var copies []string
	for source := range strings.SplitSeq(list, ":") {
		copies = append(copies, copyFile(t, source))
	}
	return copies
}

func copyFile(t *testing.T, source string) string {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close() //nolint:errcheck // read-only
	path := filepath.Join(t.TempDir(), filepath.Base(source))
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRealTranscriptWindowsEqualTheReference(t *testing.T) {
	for _, path := range realTranscripts(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			x := openIndex(t, path, t.TempDir())
			want := referenceCandidates(t, path)
			window, err := x.Latest(windowLimit)
			if err != nil {
				t.Fatal(err)
			}
			assertWindow(t, "latest", window, want, len(want), windowLimit)
			end := len(want) - len(window.Candidates)
			for end > 0 { // every page back to the first item
				window, err := x.Before(want[end].Position, windowLimit)
				if err != nil {
					t.Fatal(err)
				}
				assertWindow(t, "backfill", window, want, end, windowLimit)
				end -= len(window.Candidates)
			}
			random := rand.New(rand.NewPCG(1, 2))
			for range 200 {
				end := random.IntN(len(want))
				window, err := x.Before(want[end].Position, windowLimit)
				if err != nil {
					t.Fatal(err)
				}
				assertWindow(t, "random", window, want, end, windowLimit)
			}
			t.Logf("%s: %d items, every compared window equals the whole-file projection", filepath.Base(path), len(want))
		})
	}
}

func TestRealTranscriptLatency(t *testing.T) {
	for _, path := range realTranscripts(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			name := filepath.Base(path)
			tail := entryTail(t, path, 50)
			started := time.Now()
			x := openIndex(t, path, t.TempDir())
			t.Logf("build file=%s took=%v items=%d turns=%d", name, time.Since(started), x.items.n, x.turns.n)

			// source is the read the in-memory baseline's "source" rows
			// measure; page adds regrouping and JSON encoding, the rest of a
			// thread/read response, as the baseline's "page" rows do.
			source := func() time.Duration {
				started := time.Now()
				if err := x.CatchUp(); err != nil {
					t.Fatal(err)
				}
				if _, err := x.Latest(windowLimit); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}
			page := func() time.Duration {
				started := time.Now()
				if err := x.CatchUp(); err != nil {
					t.Fatal(err)
				}
				window, err := x.Latest(windowLimit)
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
			for _, run := range []struct {
				label     string
				sample    func() time.Duration
				appending bool
				limit     time.Duration
			}{
				{"index-idle", source, false, idleP99Limit},
				{"index-page-idle", page, false, idleP99Limit},
				{"index-appending", source, true, appendingP99Limit},
				{"index-page-appending", page, true, appendingP99Limit},
			} {
				var samples []time.Duration
				if run.appending {
					samples = sampleWhileAppending(t, path, tail, run.sample)
				} else {
					samples = make([]time.Duration, latencySamples)
					for i := range samples {
						samples[i] = run.sample()
					}
				}
				p50, p99 := percentiles(samples)
				t.Logf("latency %s file=%s samples=%d p50=%v p99=%v", run.label, name, len(samples), p50, p99)
				if p99 >= run.limit {
					t.Errorf("%s p99 %v is not under %v", run.label, p99, run.limit)
				}
			}
		})
	}
}

// sampleWhileAppending appends one entry line every appendInterval (cycling
// through lines) while taking a sample every appendingReadEvery.
func sampleWhileAppending(t *testing.T, path string, lines [][]byte, sample func() time.Duration) []time.Duration {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // appends are checked
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var appendErr error
	appended := 0
	wg.Go(func() {
		ticker := time.NewTicker(appendInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := f.Write(lines[appended%len(lines)]); err != nil {
					appendErr = err
					return
				}
				appended++
			}
		}
	})
	samples := make([]time.Duration, latencySamples)
	ticker := time.NewTicker(appendingReadEvery)
	for i := range samples {
		<-ticker.C
		samples[i] = sample()
	}
	ticker.Stop()
	close(stop)
	wg.Wait()
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	t.Logf("appended %d entries while sampling", appended)
	return samples
}

// entryTail returns the file's last n entry lines, newline included.
func entryTail(t *testing.T, path string, n int) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	start := max(0, info.Size()-(64<<20))
	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, info.Size()-start), 1<<20)
	var lines [][]byte
	if start > 0 {
		if _, err := reader.ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines[max(0, len(lines)-n):]
}

func percentiles(samples []time.Duration) (p50, p99 time.Duration) {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	at := func(q float64) time.Duration {
		return sorted[min(len(sorted)-1, int(q*float64(len(sorted))+0.5)-1)]
	}
	return at(0.50), at(0.99)
}
