package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

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
func TestResumeScanDecodesEntriesConcurrently(t *testing.T) {
	// Not parallel: it installs the package-level resumeDecodeSeam.
	const entries = 200
	path := writeResumeScanTranscript(t, resumeScanBody(entries))

	var inFlight, peak atomic.Int32
	var decodes atomic.Int64
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

// TestResumeScanJoinsItsDecodeWorkers pins that no decode is still running
// once the scan returns, on the success path and after a mid-file read
// failure. A pool that abandoned its workers would leak one per failed resume,
// and a resumed daemon's failure path is exactly where that repeats.
func TestResumeScanJoinsItsDecodeWorkers(t *testing.T) {
	// Not parallel: it installs the package-level resumeDecodeSeam.
	var inFlight atomic.Int32
	resumeDecodeSeam = func(entering bool) {
		if entering {
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
