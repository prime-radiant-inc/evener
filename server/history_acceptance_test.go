package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/transcriptindex"
)

// The transcript read model's acceptance measurements (plan Task 19, spec
// "Acceptance criteria"). Both tests are opt-in and read only copies: never
// Jesse's live state directory.

// trmSessionDirEnv names a directory of *.transcript.jsonl files (a real
// session copy, root and delegates) for the memory measurement. Unset:
// TestRealSessionRetainedHistoryMemory skips.
const trmSessionDirEnv = "EVENER_TRM_SESSION_DIR"

// trmTranscriptRealEnv names a colon-separated list of real transcript paths
// for the latency measurement, the same variable
// internal/transcriptindex's real-data tests use. Unset:
// TestRealTranscriptHistoryReadLatency skips.
const trmTranscriptRealEnv = "EVENER_TRANSCRIPT_INDEX_REAL"

// TestRealSessionRetainedHistoryMemory measures retained history memory on a
// long-lived daemon that has replayed a real session's activity (spec:
// "after replaying its activity rather than after a cold restart"). It runs
// the full session, then the 20 smallest transcripts in it, so the per-thread
// figures can be compared: the criterion is that no term grows with history
// size.
func TestRealSessionRetainedHistoryMemory(t *testing.T) {
	dir := os.Getenv(trmSessionDirEnv)
	if dir == "" {
		t.Skipf("set %s to a directory of *.transcript.jsonl files (a real session copy, root and delegates)", trmSessionDirEnv)
	}
	sources, err := filepath.Glob(filepath.Join(dir, "*.transcript.jsonl"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(sources) == 0 {
		t.Fatalf("%s has no *.transcript.jsonl files: %s", trmSessionDirEnv, dir)
	}

	full := measureRetainedHistoryMemory(t, sources)
	t.Logf("memory full-session threads=%d retained=%d handles=%d notice-budget-used=%d max-thread-notice-bytes=%d",
		full.threads, full.retained, full.openHandles, full.noticeBudgetUsed, full.maxThreadNoticeBytes)

	smallest := smallestTranscripts(t, sources, 20)
	small := measureRetainedHistoryMemory(t, smallest)
	t.Logf("memory 20-smallest threads=%d retained=%d handles=%d notice-budget-used=%d max-thread-notice-bytes=%d",
		small.threads, small.retained, small.openHandles, small.noticeBudgetUsed, small.maxThreadNoticeBytes)
	t.Logf("memory per-thread-retained full=%d smallest=%d",
		full.retained/int64(full.threads), small.retained/int64(small.threads))

	if full.maxThreadNoticeBytes > 64<<10 {
		t.Errorf("a thread's notice ring held %d bytes, want at most 64 KiB", full.maxThreadNoticeBytes)
	}
	if full.noticeBudgetUsed > appoverlay.DefaultBudgetBytes {
		t.Errorf("the notice budget held %d bytes, want at most %d (16 MiB) daemon-wide", full.noticeBudgetUsed, appoverlay.DefaultBudgetBytes)
	}
	if full.openHandles > transcriptindex.DefaultCacheCapacity {
		t.Errorf("the index cache held %d open handles, want at most its capacity %d", full.openHandles, transcriptindex.DefaultCacheCapacity)
	}
}

// retainedHistoryMeasurement is one run of measureRetainedHistoryMemory.
type retainedHistoryMeasurement struct {
	threads              int
	retained             int64
	openHandles          int
	noticeBudgetUsed     int
	maxThreadNoticeBytes int
}

// measureRetainedHistoryMemory replays every source transcript's activity
// into its own thread history on a fresh Server, then reports what stayed on
// the heap: the criterion is that this holds only the index handle cache and
// each thread's projection goroutine state, not a copy of history.
func measureRetainedHistoryMemory(t *testing.T, sources []string) retainedHistoryMeasurement {
	t.Helper()
	dir := t.TempDir()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	watcher := watchHistoryPublications(t)

	var root string
	for _, source := range sources {
		header := readTranscriptHeader(t, source)
		if header.ParentSessionID == "" {
			root = header.SessionID
		}
	}
	t.Logf("replaying %d transcripts (root %s) into %s", len(sources), root, dir)

	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	var writers []*transcript.Writer
	maxNoticeBytes := 0
	for _, source := range sources {
		writer, history, recordedLength := replayRealTranscript(t, srv, source, dir)
		watcher.await(t, history, recordedLength)
		if n := history.overlay.NoticeBytes(); n > maxNoticeBytes {
			maxNoticeBytes = n
		}
		writers = append(writers, writer)
	}
	for _, w := range writers {
		if err := w.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
	}

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	return retainedHistoryMeasurement{
		threads:              len(sources),
		retained:             int64(after.HeapAlloc) - int64(before.HeapAlloc),
		openHandles:          srv.appHistories.cache.OpenHandles(),
		noticeBudgetUsed:     srv.appHistories.budget.Used(),
		maxThreadNoticeBytes: maxNoticeBytes,
	}
}

// readTranscriptHeader decodes source's header line only.
func readTranscriptHeader(t *testing.T, source string) transcript.Header {
	t.Helper()
	f, err := os.Open(source)
	if err != nil {
		t.Fatalf("open %s: %v", source, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	line, complete, _, err := transcript.ReadLine(bufio.NewReader(f), transcript.DefaultMaxLineBytes)
	if err != nil || !complete {
		t.Fatalf("read header of %s: complete=%v err=%v", source, complete, err)
	}
	header, err := transcript.DecodeHeader(line)
	if err != nil {
		t.Fatalf("decode header of %s: %v", source, err)
	}
	return header
}

// replayRealTranscript copies source's header into a fresh file under dir,
// registers a thread history for it on srv, and replays every entry line
// through a transcript.Writer with the history's recorded hook installed
// (transcript.PlaceVerbatim keeps each entry's own identity). It feeds the
// overlay a synthetic round_timings notice every 20 entries, matching the
// plan's stress on the notice ring. It returns the writer (still open), the
// registered history and the length replay recorded.
func replayRealTranscript(t *testing.T, srv *Server, source, dir string) (*transcript.Writer, *threadHistory, int64) {
	t.Helper()
	header := readTranscriptHeader(t, source)
	path := filepath.Join(dir, filepath.Base(source))
	writer, err := transcript.NewWriterNoSync(path, header)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	writer.SyncInterval = time.Hour

	threadID := header.SessionID
	ref := appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	history := srv.ensureHistory(threadID, ref, path, 0, 0, "1")
	writer.OnRecorded(func(rec transcript.Record) { history.recorded(rec) })

	f, err := os.Open(source)
	if err != nil {
		t.Fatalf("open %s: %v", source, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	reader := bufio.NewReader(f)
	if _, complete, _, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes); err != nil || !complete {
		t.Fatalf("skip header of %s: complete=%v err=%v", source, complete, err)
	}
	entries := 0
	for {
		line, complete, _, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			t.Fatalf("read entry of %s: %v", source, err)
		}
		if !complete {
			break
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatalf("decode entry %d of %s: %v", entries, source, err)
		}
		if _, err := writer.Record(entry.Turn, transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record entry %d of %s: %v", entries, source, err)
		}
		entries++
		if entries%20 == 0 {
			history.overlayEvent(events.New(events.RoundTimings{}))
		}
	}
	recordedLength := writer.RecordedLength()
	return writer, history, recordedLength
}

// smallestTranscripts returns the n smallest files among sources by size.
func smallestTranscripts(t *testing.T, sources []string, n int) []string {
	t.Helper()
	type sized struct {
		path string
		size int64
	}
	entries := make([]sized, len(sources))
	for i, path := range sources {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		entries[i] = sized{path, info.Size()}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })
	if len(entries) > n {
		entries = entries[:n]
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.path
	}
	return out
}

// TestRealTranscriptHistoryReadLatency measures the full thread/read handler
// path (capture, latest, regroup and JSON encoding) at the default page size
// on a copy of each real transcript, idle and while a goroutine appends one
// entry line every 100 ms (spec "Acceptance criteria — Latency").
func TestRealTranscriptHistoryReadLatency(t *testing.T) {
	for _, path := range realHistoryTranscripts(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			name := filepath.Base(path)
			srv, threadID, writer, tailTurns := servedRealTranscript(t, path)

			sample := func() time.Duration {
				started := time.Now()
				response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{ThreadID: threadID, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := json.Marshal(response); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}

			for _, run := range []struct {
				label     string
				appending bool
				limit     time.Duration
			}{
				{"history-read-idle", false, idleP99Limit},
				{"history-read-appending", true, appendingP99Limit},
			} {
				var samples []time.Duration
				if run.appending {
					samples = sampleWhileAppendingViaWriter(t, writer, tailTurns, sample)
				} else {
					samples = make([]time.Duration, latencySamples)
					for i := range samples {
						samples[i] = sample()
					}
				}
				p50, p99 := historyLatencyPercentiles(samples)
				t.Logf("latency %s file=%s samples=%d p50=%v p99=%v", run.label, name, len(samples), p50, p99)
				if p99 >= run.limit {
					t.Errorf("%s p99 %v is not under %v", run.label, p99, run.limit)
				}
			}
		})
	}
}

// Latency gate constants, from the transcript read model spec's acceptance
// criteria (the phase 1 quantile rule: 200 samples, p50/p99).
const (
	latencySamples    = 200
	idleP99Limit      = 50 * time.Millisecond
	appendingP99Limit = 100 * time.Millisecond
	appendInterval    = 100 * time.Millisecond
	sampleEvery       = 10 * time.Millisecond
)

// realHistoryTranscripts copies each opted-in transcript into a temp dir.
func realHistoryTranscripts(t *testing.T) []string {
	t.Helper()
	list := os.Getenv(trmTranscriptRealEnv)
	if list == "" {
		t.Skipf("set %s to a colon-separated list of transcript paths", trmTranscriptRealEnv)
	}
	var copies []string
	for source := range strings.SplitSeq(list, ":") {
		copies = append(copies, copyRealTranscript(t, source))
	}
	return copies
}

// copyRealTranscript copies source into a fresh path under t.TempDir(); the
// original is only read.
func copyRealTranscript(t *testing.T, source string) string {
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
	if _, err := out.ReadFrom(in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// servedRealTranscript resumes path's writer, serves its session as the
// daemon's root thread on a fresh Server (with the writer's recorded-entry
// hook wired to it, as serve does), and returns the last 50 entries' turns
// for sampleWhileAppendingViaWriter to re-record (transcript.PlaceVerbatim:
// exactly what a fork's verbatim copy does, so replaying them is a realistic
// append of realistically sized entries; the test only measures read
// latency, not the resulting history's semantics).
func servedRealTranscript(t *testing.T, path string) (*Server, string, *transcript.Writer, []schema.Turn) {
	t.Helper()
	header := readTranscriptHeader(t, path)
	threadID := header.SessionID
	writer, entries, err := transcript.OpenWriterForSession(path, threadID)
	if err != nil {
		t.Fatalf("open %s for resume: %v", path, err)
	}
	writer.SyncInterval = time.Hour
	t.Cleanup(func() { _ = writer.Close() })

	tail := entries[max(0, len(entries)-50):]
	tailTurns := make([]schema.Turn, len(tail))
	for i, e := range tail {
		tailTurns[i] = e.Turn
	}

	prepared, err := PrepareAppIdentity("local", threadID, path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	writer.OnRecorded(func(rec transcript.Record) { srv.recordTranscriptEntry(threadID, rec) })
	srv.ReplaceAppIdentity(prepared.WithRecordedLength(writer.RecordedLength()).WithBootGeneration("1"), nil)
	return srv, threadID, writer, tailTurns
}

// sampleWhileAppendingViaWriter records one turn every appendInterval
// (cycling through turns) while taking a sample every sampleEvery.
func sampleWhileAppendingViaWriter(t *testing.T, writer *transcript.Writer, turns []schema.Turn, sample func() time.Duration) []time.Duration {
	t.Helper()
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
				if _, err := writer.Record(turns[appended%len(turns)], transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil {
					appendErr = err
					return
				}
				appended++
			}
		}
	})
	samples := make([]time.Duration, latencySamples)
	ticker := time.NewTicker(sampleEvery)
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

// historyLatencyPercentiles is the phase 1 quantile rule
// (internal/transcriptindex/realdata_test.go's percentiles).
func historyLatencyPercentiles(samples []time.Duration) (p50, p99 time.Duration) {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	at := func(q float64) time.Duration {
		return sorted[min(len(sorted)-1, int(q*float64(len(sorted))+0.5)-1)]
	}
	return at(0.50), at(0.99)
}
