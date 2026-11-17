package jobstore

// Ownership and allocation guards for the event view Store.Load folds from.
//
// A cache miss used to hand Fold a deep clone of the whole journal
// (readAllLocked cloned every event, prefix and tail), and Fold then copied
// that slice again to sort it. The prefix it clones is the store's own cursor,
// which nothing outside the store may reach: Load holds s.mu across the read,
// the fold and the final record copies, and the folded records it returns are
// themselves deep copies (noteFoldedJobsLocked). So the clone bought nothing
// on that path.
//
// These tests pin the boundary from both sides. The allocation guard fails if
// the full-history clone comes back. The ownership cases fail if the fix
// starts letting store-owned events escape, or lets a fold mutate the events
// the cursor keeps for the next load.

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/provenance"
)

// foldInputHistory is the fixture history: one job's start, its terminal
// event, and enough message events that the per-event cost of a whole-history
// clone is a large, unambiguous share of a cache-miss Load.
const foldInputHistory = 4096

// foldInputMessageEvent is a job_message_sent, which IsJobRecordEvent counts
// and the reducer drops. The history therefore folds to exactly one record no
// matter how many of these there are, so fixture correctness does not depend
// on which reducer branch they would take.
func foldInputMessageEvent(i int) Event {
	return Event{
		Kind:   EventJobMessageSent,
		TS:     time.Unix(1700000000, int64(i)).UTC(),
		JobID:  "job_probe",
		Target: "ses_target",
		Action: "note",
		Provenance: &provenance.Causal{
			WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
			Chain:     []provenance.Entry{{Kind: "job", JobID: "job_probe"}},
			FireKeys:  []provenance.FireKey{{WatchID: "w1", WatchGeneration: "g1", DeliveryID: "d1"}},
		},
		StructuredResult: map[string]any{"n": []any{float64(i)}, "s": fmt.Sprintf("result-%d", i)},
	}
}

// foldInputFixture builds a history of one started job followed by message
// events carrying every reference-typed payload an event can hold.
func foldInputFixture() []Event {
	started := time.Unix(1700000000, 0).UTC()
	events := make([]Event, 0, foldInputHistory+1)
	events = append(events, Event{
		Kind:             EventJobStarted,
		TS:               started,
		JobID:            "job_probe",
		Type:             JobShell,
		Command:          "true",
		Task:             "probe the fold input boundary",
		OwnerSessionID:   "ses_owner",
		VisibleToSession: "ses_owner",
		StartedAt:        &started,
		OutputPath:       "/tmp/out",
		Provenance: &provenance.Causal{
			Chain:     []provenance.Entry{{Kind: "job", JobID: "job_probe"}},
			WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
		},
	})
	for i := range foldInputHistory {
		events = append(events, foldInputMessageEvent(i))
	}
	return events
}

// newFoldInputStore opens a store on a fresh log holding the fixture.
func newFoldInputStore(tb testing.TB) *Store {
	tb.Helper()
	s, err := OpenNoSync(filepath.Join(tb.TempDir(), "jobs.jsonl"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	if err := s.AppendBatch(foldInputFixture()); err != nil {
		tb.Fatalf("append fixture: %v", err)
	}
	if _, err := s.Load(); err != nil {
		tb.Fatalf("prime load: %v", err)
	}
	return s
}

// requireFoldInputRecord checks the fixture's own folded state - authored by
// the test, never read back from the store - so a change that makes Load cheap
// by folding nothing fails here rather than passing the budget.
func requireFoldInputRecord(t *testing.T, recs map[string]*JobRecord, when string) {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("%s: Load returned %d records, want exactly the fixture's one job", when, len(recs))
	}
	rec := recs["job_probe"]
	if rec == nil {
		t.Fatalf("%s: Load dropped the fixture's job", when)
	}
	if rec.Command != "true" || rec.Task != "probe the fold input boundary" {
		t.Fatalf("%s: folded start payload = (%q, %q), want the fixture's", when, rec.Command, rec.Task)
	}
	if rec.Status != StatusRunning {
		t.Fatalf("%s: folded status = %q, want running", when, rec.Status)
	}
}

// TestLoadCacheMissDoesNotCloneHistory is the red/green allocation guard. An
// own append clears the fold cache while the cursor still accounts for the
// whole prefix, so the next Load misses and re-folds the entire history. The
// measured quantity is the bytes that Load alone allocates on such a miss.
//
// The append sits OUTSIDE the measured window on purpose. Appending is not what
// this change touches, and a window spanning both would let the append's own
// cursor-slice growth (a history-sized slice per append) dominate the reading:
// the clone being removed is a smaller share than that. Measuring Load alone is
// also the quantity the issue states, "B/op for append+load ... cloned events",
// with the clone isolated from the write.
//
// The budget is a literal between a path that deep-copies the history and one
// that folds a borrowed view, not a value derived from the implementation under
// test. On this fixture the two shapes measured 7,002,608 and 2,242,192 bytes;
// the ceiling sits roughly midway so either side keeps real margin.
//
// Serial: it measures process-global TotalAlloc, so allocations from tests
// running concurrently would land inside its windows.
func TestLoadCacheMissDoesNotCloneHistory(t *testing.T) {
	const budget int64 = 4_000_000

	s := newFoldInputStore(t)
	var quietest int64
	for round := range 3 {
		for i := range 8 {
			if err := s.Append(foldInputMessageEvent(10_000 + round*8 + i)); err != nil {
				t.Fatalf("append: %v", err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			recs, err := s.Load()
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			requireFoldInputRecord(t, recs, "append+load loop")
			if delta := int64(after.TotalAlloc - before.TotalAlloc); quietest == 0 || delta < quietest {
				quietest = delta
			}
		}
	}
	t.Logf("bytes per cache-miss Load: %d, budget %d", quietest, budget)
	if quietest >= budget {
		t.Fatalf("one cache-miss Load allocated %d bytes, budget %d: Load is still deep-copying the history it folds", quietest, budget)
	}
}

// TestLoadCacheMissLeavesCursorEventsIntact pins the obligation the borrowed
// view creates: Fold is handed the store's OWN events now, so anything Fold does
// to its input is done to the events the cursor keeps for every later read.
// Fold copies the slice before sorting for exactly that reason.
//
// The fixture's log holds its events in reverse seq order, which a foreign
// rewrite may legitimately do. Sorting an uncopied input would reorder the
// cursor in place and the store would then disagree with the file it just read,
// while the fold's own result stayed correct - so this compares the store
// against a fresh reopen of the same bytes rather than against Fold's output.
func TestLoadCacheMissLeavesCursorEventsIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	ordered := foldInputFixture()
	reversed := make([]Event, 0, len(ordered))
	for i := 1; i <= len(ordered); i++ {
		e := ordered[i-1]
		e.Seq = int64(len(ordered) + 1 - i)
		reversed = append(reversed, e)
	}
	rewriteLog(t, path, reversed)

	s, err := OpenNoSync(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	requireFoldInputRecord(t, recs, "cache-miss load of an unordered log")

	// The next read comes from the cursor this fold just borrowed. It must be
	// the file's order and the file's fields, not the order Fold sorted into.
	got, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	fresh := freshFullRead(t, path)
	if len(got) != len(fresh) {
		t.Fatalf("store reports %d events, a fresh reread of the same bytes has %d", len(got), len(fresh))
	}
	for i := range fresh {
		if got[i].Seq != fresh[i].Seq {
			t.Fatalf("event %d: store reports seq %d, the file holds %d - the fold reordered the cursor it borrowed", i, got[i].Seq, fresh[i].Seq)
		}
		if got[i].Kind != fresh[i].Kind || got[i].Command != fresh[i].Command {
			t.Fatalf("event %d: store reports (%q,%q), the file holds (%q,%q) - the fold wrote through the cursor it borrowed",
				i, got[i].Kind, got[i].Command, fresh[i].Kind, fresh[i].Command)
		}
	}
}

// TestLoadBorrowedViewIsIndependentAcrossReaders pins that removing Load's
// clone did not loosen any other reader's snapshot. LoadEvents and Load run
// interleaved on one store: each LoadEvents result stays a private copy that a
// caller may mutate (mutateEventDeeply writes through every reference field)
// without changing what a later Load or LoadEvents reports.
func TestLoadBorrowedViewIsIndependentAcrossReaders(t *testing.T) {
	s := newFoldInputStore(t)
	before := freshFullRead(t, s.path)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				// Interleaved readers must not see each other's work.
				if _, err := s.Load(); err != nil {
					t.Errorf("load: %v", err)
					return
				}
				events, err := s.LoadEvents()
				if err != nil {
					t.Errorf("load events: %v", err)
					return
				}
				for i := range events {
					mutateEventDeeply(&events[i])
				}
			}
		}()
	}
	wg.Wait()

	after, err := s.LoadEvents()
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("store reports %d events after concurrent readers, want %d", len(after), len(before))
	}
	for i := range after {
		if after[i].Kind != before[i].Kind || after[i].Seq != before[i].Seq {
			t.Fatalf("event %d changed under concurrent readers: %+v vs %+v", i, after[i], before[i])
		}
		if strings.Contains(fmt.Sprintf("%+v", after[i].StructuredResult), "mutated") {
			t.Fatalf("a reader's mutation of its snapshot reached the store's event %d", i)
		}
	}
	recs, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	requireFoldInputRecord(t, recs, "load after concurrent readers")
}
