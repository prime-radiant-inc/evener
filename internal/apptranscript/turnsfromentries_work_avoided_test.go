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
// The gate is the per-entry difference between the two forms, not their ratio.
// The ratio was the wrong expression of the same invariant: it compares the
// entries form against the file form, so making the file form cheaper — the
// point of every allocation fix that touches it — narrows the ratio toward 1x
// and eventually under any fixed floor, without the entries form having
// changed at all. A difference in allocations per entry stays meaningful
// however cheap the file form gets, because it measures the work the entries
// form skips rather than the size of the file form.
//
// Every bound is a deliberate literal measured on this fixture, not a value
// derived from the implementation, and each was checked by mutation: forcing
// the entries form to re-pay the strict decode must fail this test. Healthy
// and mutated runs measure, per entry:
//
//	plain  file pays 8.00 more, mutation pays 1.00 more, entries cost 15.96
//	-race  file pays 21.03 more, mutation pays 4.73 more, entries cost 18.20
//
// Both lanes assert a difference floor and an entries-cost ceiling; see the
// constants below for why each lane needs its own pair.
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
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered}); err != nil {
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
	var fileProj ItemTurnProjection
	var fileErr error
	fileAllocs, fileElapsed, fileBytes := allocsPerProjection(func() {
		fileProj, fileErr = ItemTurnProjectionFromFile(path, 1<<30, project)
	})
	if fileErr != nil {
		t.Fatalf("ItemTurnProjectionFromFile: %v", fileErr)
	}
	fileTurns := fileProj.Turns

	// Entries form: decode once the way resume does, then project.
	rw, entries, err := transcript.OpenWriterForSession(path, "th_large")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	_ = rw.Close() //nolint:errcheck // measurement fixture
	var entryTurns []appwire.Turn
	var entriesErr error
	entriesAllocs, entriesElapsed, entriesBytes := allocsPerProjection(func() {
		entryTurns, entriesErr = itemTurnsFromEntries(rw.Header(), entries, project)
	})
	if entriesErr != nil {
		t.Fatalf("itemTurnsFromEntries: %v", entriesErr)
	}

	filePerEntry := fileAllocs / float64(entryCount)
	entriesPerEntry := entriesAllocs / float64(entryCount)
	avoidedPerEntry := filePerEntry - entriesPerEntry
	avoidedFloor := minFileScanAllocsPerEntry()
	entriesCeiling := maxEntriesAllocsPerEntry()

	t.Logf("fixture: %d entries, %d bytes (%.1f MB)", entryCount, info.Size(), float64(info.Size())/1024/1024)
	t.Logf("file form (scan+decode+project): %.0f allocs/run over %d runs, warm-up %d bytes in %v, turns=%d", fileAllocs, projectionRuns, fileBytes, fileElapsed, len(fileTurns))
	t.Logf("entries form (project only):     %.0f allocs/run over %d runs, warm-up %d bytes in %v, turns=%d", entriesAllocs, projectionRuns, entriesBytes, entriesElapsed, len(entryTurns))
	t.Logf("per entry: file %.2f, entries %.2f, avoided %.2f (floor %.1f, entries ceiling %.1f, race %v)",
		filePerEntry, entriesPerEntry, avoidedPerEntry, avoidedFloor, entriesCeiling, raceDetectorEnabled)

	// The invariant: the file form pays for the scan and decode, and the
	// entries form does not. If the entries form starts paying it too, it
	// overtakes the file form on this fixture and this difference goes negative.
	if avoidedPerEntry < avoidedFloor {
		t.Fatalf("the file form avoided only %.2f allocations per entry against the entries form (file=%.0f, entries=%.0f, over %d entries), want at least %.1f: the entries projection's skip of the file scan + decode pass is its entire reason to exist, so either the entries form started paying that work or the file form stopped paying it",
			avoidedPerEntry, fileAllocs, entriesAllocs, entryCount, avoidedFloor)
	}
	// The difference alone would also pass if both forms grew together, so the
	// entries form's own cost is pinned. It is the cheaper form and the one
	// callers reached for precisely to avoid work; it must not drift up.
	if entriesPerEntry > entriesCeiling {
		t.Fatalf("the entries form allocated %.2f allocations per entry over %d entries, want at most %.1f: projecting already-decoded entries must not pay for decoding or scanning its input",
			entriesPerEntry, entryCount, entriesCeiling)
	}
	if len(fileTurns) != len(entryTurns) {
		t.Fatalf("turn counts diverge: file=%d entries=%d", len(fileTurns), len(entryTurns))
	}
}

const (
	// The two floors are the least extra allocation per entry the file form
	// must pay over the entries form, per lane. Measured: plain 8.00 healthy
	// and 1.00 with the entries form re-paying the decode, -race 21.03 and
	// 4.73. The two lanes need different floors because -race inflates the two
	// forms unevenly — 2.6x on the healthy file form, 4.7x on the mutated one —
	// so a floor sized for the plain margin sits close to the mutated race
	// figure. One floor in the gap between plain-healthy and race-mutated (6.0,
	// say) does pass all four corners, but it clears the nearest one by only
	// about 21 percent, where these two keep better than 2x on every corner.
	//
	// A second regressed state, one that re-runs the whole file form on top of
	// projecting, measures about 16.0 fewer plain, because it then costs more
	// than the file form itself. Both floors reject that one too.
	minFileScanAllocsPerEntryPlain = 4.0
	minFileScanAllocsPerEntryRace  = 10.0

	// The two ceilings bound the entries form's own cost, a quarter above what
	// it measures in that lane: about 16.0 allocations per entry plain, about
	// 18.2 under -race, where the detector's bookkeeping inflates every
	// allocation. They are the counterpart to the difference: a form that got
	// slower on its own would otherwise be hidden by a file form that stayed
	// expensive.
	//
	// This pin, not the difference floor, is what convicts the decode mutation
	// under -race: there the mutation still leaves 4.73 of difference, and it
	// is the entries form's 34.5 allocations per entry against this 23.0 that
	// fails. The floor is a backstop on that lane. Keep both: the pin catches a
	// more expensive entries form, the floor catches a cheaper file form.
	maxEntriesAllocsPerEntryPlain = 20.0
	maxEntriesAllocsPerEntryRace  = 23.0
)

// maxEntriesAllocsPerEntry is this lane's ceiling on the entries form's own
// cost, split by build tag so a -race run is not failed by the detector's
// inflation and the plain run is not loosened to accommodate it.
func maxEntriesAllocsPerEntry() float64 {
	if raceDetectorEnabled {
		return maxEntriesAllocsPerEntryRace
	}
	return maxEntriesAllocsPerEntryPlain
}

// minFileScanAllocsPerEntry is this lane's floor on the work the entries form
// skips, split by build tag for the same reason as the ceiling above.
func minFileScanAllocsPerEntry() float64 {
	if raceDetectorEnabled {
		return minFileScanAllocsPerEntryRace
	}
	return minFileScanAllocsPerEntryPlain
}
