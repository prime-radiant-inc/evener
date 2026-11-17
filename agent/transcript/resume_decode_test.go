package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// resumeDecodeOverlapTimeout bounds how long the concurrency test's first
// decode waits for a second one to enter. It is generous enough that a loaded
// host cannot trip it and short enough that a sequential scan fails the test
// quickly instead of hanging it.
const resumeDecodeOverlapTimeout = 30 * time.Second

// seamEntries counts every decode the seam has seen enter, across tests.
var seamEntries atomic.Int64

// waitForResumeDecoderGoroutines waits up to d for every pool goroutine to
// exit and returns how many remained. Workers and the collector exit promptly
// once finish runs; a skipped finish leaves them blocked forever, so any
// remaining after d is a leak rather than slowness.
func waitForResumeDecoderGoroutines(d time.Duration) int64 {
	deadline := time.Now().Add(d)
	for {
		if n := resumeDecoderGoroutines.Load(); n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Millisecond)
	}
}

// failingReadFS fails the read of path once fewer than remaining bytes are
// left, so a scan over a longer transcript submits entries and then hits a read
// error. It exists to put the scan on its read-error path after submits, which
// a header or open failure cannot do.
type failingReadFS struct {
	afero.Fs
	path      string
	remaining int
}

// OpenFile is the seam the resume path uses, so the wrapper has to be here
// rather than only on Open.
func (f *failingReadFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil || name != f.path {
		return file, err
	}
	return &failingReadFile{File: file, remaining: f.remaining}, nil
}

func (f *failingReadFS) Open(name string) (afero.File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0o644)
}

type failingReadFile struct {
	afero.File
	remaining int
}

func (f *failingReadFile) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errors.New("injected read failure")
	}
	if len(p) > f.remaining {
		p = p[:f.remaining]
	}
	n, err := f.File.Read(p)
	f.remaining -= n
	return n, err
}

// A resume scan decodes every recorded entry before a resumed daemon can
// announce itself. That decode is pure CPU over lines with no ordering
// dependency between them, so the scan runs it on a bounded pool of workers
// instead of one goroutine (resume_decode.go).
//
// These tests pin the two halves of that, neither of which is about elapsed
// time -- this repository's suites run on hosts under heavy load, where a
// duration measures the host and not the code:
//
//   - Work performed: the scan's decodes really overlap. The seam counts how
//     many are inside DecodeEntry at once on a transcript of independent
//     lines; a sequential scan can only ever report one.
//   - Ordering and outcome: the entries, the next sequence, the recorded
//     length, the failure sentinel and the crash-tail truncation a resumed
//     scan reports are the ones the file says, whichever worker finished
//     first. The corpus table in resume_corpus_test.go pins those outcomes on
//     every transcript shape; the ordering test below pins that they do not
//     depend on completion order.

// resumeScanBody builds a transcript of independent USER_INPUT entries.
func resumeScanBody(entries int) string {
	var body strings.Builder
	body.WriteString(corpusHeader)
	for i := range entries {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User("u"+strconv.Itoa(i)))
		encoded, err := json.Marshal(Entry{Kind: "entry", MachineryFlagged: true, Seq: i, Turn: turn})
		if err != nil {
			panic(err)
		}
		body.WriteString(string(encoded) + "\n")
	}
	return body.String()
}

func writeResumeScanTranscript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// TestResumeScanDecodesEntriesConcurrently pins that a resume scan's decodes
// overlap. A sequential scan reports a peak of one no matter how large the
// transcript, so the assertion fails on the code this test was written
// against.
//
// The overlap is forced, not hoped for: the seam holds the first decode that
// enters until a second one enters, and waiting on a channel yields the
// processor, so this holds even on a single P and under GOMAXPROCS=1. A
// sequential scan never lets the second decode enter, and the bounded wait
// then fails the test instead of hanging it.
func TestResumeScanDecodesEntriesConcurrently(t *testing.T) {
	// Not parallel: it installs the package-level resumeDecodeSeam.
	// The scan sizes its pool from GOMAXPROCS, so pin two here: the claim is
	// that the scan runs decodes in parallel at all, and a one-P host would
	// otherwise measure the pool's width rather than the scan's behavior.
	previousProcs := runtime.GOMAXPROCS(2)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	const entries = 200
	path := writeResumeScanTranscript(t, resumeScanBody(entries))

	var inFlight, peak atomic.Int32
	var decodes atomic.Int64
	// overlap receives while the first decode waits for a second one; it is
	// buffered so the second decode never blocks on a test that has moved on.
	overlap := make(chan struct{}, 1)
	var first sync.Once
	var second atomic.Bool
	resumeDecodeSeam = func(entering bool) {
		if entering {
			decodes.Add(1)
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			if n > 1 && second.CompareAndSwap(false, true) {
				select {
				case overlap <- struct{}{}:
				default:
				}
			}
			// The first decode holds until a second one has entered, so the
			// peak cannot depend on whether this host happens to schedule a
			// second worker inside a sub-millisecond window.
			first.Do(func() {
				select {
				case <-overlap:
				case <-time.After(resumeDecodeOverlapTimeout):
					// A sequential scan can never reach here; the assertion
					// below reports it rather than this wait.
				}
			})
			return
		}
		inFlight.Add(-1)
	}
	t.Cleanup(func() { resumeDecodeSeam = nil })

	w, got, err := OpenWriterForSession(path, "CORPUS")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer func() { _ = w.Close() }()

	if len(got) != entries {
		t.Fatalf("resumed %d entries, want %d", len(got), entries)
	}
	if decodes.Load() != entries {
		t.Fatalf("seam saw %d decodes, want one per entry (%d)", decodes.Load(), entries)
	}
	// The pool's own width is not the claim: the claim is that the scan does
	// not serialize decodes on one goroutine. Any overlap proves that, and
	// requiring more would pin this host's core count.
	if peak.Load() < 2 {
		t.Fatalf("peak concurrent decodes = %d, want at least 2: a resume scan of %d independent entries decoded them one at a time", peak.Load(), entries)
	}
	if n := inFlight.Load(); n != 0 {
		t.Fatalf("%d decodes still in flight after the resume returned", n)
	}
}

// TestResumeDecodeReportsTheFirstFileOrderError pins that the collector names
// the FIRST corrupt line in file order, not whichever worker's error arrived
// first. It feeds the collector the LATER line's result first, so a collector
// that reports arrival order fails here regardless of how the workers raced.
func TestResumeDecodeReportsTheFirstFileOrderError(t *testing.T) {
	t.Parallel()

	firstInFile := errors.New("first line in file order")
	laterInFile := errors.New("later line in file order")
	firstEntry := Entry{Kind: "entry", Seq: 0}
	laterEntry := Entry{Kind: "entry", Seq: 1}

	decode := newResumeDecoder(2)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		// Reverse order: the line at index 1 lands before index 0.
		decode.results <- resumeDecodeResult{index: 1, entry: laterEntry, err: laterInFile}
		decode.results <- resumeDecodeResult{index: 0, entry: firstEntry, err: firstInFile}
	}()
	<-sent

	entries, err := decode.finish()
	if !errors.Is(err, firstInFile) {
		t.Fatalf("decode error = %v, want the first line in file order (%v)", err, firstInFile)
	}
	if len(entries) != 2 || entries[0].Seq != 0 || entries[1].Seq != 1 {
		t.Fatalf("entries = %+v, want the two entries in file order", entries)
	}
}

// TestResumeScanReportsTheFirstFileOrderFailure pins the same selection end to
// end: a failed resume over a file whose first corrupt line is not the first to
// be decoded still reports that line's error, and leaves the file alone.
func TestResumeScanReportsTheFirstFileOrderFailure(t *testing.T) {
	t.Parallel()

	// seq 1 is not JSON at all, which the boundary peek rejects immediately.
	// seq 3 is valid JSON with an unknown member, which the strict decode
	// refuses only after parsing it. The fast error is the first in the file,
	// so this case guards the outcome rather than the arrival order; the
	// collector test above pins the arrival order.
	body := corpusHeader +
		corpusLine(t, 0, schema.NewTurn(schema.TurnUserInput, llm.User("u0"))) +
		"{not json}\n" +
		corpusLine(t, 2, schema.NewTurn(schema.TurnUserInput, llm.User("u2"))) +
		`{"kind":"entry","seq":3,"nonesuch":true,"turn":{"format":1,"kind":"USER_INPUT","message":{"role":"user","content":[]}}}` + "\n"
	path := writeResumeScanTranscript(t, body)

	w, got, err := OpenWriterForSession(path, "CORPUS")
	if w != nil {
		_ = w.Close()
		t.Fatal("a failed resume returned a writer")
	}
	if got != nil {
		t.Fatalf("failed resume returned %d entries, want none", len(got))
	}
	if !errors.Is(err, ErrInvalidRecordBoundary) {
		t.Fatalf("resume error = %v, want the first corrupt line's ErrInvalidRecordBoundary", err)
	}
	// Fail closed includes leaving the file alone, which is what the scan's
	// "truncate only after every complete line validated" order protects.
	onDisk, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read transcript after failed resume: %v", readErr)
	}
	if string(onDisk) != body {
		t.Fatalf("failed resume changed the file: %d bytes, want the original %d", len(onDisk), len(body))
	}
}

// TestResumeScanReportsAnEntryErrorBeforeALaterReadFailure pins that a corrupt
// entry keeps its error when a read fails further along the file. The corrupt
// line is earlier in file order, so it is the first file-order error, and the
// read failure that stopped the scan is second. Reporting the read error would
// hide the line the user has to fix.
func TestResumeScanReportsAnEntryErrorBeforeALaterReadFailure(t *testing.T) {
	t.Parallel()

	body := corpusHeader +
		corpusLine(t, 0, schema.NewTurn(schema.TurnUserInput, llm.User("u0"))) +
		"{not json}\n" +
		corpusLine(t, 2, schema.NewTurn(schema.TurnUserInput, llm.User("u2"))) +
		corpusLine(t, 3, schema.NewTurn(schema.TurnUserInput, llm.User("u3")))
	base := afero.NewMemMapFs()
	if err := base.MkdirAll(filepath.Dir(faultTranscriptPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := afero.WriteFile(base, faultTranscriptPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	// Let the first read through -- it carries the whole file, including the
	// corrupt line -- and fail the second, so the corrupt entry is submitted
	// before the read error lands.
	fs := &failingReadFS{Fs: base, path: faultTranscriptPath, remaining: len(body)}

	w, got, err := OpenWriterForSessionWithFS(fs, faultTranscriptPath, "CORPUS")
	if w != nil {
		_ = w.Close()
		t.Fatal("a failed resume returned a writer")
	}
	if got != nil {
		t.Fatalf("failed resume returned %d entries, want none", len(got))
	}
	if !errors.Is(err, ErrInvalidRecordBoundary) {
		t.Fatalf("resume error = %v, want the earlier corrupt line's ErrInvalidRecordBoundary", err)
	}
	if strings.Contains(err.Error(), "read transcript for resume") {
		t.Fatalf("resume error = %v, want the corrupt line's error rather than the later read failure", err)
	}
}

// TestResumeScanJoinsItsDecodeWorkers pins that no decode is still running
// once the scan returns, on the success path and after a mid-file read failure.
// A pool that abandoned its workers would leak one per failed resume, and a
// resumed daemon's failure path is exactly where that repeats.
//
// The failure case fails the READ, after entry lines have been submitted, and
// opens the file through the fault filesystem so the failing operation is the
// scan's own read rather than the open or the stat before it. A case that was
// refused earlier -- at the header, say -- would leave nothing submitted and so
// could not tell a joined pool from an abandoned one.
func TestResumeScanJoinsItsDecodeWorkers(t *testing.T) {
	// Not parallel: it installs the package-level resumeDecodeSeam.
	var inFlight atomic.Int32
	resumeDecodeSeam = func(entering bool) {
		if entering {
			seamEntries.Add(1)
			inFlight.Add(1)
			return
		}
		inFlight.Add(-1)
	}
	t.Cleanup(func() { resumeDecodeSeam = nil })

	t.Run("clean transcript", func(t *testing.T) {
		path := writeResumeScanTranscript(t, resumeScanBody(64))
		w, got, err := OpenWriterForSession(path, "CORPUS")
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		defer func() { _ = w.Close() }()
		if len(got) != 64 {
			t.Fatalf("resumed %d entries, want 64", len(got))
		}
		if n := inFlight.Load(); n != 0 {
			t.Fatalf("%d decodes still in flight after the resume returned", n)
		}
	})

	t.Run("read fails after entries were submitted", func(t *testing.T) {
		// 512 entries is far more than one 64 KiB read of the buffer, so
		// failing the read after 32 KiB lets a few hundred entries be
		// submitted first and still leaves the scan unfinished.
		body := resumeScanBody(512)
		base := afero.NewMemMapFs()
		if err := base.MkdirAll(filepath.Dir(faultTranscriptPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := afero.WriteFile(base, faultTranscriptPath, []byte(body), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}

		fs := &failingReadFS{Fs: base, path: faultTranscriptPath, remaining: 32 * 1024}
		before := inFlight.Load()
		if before != 0 {
			t.Fatalf("%d decodes in flight before the failed resume", before)
		}
		enteredBefore := seamEntries.Load()
		w, got, err := OpenWriterForSessionWithFS(fs, faultTranscriptPath, "CORPUS")
		if w != nil {
			_ = w.Close()
			t.Fatal("a failed resume returned a writer")
		}
		if got != nil {
			t.Fatalf("failed resume returned %d entries, want none", len(got))
		}
		if err == nil {
			t.Fatal("resume succeeded through a read fault")
		}
		if !strings.Contains(err.Error(), "read transcript for resume") {
			t.Fatalf("resume error = %v, want the scan's read error", err)
		}
		// The point of this case: entry lines really were submitted before the
		// read failed, so a pool left running would be caught below.
		if entered := seamEntries.Load() - enteredBefore; entered == 0 {
			t.Fatal("no decode was submitted before the read failed: this case cannot see an abandoned pool")
		}
		if n := inFlight.Load(); n != 0 {
			t.Fatalf("%d decodes still in flight after a failed resume returned", n)
		}
		// The scan's own read-error path is the one under test, so the pool must
		// be gone once it returns. A pool whose finish was skipped leaves its
		// workers blocked on the jobs channel and its collector on results,
		// forever; those goroutines are what this looks for.
		if leaked := waitForResumeDecoderGoroutines(time.Second); leaked > 0 {
			t.Fatalf("%d resume decode goroutines outlived a failed resume", leaked)
		}
	})

	t.Run("header names another session", func(t *testing.T) {
		path := writeResumeScanTranscript(t, resumeScanBody(64))
		w, got, err := OpenWriterForSession(path, "ANOTHER-SESSION")
		if w != nil {
			_ = w.Close()
			t.Fatal("a failed resume returned a writer")
		}
		if got != nil {
			t.Fatalf("failed resume returned %d entries, want none", len(got))
		}
		if _, ok := errors.AsType[*SessionIDMismatchError](err); !ok {
			t.Fatalf("resume error = %v, want SessionIDMismatchError", err)
		}
		if n := inFlight.Load(); n != 0 {
			t.Fatalf("%d decodes still in flight after a refused resume returned", n)
		}
	})
}

// TestResumeDecodeWorkerCountIsBounded pins the pool's cap against a host
// whose core count would otherwise make the pool unbounded.
func TestResumeDecodeWorkerCountIsBounded(t *testing.T) {
	t.Parallel()
	cases := []struct{ gomaxprocs, want int }{
		{-1, 1},
		{0, 1},
		{1, 1},
		{4, 4},
		{resumeDecodeMaxWorkers - 1, resumeDecodeMaxWorkers - 1},
		{resumeDecodeMaxWorkers, resumeDecodeMaxWorkers},
		{resumeDecodeMaxWorkers * 4, resumeDecodeMaxWorkers},
	}
	for _, tc := range cases {
		if got := resumeDecodeWorkerCount(tc.gomaxprocs); got != tc.want {
			t.Errorf("resumeDecodeWorkerCount(%d) = %d, want %d", tc.gomaxprocs, got, tc.want)
		}
	}
}

// TestResumeDecodeBudgetAdmitsOneOversizedLine pins the escape hatch that
// keeps a single maximum-size record from deadlocking the scan: a line larger
// than the whole in-flight budget proceeds once the budget is otherwise empty.
// A budget that simply refused it would block here forever.
func TestResumeDecodeBudgetAdmitsOneOversizedLine(t *testing.T) {
	t.Parallel()
	budget := newResumeDecodeBudget()
	oversized := int64(resumeDecodeInFlightBytes) * 2
	budget.acquire(oversized)
	if used := budget.used; used != oversized {
		t.Fatalf("budget used = %d, want the oversized line's %d", used, oversized)
	}
	budget.release(oversized)
	if used := budget.used; used != 0 {
		t.Fatalf("budget used = %d after release, want 0", used)
	}
}
