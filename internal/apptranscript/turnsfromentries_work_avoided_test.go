package apptranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// projectionRuns is how many times each form is replayed under
// testing.AllocsPerRun once it is warm.
const projectionRuns = 3

// allocsPerProjection counts the heap allocations one projection form makes
// per run. The runtime's counters are process-global, so the form is run once
// first: whatever a first call allocates and a later one does not — lazily
// built package state, buffers growing to their steady size — lands outside
// the measured window. testing.AllocsPerRun then pins GOMAXPROCS to 1 and
// averages over projectionRuns runs, so an allocation from somewhere else in
// the process moves the average by a fraction of one run rather than by all of
// itself. Elapsed time and allocated bytes come from the warm-up run and are
// for the log only.
func allocsPerProjection(fn func()) (allocs float64, elapsed time.Duration, bytes uint64) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	fn()
	elapsed = time.Since(start)
	runtime.ReadMemStats(&after)
	return testing.AllocsPerRun(projectionRuns, fn), elapsed, after.TotalAlloc - before.TotalAlloc
}

// TestItemTurnsFromEntriesAvoidsFileScanAndDecode pins the entries
// projection's entire reason to exist over a large synthetic transcript: the
// file form scans every line and decodes every entry, the entries form
// projects already-decoded entries and does neither, so the file form must
// pay allocations the entries form never pays. The gate counts those
// allocations rather than comparing elapsed times. An allocation count is the
// work actually avoided and reproduces to two decimal places however busy the
// machine is, whereas a ratio of two short wall-clock measurements compresses
// under load: the entries form is the smaller denominator, so scheduler noise
// there moves the ratio hardest. Elapsed time is logged, never asserted on.
//
// The floor is 2x against roughly 2.8x observed; running the scan or decode
// pass inside the entries form drops the measured ratio to about 1.4x.
func TestItemTurnsFromEntriesAvoidsFileScanAndDecode(t *testing.T) {
	const entryCount = 20000
	path := filepath.Join(t.TempDir(), "large.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_large",
		SystemPrompt: "You are Evener.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// The measurement is the READ side; the fixture write only needs the
	// bytes on disk, so skip the per-append fsync the durability default pays.
	w.SyncInterval = time.Hour
	for i := range entryCount {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("message %d with some body text to make the line realistic", i)))
		turn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		turn.Timestamp = time.Unix(1_700_000_000+int64(i), 0).UTC()
		if err := w.Append(turn); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	project := func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "userMessage", ID: turnID, TurnID: turnID, Text: turn.Message.Content[0].Text}}
	}

	// File form.
	var fileTurns []appwire.Turn
	var fileErr error
	fileAllocs, fileElapsed, fileBytes := allocsPerProjection(func() {
		fileTurns, fileErr = ItemTurnsFromFile(path, 1<<30, project)
	})
	if fileErr != nil {
		t.Fatalf("ItemTurnsFromFile: %v", fileErr)
	}

	// Entries form: decode once the way resume does, then project.
	rw, entries, err := transcript.OpenWriterForSession(path, "th_large")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	_ = rw.Close() //nolint:errcheck // measurement fixture
	var entryTurns []appwire.Turn
	var entriesErr error
	entriesAllocs, entriesElapsed, entriesBytes := allocsPerProjection(func() {
		entryTurns, entriesErr = ItemTurnsFromEntries(rw.Header(), entries, project)
	})
	if entriesErr != nil {
		t.Fatalf("ItemTurnsFromEntries: %v", entriesErr)
	}

	t.Logf("fixture: %d entries, %d bytes (%.1f MB)", entryCount, info.Size(), float64(info.Size())/1024/1024)
	t.Logf("file form (scan+decode+project): %.0f allocs/run over %d runs, warm-up %d bytes in %v, turns=%d", fileAllocs, projectionRuns, fileBytes, fileElapsed, len(fileTurns))
	t.Logf("entries form (project only):     %.0f allocs/run over %d runs, warm-up %d bytes in %v, turns=%d", entriesAllocs, projectionRuns, entriesBytes, entriesElapsed, len(entryTurns))

	ratio := fileAllocs / entriesAllocs
	t.Logf("allocation ratio: %.2fx", ratio)
	if ratio < 2 {
		t.Fatalf("the file form made only %.2fx the allocations of the entries form (file=%.0f, entries=%.0f, over %d entries); the entries projection's skip of the file scan + decode pass is its entire reason to exist", ratio, fileAllocs, entriesAllocs, entryCount)
	}
	if len(fileTurns) != len(entryTurns) {
		t.Fatalf("turn counts diverge: file=%d entries=%d", len(fileTurns), len(entryTurns))
	}
}
