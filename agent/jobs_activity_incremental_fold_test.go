package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/testwait"
)

// This file holds the six crux tests proving job-activity loading is a pure
// incremental fold: 200k events is a normal Tuesday here, truncating
// legitimate history is unacceptable, so reads must cost O(new events) with
// nothing left to truncate (Jesse's ruling). Crux (d) — an advancing
// continuation, two pages, no re-read from byte zero — lives at the hub
// integration level instead: cmd/evener-hub/app_jobs_test.go's
// TestHubJobsListMidListTruncationMintsAdvancingContinuation.

// writeJobLogFast raw-writes n newline-delimited job_started records for
// sessID directly, bypassing jobstore.Store's Append/AppendBatch per-call
// overhead (a stat and a seek per event) — matching the exact on-disk
// format Store.Append produces (plain JSON per line, no header;
// Store assigns Seq sequentially starting at 1, so this does too). Used
// only where a crux test's own scale (200k+ events) makes
// s1cov_writeJobLog's per-event Store.Append noticeably slower for no
// behavioral difference; everything below that scale still goes through
// s1cov_writeJobLog, the existing, Store-verified helper.
func writeJobLogFast(t *testing.T, stateDir, sessID string, n int) string {
	t.Helper()
	return writeShellJobLogFast(t, stateDir, sessID, n, false)
}

func writeActivityJobLogFast(t *testing.T, stateDir, sessID string, n int) string {
	t.Helper()
	return writeShellJobLogFast(t, stateDir, sessID, n, true)
}

func writeShellJobLogFast(t *testing.T, stateDir, sessID string, n int, background bool) string {
	t.Helper()
	dir := jobsDir(stateDir, sessID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jobs.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buffered := bufio.NewWriter(f)
	enc := json.NewEncoder(buffered)
	started := time.Unix(1_600_000_000, 0).UTC()
	for i := range n {
		jobID := fmt.Sprintf("job_%06d", i)
		event := jobstore.Event{
			Kind: jobstore.EventJobStarted, Seq: int64(i + 1), TS: started,
			JobID: jobID, Type: jobstore.JobShell,
			OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started, Background: background,
		}
		if err := enc.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffered.Flush(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadSessionJobActivityTree_TuesdayFullyFoldsAndFullyPaginates is crux
// (a): 200k+ events fold FULLY, with zero truncation at the LOAD layer, and
// paginating all the way through renders every one of them exactly once.
// TestLoadSessionJobActivityTree_BoundsSingleSessionScanAtWorkUnitBudget
// covers the same property at roughly 1% the scale.
func TestLoadSessionJobActivityTree_TuesdayFullyFoldsAndFullyPaginates(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "tuesdayroot"
	totalJobs := 200_000
	if raceDetectorEnabled {
		// Pages cost the page, but writing and decoding 200k events once still
		// takes about 7s of CPU under -race, about 20s of wall time on a
		// developer machine. Three full pages and a partial fourth take every
		// continuation step the walk has, so the race detector sees the same
		// paths, and the uninstrumented run keeps the Tuesday-sized history.
		totalJobs = 3*activityMaxWorkUnits + 1
	}
	jobsPath := writeJobLogFast(t, stateDir, rootID, totalJobs)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	// The LOAD layer itself must fold the complete history — no per-file
	// ceiling degrades or truncates it: historicalJobFoldCache has no size
	// ceiling, only a MaxLineBytes per-line pathology tripwire (see
	// writeJobLogFast, which writes ordinary-sized lines).
	records, _, _, err := loadCachedJobRecords(context.Background(), jobsPath)
	if err != nil {
		t.Fatalf("loadCachedJobRecords: %v", err)
	}
	if records.len() != totalJobs {
		t.Fatalf("loaded %d records, want the complete %d — a ceiling truncated legitimate history", records.len(), totalJobs)
	}

	// PROJECTION still paginates one response at a time (activityMaxWorkUnits
	// per page is a deliberate, separate response-size bound, not a load-time
	// ceiling) — but paginating all the way through must show every job
	// exactly once: no gaps (a job silently dropped), no repeats (a
	// continuation that never advances, looping the same page forever).
	seen := make(map[string]bool, totalJobs)
	params := appwire.JobsListParams{Ref: "local:" + rootID}
	const maxPages = 200 // generous: totalJobs/activityMaxWorkUnits is at most ~100
	pages := 0
	for {
		pages++
		if pages > maxPages {
			t.Fatalf("did not terminate within %d pages (saw %d/%d unique jobs so far)", maxPages, len(seen), totalJobs)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, params)
		if err != nil {
			t.Fatalf("LoadSessionJobActivityTree page %d: %v", pages, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				continue
			}
			if seen[entry.Job.JobID] {
				t.Fatalf("job %q rendered on more than one page (page %d) — the continuation did not advance", entry.Job.JobID, pages)
			}
			seen[entry.Job.JobID] = true
		}
		if !tree.Root.Branch.Truncated {
			break
		}
		if tree.Root.Branch.Continuation == "" {
			t.Fatalf("page %d Truncated=true but Continuation is empty", pages)
		}
		params = appwire.JobsListParams{Ref: "local:" + rootID, Continuation: tree.Root.Branch.Continuation}
	}
	if len(seen) != totalJobs {
		t.Fatalf("saw %d unique jobs across %d pages, want all %d", len(seen), pages, totalJobs)
	}
}

// TestLoadSessionJobActivityTree_PageCostDoesNotGrowWithHistory proves
// that serving one page of an unchanged journal costs the page, not the history
// behind it. The same mid-history page is served from a short history and
// from one several times longer; the runtime's own allocation count (no
// instrumentation the code under test could misreport) must stay flat
// between them. Copying, filtering or re-indexing every record per page
// allocates at least once per record, so the longer history would show tens
// of thousands of extra allocations.
//
// Not parallel: testing.AllocsPerRun counts every allocation in the process.
func TestLoadSessionJobActivityTree_PageCostDoesNotGrowWithHistory(t *testing.T) {
	const shortHistory = 3 * activityMaxWorkUnits
	longHistory := 12 * activityMaxWorkUnits
	if raceDetectorEnabled {
		// Allocation counts are the same under -race; a shorter long history
		// keeps this serial test's one-time write and fold cheap there.
		longHistory = 6 * activityMaxWorkUnits
	}
	short := secondPageAllocations(t, "shorthistory", shortHistory)
	long := secondPageAllocations(t, "longhistory", longHistory)
	t.Logf("page allocations: %.0f with %d jobs, %.0f with %d jobs", short, shortHistory, long, longHistory)
	// One allocation per hundred extra records is far below anything that
	// visits each record, and far above run-to-run noise in a fixed page.
	if growth := long - short; growth > float64(longHistory-shortHistory)/100 {
		t.Fatalf("a page of a %d-job history allocated %.0f times, %.0f more than the same page of a %d-job history: page cost grows with history",
			longHistory, long, growth, shortHistory)
	}
}

// secondPageAllocations writes a totalJobs history for rootID and returns
// the allocations one request for its second page makes once the journal is
// folded.
func secondPageAllocations(t *testing.T, rootID string, totalJobs int) float64 {
	t.Helper()
	stateDir := t.TempDir()
	writeJobLogFast(t, stateDir, rootID, totalJobs)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	ref := "local:" + rootID
	first, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Ref: ref})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !first.Root.Branch.Truncated || first.Root.Branch.Continuation == "" {
		t.Fatalf("first page of %d jobs did not continue", totalJobs)
	}
	params := appwire.JobsListParams{Ref: ref, Continuation: first.Root.Branch.Continuation}
	var pageErr error
	allocs := testing.AllocsPerRun(5, func() {
		if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, params); err != nil {
			pageErr = err
		}
	})
	if pageErr != nil {
		t.Fatalf("second page: %v", pageErr)
	}
	return allocs
}

// TestLoadCachedJobRecords_SecondRequestReadsOnlyTheAppendedDelta is crux
// (b): incrementality proven via an instrumented scan seam, not assumed —
// the second request after an append must read ONLY the delta (a nonzero
// fromOffset, and a decoded count matching exactly what was appended), not
// re-read the file from byte zero.
func TestLoadCachedJobRecords_SecondRequestReadsOnlyTheAppendedDelta(t *testing.T) {
	stateDir := t.TempDir()
	sessID := "deltaroot"
	started := time.Unix(2_000_000_000, 0).UTC()
	s1cov_writeJobLog(t, stateDir, sessID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_a", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_b", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
	)
	jobsPath := filepath.Join(jobsDir(stateDir, sessID), "jobs.jsonl")

	type scanCall struct {
		fromOffset int64
		eventsRead int
	}
	var calls []scanCall
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		events, toOffset, err := original(ctx, path, fromOffset, limits)
		calls = append(calls, scanCall{fromOffset: fromOffset, eventsRead: len(events)})
		return events, toOffset, err
	}
	defer func() { scanJobJournal = original }()

	first, _, _, err := loadCachedJobRecords(context.Background(), jobsPath)
	if err != nil {
		t.Fatalf("first loadCachedJobRecords: %v", err)
	}
	if first.len() != 2 {
		t.Fatalf("first load = %d records, want 2", first.len())
	}
	if len(calls) != 1 || calls[0].fromOffset != 0 || calls[0].eventsRead != 2 {
		t.Fatalf("calls after first load = %+v, want exactly one call reading from offset 0 with 2 events", calls)
	}

	// mtime resolution on some filesystems is coarse (1s); sleep to
	// guarantee the append moves mtime forward so the cache can't mistake
	// this for "nothing changed" and skip the read entirely.
	time.Sleep(1100 * time.Millisecond)
	s1cov_writeJobLog(t, stateDir, sessID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started.Add(time.Second), JobID: "job_c", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
	)

	second, _, _, err := loadCachedJobRecords(context.Background(), jobsPath)
	if err != nil {
		t.Fatalf("second loadCachedJobRecords: %v", err)
	}
	if second.len() != 3 {
		t.Fatalf("second load = %d records, want 3 (2 old + 1 new)", second.len())
	}
	if len(calls) != 2 {
		t.Fatalf("scanJobJournal called %d times total, want exactly 2", len(calls))
	}
	if calls[1].fromOffset == 0 {
		t.Fatalf("second call fromOffset = 0, want nonzero — a full rescan from byte zero is not incremental")
	}
	if calls[1].eventsRead != 1 {
		t.Fatalf("second call read %d events, want exactly 1 (only the newly appended job, not all 3)", calls[1].eventsRead)
	}
}

// TestLoadCachedJobRecords_RewrittenJournalForcesFullRescanWithCorrectResult
// is crux (c): a rewritten/shrunk journal forces a full rescan and produces
// the correct result for the NEW content — not a stale view, and not a
// broken merge of old and new.
func TestLoadCachedJobRecords_RewrittenJournalForcesFullRescanWithCorrectResult(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessID := "rewriteroot"
	started := time.Unix(3_000_000_000, 0).UTC()
	s1cov_writeJobLog(t, stateDir, sessID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_old_a", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_old_b", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_old_c", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started},
	)
	jobsPath := filepath.Join(jobsDir(stateDir, sessID), "jobs.jsonl")

	first, firstEpoch, _, err := loadCachedJobRecords(context.Background(), jobsPath)
	if err != nil {
		t.Fatalf("first loadCachedJobRecords: %v", err)
	}
	if first.len() != 3 {
		t.Fatalf("first load = %d records, want 3", first.len())
	}

	time.Sleep(1100 * time.Millisecond)
	// A genuine rewrite: remove and recreate rather than append, so this is
	// not the same Store instance's own further writes — a repair tool or a
	// different process replacing the file entirely.
	if err := os.Remove(jobsPath); err != nil {
		t.Fatal(err)
	}
	newStarted := started.Add(time.Hour)
	s1cov_writeJobLog(t, stateDir, sessID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: newStarted, JobID: "job_new_x", Type: jobstore.JobShell, OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &newStarted},
	)

	second, secondEpoch, _, err := loadCachedJobRecords(context.Background(), jobsPath)
	if err != nil {
		t.Fatalf("second loadCachedJobRecords: %v", err)
	}
	if !reflect.DeepEqual(activityRecordIDs(slices.Collect(second.all())), []string{"job_new_x"}) {
		t.Fatalf("second load = %v, want exactly the 1 new job and none of the 3 old ones", activityRecordIDs(slices.Collect(second.all())))
	}
	if secondEpoch == firstEpoch {
		t.Fatalf("epoch unchanged (%d) across a rewrite, want it bumped so an in-flight continuation is detectably stale", firstEpoch)
	}
}

// TestLoadSessionJobActivityTree_PathologicalLineErrorsLoudlyNotSilently is
// crux (e): a pathological single line still errors loudly with the
// diagnostic, propagated all the way through LoadSessionJobActivityTree —
// never silently caught, degraded to an empty tree, or otherwise swallowed
// anywhere between the scanner and the caller. MaxLineBytes' own
// enforcement (does it actually refuse an oversized line, does it drain the
// reader correctly) is already covered at the jobstore package level
// (TestScanEvents_RefusesSingleOversizedUnterminatedLine et al.); this test
// covers the property those can't: that the error reaches all the way up.
func TestLoadSessionJobActivityTree_PathologicalLineErrorsLoudlyNotSilently(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "pathologicalroot"
	dir := jobsDir(stateDir, rootID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jobs.jsonl")
	oversized := `{"kind":"job_started","seq":1,"job_id":"` + strings.Repeat("x", 5000) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	savePastActivityMeta(t, stateDir, rootID, "Root")

	// Inject a small MaxLineBytes so this test's fixture doesn't need to be
	// an actual 128 MiB line to trip jobstore's package default — this test
	// is about the PROPAGATION property, not re-proving the cap fires (that
	// is what the jobstore-level tests already do).
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		limits.MaxLineBytes = 100
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	_, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err == nil {
		t.Fatal("expected an error, got nil — a pathological line must never silently degrade to an empty or partial success")
	}
	if !errors.Is(err, jobstore.ErrLineTooLong) {
		t.Fatalf("error = %v, want ErrLineTooLong", err)
	}
}

// TestLoadCachedJobRecords_ConcurrentRequestsShareOneFullScan is crux (f):
// concurrent requests for the same journal, race-detector clean, must
// coalesce onto one full scan, not N redundant ones.
func TestLoadCachedJobRecords_ConcurrentRequestsShareOneFullScan(t *testing.T) {
	stateDir := t.TempDir()
	sessID := "raceroot"
	started := time.Unix(4_000_000_000, 0).UTC()
	events := make([]jobstore.Event, 0, 50)
	for i := range 50 {
		jobID := fmt.Sprintf("job_race_%d", i)
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
			OwnerSessionID: sessID, VisibleToSession: sessID, StartedAt: &started,
		})
	}
	s1cov_writeJobLog(t, stateDir, sessID, events...)
	jobsPath := filepath.Join(jobsDir(stateDir, sessID), "jobs.jsonl")

	var scanCalls atomic.Int32
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scanCalls.Add(1)
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	const n = 20
	var wg sync.WaitGroup
	results := make([]*activityJobList, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, _, errs[i] = loadCachedJobRecords(context.Background(), jobsPath)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("loadCachedJobRecords[%d]: %v", i, err)
		}
		if results[i].len() != 50 {
			t.Fatalf("results[%d] = %d records, want 50", i, results[i].len())
		}
	}
	if got := scanCalls.Load(); got != 1 {
		t.Fatalf("scanJobJournal called %d times for %d concurrent requests on the same journal, want exactly 1", got, n)
	}
}

// liveActivityScanCalls records one journal scan the live path issued.
type liveActivityScanCall struct {
	fromOffset int64
	endOffset  int64
	eventsRead int
}

// spyLiveActivityScans swaps scanJobJournal for the duration of a test and
// returns the calls it observed. A test that swaps the package variable must
// not run parallel with another that does.
func spyLiveActivityScans(t *testing.T) *[]liveActivityScanCall {
	t.Helper()
	calls := new([]liveActivityScanCall)
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		events, toOffset, err := original(ctx, path, fromOffset, limits)
		*calls = append(*calls, liveActivityScanCall{fromOffset: fromOffset, endOffset: limits.EndOffset, eventsRead: len(events)})
		return events, toOffset, err
	}
	t.Cleanup(func() { scanJobJournal = original })
	return calls
}

// seedLiveShellStarts appends n shell starts owned by s to its own journal.
func seedLiveShellStarts(t *testing.T, s *Session, n int) {
	t.Helper()
	started := time.Unix(1_600_000_000, 0).UTC()
	seed := make([]jobstore.Event, 0, n)
	for i := range n {
		seed = append(seed, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started,
			JobID: fmt.Sprintf("job_live_%06d", i), Type: jobstore.JobShell,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
		})
	}
	// Pin the journal's modification time BEFORE the seeding append, not after
	// it.
	//
	// The store decides whether its own appends are distinguishable by length
	// and mtime, and it decides that ON the append: an append that changes the
	// length while the filesystem reports the same mtime proves the timestamps
	// here cannot resolve a write, and the store gives metadata-based reuse up
	// for good. On a coarse clock the journal's creation and the seed append can
	// land in one tick, which disables it before any caller can pin anything —
	// and the flag is sticky, so a later pin cannot restore what the append
	// already discarded.
	//
	// Moving the mtime into the past first makes the append's own write
	// unambiguously later on any clock, so the assertion the callers make —
	// "a later append folds only the delta" — measures the delta path rather
	// than the host's timestamp resolution.
	// The journal's location comes from the store's own boundary read, which
	// reports where it is without taking a cursor sample.
	located, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("locate the journal before seeding: %v", err)
	}
	if info, err := os.Stat(located.Path); err == nil {
		pinned := info.ModTime().Add(-time.Hour)
		if err := os.Chtimes(located.Path, pinned, pinned); err != nil {
			t.Fatalf("pin journal mtime before seeding: %v", err)
		}
	}
	if err := s.jobManager.store.AppendBatch(seed); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	// Pin AGAIN after the seed, and re-book the store's baseline against the
	// pinned sample, so the CALLER's later append is unambiguously later too.
	//
	// The pin above only covers the seed append. The tests that use this helper
	// then append one more event and assert that the following read folds only
	// the delta — and that assertion needs the same property for THEIR append.
	// Without this second pin, both appends can land inside one coarse-clock
	// tick, where the length moves and the modification time does not. The store
	// reads exactly that shape as proof the filesystem cannot resolve a write
	// (noteUnresolvableTimestampLocked), records it permanently, and the next
	// read refolds the whole journal rather than extending it. That is the
	// conservative answer to a real observation, so the fixture must not
	// manufacture it: the assertion is about the delta path, not about whether
	// this host's clock advanced between two writes.
	//
	// Re-booking is the half that makes the pin visible to the store: the
	// boundary read books the pinned sample as the writer's own baseline, so the
	// caller's append grows FROM it instead of from the seed's post-sync time.
	if located.Path != "" {
		if info, err := os.Stat(located.Path); err == nil {
			pinned := info.ModTime().Add(-time.Hour)
			if err := os.Chtimes(located.Path, pinned, pinned); err != nil {
				t.Fatalf("pin journal mtime after seeding: %v", err)
			}
			if _, err := s.jobManager.store.CommittedJournal(context.Background()); err != nil {
				t.Fatalf("re-book the pinned journal baseline: %v", err)
			}
		}
	}
}

// TestLiveActivityBase_ReadsOnlyTheAppendedDelta pins the live path's read cost:
// a live session's own job list is folded incrementally, so the first read scans
// the journal once and every later read costs what was appended since — not a
// fresh clone and fold of the whole history, which is what Store.Load does per
// call. The row count proves the fold is still COMPLETE: an incremental read is
// not a truncated one.
func TestLiveActivityBase_ReadsOnlyTheAppendedDelta(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	const total = 3 * activityMaxWorkUnits
	seedLiveShellStarts(t, s, total)
	calls := spyLiveActivityScans(t)

	first, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("first loadLiveActivityBase: %v", err)
	}
	if got := first.snapshot.Jobs.len(); got != total {
		t.Fatalf("first load = %d records, want the complete %d", got, total)
	}
	if len(*calls) != 1 || (*calls)[0].fromOffset != 0 || (*calls)[0].eventsRead != total {
		t.Fatalf("calls after first load = %+v, want exactly one scan from offset 0 reading %d events", *calls, total)
	}

	// Nothing appended: the fold is served without reading the journal at all.
	*calls = nil
	second, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("second loadLiveActivityBase: %v", err)
	}
	if got := second.snapshot.Jobs.len(); got != total {
		t.Fatalf("second load = %d records, want %d", got, total)
	}
	if len(*calls) != 0 {
		t.Fatalf("an unchanged journal was scanned %d times, want 0: %+v", len(*calls), *calls)
	}

	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Unix(1_600_000_001, 0).UTC(),
		JobID: "job_live_appended", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	*calls = nil
	third, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("third loadLiveActivityBase: %v", err)
	}
	if got := third.snapshot.Jobs.len(); got != total+1 {
		t.Fatalf("third load = %d records, want %d", got, total+1)
	}
	if len(*calls) != 1 || (*calls)[0].fromOffset == 0 || (*calls)[0].eventsRead != 1 {
		t.Fatalf("calls after appending one event = %+v, want one scan resuming past byte 0 and reading exactly 1 event", *calls)
	}
}

// TestLiveActivityBase_FoldsOnlyCommittedBytes pins that the live fold reads no
// further than the boundary the store reports, which is the length the store
// acknowledged. Append holds the store's lock across its write, its fsync and
// its rollback, so a reader that respects that length can never fold a start
// whose append is still in flight — and one whose fsync then rolls it back
// leaves no row behind and emits no revision to invalidate a continuation that
// counted one.
//
// The scan itself is what enforces that, so this test also drives it with a
// journal that holds a COMPLETE record past a positive boundary. Checking only
// the offset handed to the scanner cannot distinguish "the fold stops at the
// boundary" from "the fold reads the file and trusts the offset", and only the
// second shape fails when a whole record sits beyond the sampled length: it is
// decodable, so nothing about decoding withholds it. Here the record beyond the
// boundary must be absent, and must appear exactly once when the boundary
// advances past it.
func TestLiveActivityBase_FoldsOnlyCommittedBytes(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)

	calls := spyLiveActivityScans(t)
	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase: %v", err)
	}
	if got := base.snapshot.Jobs.len(); got != 3 {
		t.Fatalf("load = %d records, want 3", got)
	}
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	committed := snapshot.Size
	if len(*calls) != 1 {
		t.Fatalf("scans = %d, want 1", len(*calls))
	}
	if (*calls)[0].endOffset != committed {
		t.Fatalf("scan endOffset = %d, want the store's committed length %d", (*calls)[0].endOffset, committed)
	}

	// A boundary that has not moved folds nothing, so an in-flight append
	// cannot be observed: the page keeps exactly the rows it already had.
	*calls = nil
	again, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase at an unchanged boundary: %v", err)
	}
	if got := again.snapshot.Jobs.len(); got != 3 {
		t.Fatalf("load = %d records at an unchanged boundary, want the same 3", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("an unchanged committed boundary scanned the journal %d times, want 0: %+v", len(*calls), *calls)
	}

	// A committed append moves the boundary, and only the delta is read.
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Unix(1_600_000_002, 0).UTC(),
		JobID: "job_committed", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	movedSnapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after append: %v", err)
	}
	moved := movedSnapshot.Size
	if moved <= committed {
		t.Fatalf("committed length = %d after an append, want it past the previous %d", moved, committed)
	}
	*calls = nil
	after, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase after a committed append: %v", err)
	}
	if got := after.snapshot.Jobs.len(); got != 4 {
		t.Fatalf("load = %d records after a committed append, want 4", got)
	}
	if len(*calls) != 1 || (*calls)[0].fromOffset == 0 || (*calls)[0].eventsRead != 1 || (*calls)[0].endOffset != moved {
		t.Fatalf("scan after a committed append = %+v, want one delta scan to the new boundary %d", *calls, moved)
	}

	// Now the shape the offset check alone cannot catch: a COMPLETE record past a
	// positive boundary. The fold is driven with an explicit committed length that
	// stops after the first record, while the file holds all of them, so nothing
	// about decoding withholds the rest — only respecting the boundary does. That
	// is the append-in-flight case exactly: the bytes are durable and readable,
	// and the store has not acknowledged them.
	final, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal before the boundary check: %v", err)
	}
	raw, err := os.ReadFile(final.Path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	firstEnd := bytes.IndexByte(raw, '\n')
	if firstEnd < 0 || firstEnd+1 >= len(raw) {
		t.Fatalf("fixture journal holds no record beyond the first line")
	}
	short := int64(firstEnd + 1)
	at := func(size int64) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{Path: final.Path, Size: size, Generation: final.Generation, MetadataResolvesWrites: final.MetadataResolvesWrites}
	}
	var fold liveJobFold
	capped, err := fold.records(context.Background(), final.Path, at(short), nil)
	if err != nil {
		t.Fatalf("records at a boundary inside the journal: %v", err)
	}
	admittedIDs := activityRecordIDs(slices.Collect(capped.jobs.all()))
	for _, id := range admittedIDs {
		if id != "job_live_000000" {
			t.Fatalf("a fold bounded at byte %d named %q, whose record starts after it: %v", short, id, admittedIDs)
		}
	}
	if len(admittedIDs) != 1 {
		t.Fatalf("a fold bounded at byte %d named %d records, want the 1 whose record ends there: %v", short, len(admittedIDs), admittedIDs)
	}

	// Advance the boundary past every record and the rows appear, each exactly
	// once: the delta the fold withheld is read now, and nothing is counted twice.
	full, err := fold.records(context.Background(), final.Path, at(final.Size), nil)
	if err != nil {
		t.Fatalf("records at the full boundary: %v", err)
	}
	for rec := range full.jobs.all() {
		if got := countJobNamed(full.jobs, rec.JobID); got != 1 {
			t.Fatalf("%q appears %d times after the boundary advanced past it, want exactly 1", rec.JobID, got)
		}
	}
	if got := full.jobs.len(); got != 4 {
		t.Fatalf("the advanced fold = %d records, want the 4 the journal holds", got)
	}
}

// countJobNamed reports how many rows in list name jobID. It exists so an
// assertion about a row appearing "exactly once" counts rows rather than
// matching a rendered list, which is why the two are not the same question.
func countJobNamed(list *activityJobList, jobID string) int {
	if list == nil {
		return 0
	}
	count := 0
	for rec := range list.all() {
		if rec.JobID == jobID {
			count++
		}
	}
	return count
}

// TestLiveActivityBase_LastWaiterLeavingStopsTheScan pins the fold's lifetime:
// a caller that abandons a cold read must stop its scan once no other waiter
// remains, rather than paying the whole journal's I/O and allocation for a
// result nobody will read. The scan observes its own cancellation, which is the
// only way it can report that it stopped; a foldcache-style detached flight
// would read the journal to the end instead.
func TestLiveActivityBase_LastWaiterLeavingStopsTheScan(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)

	// Only the first scan blocks; every later one reads normally, so the
	// assertion after the abandonment is about a fresh scan finishing, not
	// about this fixture releasing it.
	var scans atomic.Int32
	entered := make(chan struct{}, 1)
	stopped := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		if scans.Add(1) == 1 {
			entered <- struct{}{}
			<-ctx.Done()
			close(stopped)
			return nil, 0, ctx.Err()
		}
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	// One caller on a cold fold: it blocks inside the scan until that scan's
	// own context is canceled, which only the last waiter leaving does.
	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(ctx, s)
		abandoned <- err
	}()
	<-entered
	cancel()

	if err := <-abandoned; !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned load = %v, want context.Canceled", err)
	}
	// The scan itself stopped, not merely the RPC that was waiting on it.
	<-stopped

	// The abandoned scan is withdrawn, so this read starts a fresh one and
	// succeeds rather than joining a torn-down scan and inheriting its
	// cancellation.
	after, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after abandonment: %v", err)
	}
	if got := after.snapshot.Jobs.len(); got != 3 {
		t.Fatalf("load after abandonment = %d records, want the 3 committed ones", got)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("scans = %d, want the abandoned one plus one fresh scan", got)
	}
}

// TestLiveActivityBase_AbandonedScanStaysRegisteredUntilItExits pins the
// ownership rule that makes the fold safe: while an extension is still running
// it owns the position map, so the fold must not offer a second extension over
// the same base. The abandoned scan therefore keeps its registration until its
// goroutine exits, publishes nothing, and the next caller waits for that exit
// and retries rather than extending beside it.
func TestLiveActivityBase_AbandonedScanStaysRegisteredUntilItExits(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)

	// The first scan blocks until the test lets it go, and then finishes as if
	// its cancellation had not arrived yet: the point is the window between
	// the last waiter leaving and the goroutine actually exiting.
	var scans atomic.Int32
	entered := make(chan struct{}, 1)
	finish := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		if scans.Add(1) == 1 {
			entered <- struct{}{}
			<-finish
		}
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(ctx, s)
		abandoned <- err
	}()
	<-entered
	cancel()
	if err := <-abandoned; !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned load = %v, want context.Canceled", err)
	}

	// The goroutine has not exited yet, so the scan is still the registered
	// extension of this fold.
	f := &s.jobManager.liveActivityFold
	f.mu.Lock()
	registered := f.scanning != nil
	f.mu.Unlock()
	if !registered {
		t.Fatal("the abandoned scan was deregistered while its goroutine was still running")
	}

	// A caller arriving now waits for that exit instead of extending beside
	// it, then starts its own scan once the fold is free.
	next := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(context.Background(), s)
		next <- err
	}()
	// The next caller cannot have started a scan while the first is
	// registered, which is what keeps two extensions off the position map:
	// it has registered as a waiter on that same scan instead.
	testwait.Until(t, "the second caller to wait on the abandoned scan", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.scanning != nil && f.scanning.waiters == 1 && f.scanning.abandoned
	})
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans = %d while the first was still running, want 1", got)
	}

	close(finish)
	if err := <-next; err != nil {
		t.Fatalf("load after the abandoned scan exited: %v", err)
	}

	// The abandoned extend published nothing, so the retry read the journal
	// itself rather than resuming from a base the canceled read half-built.
	calls := spyLiveActivityScans(t)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("the fold was rescanned %d times after the retry completed, want 0: %+v", len(*calls), *calls)
	}
}

// TestLiveActivityBase_ExtendsToTheCallersOwnBoundary pins the other half of
// sharing: a caller may join a scan that started before an append its own
// boundary includes. That scan's fold is short of what the caller asked for, so
// the caller's read must extend the remaining delta before returning. Serving
// the short fold under the caller's newer revision is what lets a continuation
// skip the job it never saw — or repeat a delegate, once the missing row shifts
// the paging positions.
//
// The journal starts EMPTY, so the shared scan reaches byte zero and folds
// nothing: zero byte progress, from a boundary that is not this caller's. "The
// scan stopped here" is not "there is nothing beyond here", and this fixture is
// the case that tells them apart.
func TestLiveActivityBase_ExtendsToTheCallersOwnBoundary(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}

	// The first scan blocks after its boundary (zero, on an empty journal) is
	// fixed, which is the window the first append lands in.
	var scans atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scans.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	first := make(chan *activityLoadedBase, 1)
	go func() {
		base, err := loadLiveActivityBase(context.Background(), s)
		if err != nil {
			first <- nil
			return
		}
		first <- &base
	}()
	<-entered

	// The session's first shell job commits while that scan is still in flight,
	// so the second caller samples a boundary the running scan's own does not
	// reach — and the running scan will fold nothing at all.
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Unix(1_600_000_001, 0).UTC(),
		JobID: "job_first", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	second := make(chan *activityLoadedBase, 1)
	go func() {
		base, err := loadLiveActivityBase(context.Background(), s)
		if err != nil {
			second <- nil
			return
		}
		second <- &base
	}()
	// The second caller joins the running scan rather than starting its own.
	testwait.Until(t, "the second caller to join the running scan", func() bool {
		fold := &s.jobManager.liveActivityFold
		fold.mu.Lock()
		defer fold.mu.Unlock()
		return fold.scanning != nil && fold.scanning.waiters == 2
	})

	close(release)
	// The first caller sampled an empty boundary before the append, so its own
	// page need not carry the job.
	if base := <-first; base == nil {
		t.Fatal("first load failed")
	}
	// The second sampled a boundary that includes the append, so its page must
	// carry the job even though the scan it joined folded nothing.
	base := <-second
	if base == nil {
		t.Fatal("second load failed")
	}
	if got := base.snapshot.Jobs.len(); got != 1 {
		t.Fatalf("second load = %d records, want the 1 job its boundary covers", got)
	}
	for rec := range base.snapshot.Jobs.all() {
		if rec.JobID != "job_first" {
			t.Fatalf("second load carried %q, want job_first", rec.JobID)
		}
	}
	// One shared scan, then the delta the second caller was still missing.
	if got := scans.Load(); got != 2 {
		t.Fatalf("scans = %d, want the shared scan plus the extension to the later boundary", got)
	}
}

// TestLiveActivityBase_CanceledRebuildLeavesTheFoldInvalid pins the reason the
// fold's cache key is a generation rather than a "this shrank" flag. A
// truncation invalidates the fold, but the request that notices it can be
// canceled before its rebuild publishes anything. A flag is consumed by that
// first sight: every later request compares against a store that has already
// recorded the shorter length, sees "no shrink", and serves the fold holding
// rows the file no longer has. A generation cannot be consumed — the store
// keeps reporting the new one until a fold is actually built for it.
func TestLiveActivityBase_CanceledRebuildLeavesTheFoldInvalid(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Truncate to the first record behind the store's back.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	raw := mustReadCurrentJournal(t, snapshot.Path)
	firstLine := bytes.IndexByte(raw, '\n') + 1
	if firstLine <= 0 {
		t.Fatal("journal has no first line")
	}
	if err := os.Truncate(snapshot.Path, int64(firstLine)); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// The rebuild starts and is then abandoned: the caller that noticed the
	// truncation goes away before the new fold is published.
	entered := make(chan struct{}, 1)
	finish := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(scanCtx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		select {
		case entered <- struct{}{}:
			<-finish
		default:
		}
		return original(scanCtx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(ctx, s)
		abandoned <- err
	}()
	<-entered
	cancel()
	if err := <-abandoned; !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned load = %v, want context.Canceled", err)
	}
	close(finish)
	f := &s.jobManager.liveActivityFold
	testwait.Until(t, "the abandoned rebuild to exit", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.scanning == nil
	})

	// The truncated journal still holds one record. The next reader must fold
	// it, not serve the three-row fold the canceled rebuild left behind.
	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the canceled rebuild: %v", err)
	}
	if got := base.snapshot.Jobs.len(); got != 1 {
		t.Fatalf("load after the canceled rebuild = %d records, want the 1 the file still has", got)
	}
}

// TestLiveActivityBase_RewritesInPlaceInvalidateTheFold pins the other half of
// the generation: a journal rewritten to the SAME length is still a different
// file. The store cannot see that in a length, so it compares the modification
// time it last accounted for and moves the generation when the file changed
// without growing. Without that, a fold keeps serving rows the rewrite removed
// and the page reports jobs that are no longer in the journal.
func TestLiveActivityBase_RewritesInPlaceInvalidateTheFold(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}

	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	raw := mustReadCurrentJournal(t, snapshot.Path)
	// Same length, different content: the last job is renamed in place.
	was := []byte("job_live_000002")
	now := []byte("job_live_999999")
	if len(was) != len(now) || !bytes.Contains(raw, was) {
		t.Fatalf("fixture does not contain %q", was)
	}
	rewritten := bytes.ReplaceAll(raw, was, now)
	if len(rewritten) != len(raw) {
		t.Fatalf("rewrite changed the journal length from %d to %d", len(raw), len(rewritten))
	}
	f, err := os.OpenFile(snapshot.Path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open for rewrite: %v", err)
	}
	if _, err := f.WriteAt(rewritten, 0); err != nil {
		_ = f.Close()
		t.Fatalf("rewrite in place: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after rewrite: %v", err)
	}
	// A foreign writer leaves the file looking touched; say so explicitly so
	// the fixture does not depend on how many nanoseconds an append took.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(snapshot.Path, later, later); err != nil {
		t.Fatalf("touch rewritten journal: %v", err)
	}

	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after an in-place rewrite: %v", err)
	}
	var ids []string
	for rec := range base.snapshot.Jobs.records(base.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if len(ids) != 3 {
		t.Fatalf("load after a same-length rewrite = %d records, want the 3 the file has", len(ids))
	}
	if slices.Contains(ids, "job_live_000002") {
		t.Fatalf("load after a same-length rewrite still reports the replaced id: %v", ids)
	}
	if !slices.Contains(ids, "job_live_999999") {
		t.Fatalf("load after a same-length rewrite does not report the new id: %v", ids)
	}

	// The ordering the reader cannot see for itself: the boundary is sampled,
	// and THEN a same-length rewrite replaces the prefix the fold already holds
	// while an append lands past it. A scan started on that sample composes the
	// old prefix with the rewritten suffix and publishes a list that never
	// existed as one file. Rewriting from inside the scan is what puts the change
	// strictly between the boundary read and the bytes read, which is the window
	// the store's generation has to be consulted about.
	//
	// The append below is what makes the load scan at all: with the fold already
	// at the committed boundary there is nothing to read, and no scan window to
	// race.
	delta := time.Unix(1_600_000_400, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: delta, JobID: "job_before_rewrite", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &delta,
	}); err != nil {
		t.Fatalf("append before the rewrite: %v", err)
	}
	original := scanJobJournal
	var rewrote sync.Once
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		rewrote.Do(func() {
			raw := mustReadCurrentJournal(t, path)
			wasFirst := []byte("job_live_000000")
			nowFirst := []byte("job_live_888888")
			if len(wasFirst) != len(nowFirst) || !bytes.Contains(raw, wasFirst) {
				t.Errorf("fixture does not contain %q", wasFirst)
				return
			}
			rewritten := append(bytes.ReplaceAll(raw, wasFirst, nowFirst),
				[]byte(`{"kind":"job_started","seq":900,"ts":"2026-01-01T00:00:00Z","job_id":"job_after_rewrite","type":"shell"}`+"\n")...)
			if err := os.WriteFile(path, rewritten, 0o644); err != nil {
				t.Errorf("rewrite and append: %v", err)
				return
			}
			if err := os.Chtimes(path, later.Add(time.Minute), later.Add(time.Minute)); err != nil {
				t.Errorf("touch rewritten journal: %v", err)
			}
		})
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })
	stalled, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after a rewrite and append: %v", err)
	}
	var stalledIDs []string
	for rec := range stalled.snapshot.Jobs.records(stalled.snapshot.SessionID, activityShellJob, 0) {
		stalledIDs = append(stalledIDs, rec.JobID)
	}
	if slices.Contains(stalledIDs, "job_live_000000") {
		t.Fatalf("load after a rewrite and append still reports the replaced id: %v", stalledIDs)
	}
	if !slices.Contains(stalledIDs, "job_live_888888") {
		t.Fatalf("load after a rewrite and append does not report the rewritten id: %v", stalledIDs)
	}
	if !slices.Contains(stalledIDs, "job_after_rewrite") {
		t.Fatalf("load after a rewrite and append does not report the appended id: %v", stalledIDs)
	}
}

// TestLiveActivityBase_TruncationRenumbersAPagePinsTheEpoch pins why the live
// fold's epoch belongs in the snapshot. Truncating a journal removes rows, and
// the rows after the cut move toward the front, so every position a page
// counted past the cut now names a different job. The live tree's revision does
// not move for that — no job started or finished — so a continuation minted
// before the truncation would resume at an index into a list that renumbered
// under it and skip the jobs the cut pulled forward.
//
// A published rebuild therefore advances the epoch, and the snapshot carries it
// so the mint records it. The proof here is the pair: a token holding the
// pre-truncation epoch is refused, and one holding the rebuilt epoch resumes
// against the SAME revision. Only the epoch differs between them, so the epoch
// is what decides.
func TestLiveActivityBase_TruncationRenumbersAPagePinsTheEpoch(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	before, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if got := before.snapshot.JobsEpoch; got != 0 {
		t.Fatalf("first fold reports epoch %d, want 0: nothing was rebuilt yet", got)
	}

	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	raw := mustReadCurrentJournal(t, snapshot.Path)
	firstLine := bytes.IndexByte(raw, '\n') + 1
	if firstLine <= 0 {
		t.Fatal("journal has no first line")
	}
	if err := os.Truncate(snapshot.Path, int64(firstLine)); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	after, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if got := after.snapshot.Jobs.len(); got != 1 {
		t.Fatalf("load after truncation = %d records, want the 1 the file still has", got)
	}
	if after.snapshot.JobsEpoch == before.snapshot.JobsEpoch {
		t.Fatalf("epoch = %d after a rebuild that removed rows, want an advance from %d", after.snapshot.JobsEpoch, before.snapshot.JobsEpoch)
	}

	// Resuming is fenced on two things: the epochs and, for a live root, the
	// clock's revision. Both tokens below carry the CURRENT revision, so only
	// the epoch separates them.
	revision := activityCurrentRootRevision(s.jobActivityClock)
	mint := func(jobsEpoch uint64) string {
		return encodeActivityContinuation(activityContinuation{
			Version:   activityContinuationVersion,
			RootID:    s.ID(),
			SessionID: s.ID(),
			Revision:  revision,
			JobsEpoch: jobsEpoch,
		})
	}
	beforeTruncation := mint(before.snapshot.JobsEpoch)
	afterTruncation := mint(after.snapshot.JobsEpoch)
	if beforeTruncation == afterTruncation {
		t.Fatal("the two tokens are identical, so the fixture never moved the epoch")
	}
	if _, err := s.JobActivityTree(t.Context(), appwire.JobsListParams{Continuation: beforeTruncation}); err == nil {
		t.Fatal("a continuation minted before the truncation was accepted; its ResumeIndex now names jobs that moved")
	}
	resumed, err := s.JobActivityTree(t.Context(), appwire.JobsListParams{Continuation: afterTruncation})
	if err != nil {
		t.Fatalf("a continuation minted after the rebuild was refused: %v", err)
	}
	// Resuming at index 0 renders the session it names, so the branch must not
	// hand back another continuation over the same list.
	if got := resumed.Root.Branch; got.Error != "" || got.Continuation != "" {
		t.Fatalf("the resumed tree branch = %+v, want the session rendered", got)
	}
}

// TestLiveActivityBase_KeepsARunningJobWhoseStartWasTruncatedAway pins the
// visibility half of a rebuild, which is not a generation question at all.
// Truncating a journal past a still-running shell job's start removes that
// job's durable row, but the job did not stop: it is in the manager's live
// snapshot and in the manager's own listing. An overlay that only substitutes
// existing rows would drop it from activity, so the page would report a live
// job as gone. The stranded live record is appended after the durable rows and
// counted, so it appears and pages like any other running job.
func TestLiveActivityBase_KeepsARunningJobWhoseStartWasTruncatedAway(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 2)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// A third shell job starts and stays running: its start record is durable,
	// and the manager holds it as a live job.
	const runningID = "job_still_running"
	started := time.Unix(1_600_000_500, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: runningID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}); err != nil {
		t.Fatalf("append running job: %v", err)
	}
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID: runningID, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: started,
		},
		done:           make(chan struct{}),
		signal:         func() {},
		durableStarted: true,
	}
	s.jobManager.mu.Lock()
	s.jobManager.running[runningID] = run
	s.jobManager.mu.Unlock()
	t.Cleanup(func() {
		s.jobManager.mu.Lock()
		delete(s.jobManager.running, runningID)
		s.jobManager.mu.Unlock()
	})
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("load with the running job staged: %v", err)
	}

	// Truncate the journal past everything, which removes the running job's own
	// durable start record as well as the two earlier jobs.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if err := os.Truncate(snapshot.Path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	var ids []string
	for rec := range base.snapshot.Jobs.records(base.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if !slices.Contains(ids, runningID) {
		t.Fatalf("records after truncation = %v, want the still-running %q kept", ids, runningID)
	}
	if got := base.snapshot.Jobs.counts(base.snapshot.SessionID).shell; got != len(ids) || got != 1 {
		t.Fatalf("shell count = %d over %v, want the running job counted once", got, ids)
	}
	// It pages: the record after skipping the only shell entry is nothing, and
	// the entry itself is reachable at position 0.
	var paged []string
	for rec := range base.snapshot.Jobs.records(base.snapshot.SessionID, activityShellJob, 0) {
		paged = append(paged, rec.JobID)
	}
	if len(paged) != 1 || paged[0] != runningID {
		t.Fatalf("first page = %v, want the running job alone", paged)
	}

	// An unrelated job starts AFTER the rebuild. The rebuilt fold is extended by
	// that append rather than replaced by a new one, and the extension restores
	// no start record for the running job — so the fallback must survive it. This
	// is the case a `rebuilt` flag cleared on publication gets wrong: the first
	// page keeps the running job and every page after the append loses it, while
	// its process is still alive.
	later := time.Unix(1_600_000_900, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: later, JobID: "job_unrelated", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &later,
	}); err != nil {
		t.Fatalf("append unrelated job: %v", err)
	}
	refreshed, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the unrelated append: %v", err)
	}
	var afterAppend []string
	for rec := range refreshed.snapshot.Jobs.records(refreshed.snapshot.SessionID, activityShellJob, 0) {
		afterAppend = append(afterAppend, rec.JobID)
	}
	if !slices.Contains(afterAppend, runningID) {
		t.Fatalf("records after an unrelated append = %v, want the still-running %q still kept", afterAppend, runningID)
	}
	if got, want := refreshed.snapshot.Jobs.counts(refreshed.snapshot.SessionID).shell, len(afterAppend); got != want {
		t.Fatalf("shell count = %d over %v, want %d: a stranded running job is still counted", got, afterAppend, want)
	}
	// Retirement: once the job is no longer running, the fallback stops keeping
	// it. The durable row is still gone, so nothing should report it.
	s.jobManager.mu.Lock()
	delete(s.jobManager.running, runningID)
	s.jobManager.mu.Unlock()
	retired, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the job stopped: %v", err)
	}
	for rec := range retired.snapshot.Jobs.records(retired.snapshot.SessionID, activityShellJob, 0) {
		if rec.JobID == runningID {
			t.Fatalf("records after the job stopped = %v, want %q retired", afterAppend, runningID)
		}
	}
}

// TestLiveActivityBase_CompletionAfterATruncatedStartStillPagesAsShell pins the
// overlay's second obligation after a rebuild.
//
// Truncating a journal past a running job's start removes the row that said what
// the job is. A later completion for the same job appends a row that names the
// ID and nothing else about the projection: no shell type, no owner. The manager
// still holds the live record, so the page must render the job as the shell job
// it is and count it as one — the old merge substituted the live record before
// classifying, and an overlay that only appends rows the fold lacks gets this
// wrong twice: the stranded fallback skips a job the fold DOES name, and the
// page then classifies the durable completion as an unsupported record.
func TestLiveActivityBase_CompletionAfterATruncatedStartStillPagesAsShell(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}

	const runningID = "job_truncated_then_finished"
	started := time.Unix(1_600_000_500, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: runningID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}); err != nil {
		t.Fatalf("append the running job: %v", err)
	}
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID: runningID, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: started,
		},
		done:           make(chan struct{}),
		signal:         func() {},
		durableStarted: true,
	}
	s.jobManager.mu.Lock()
	s.jobManager.running[runningID] = run
	s.jobManager.mu.Unlock()
	t.Cleanup(func() {
		s.jobManager.mu.Lock()
		delete(s.jobManager.running, runningID)
		s.jobManager.mu.Unlock()
	})

	// Truncate past the start, then commit the completion. The durable fold now
	// names the job through a row that carries no type and no owner.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if err := os.Truncate(snapshot.Path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	finished := started.Add(time.Minute)
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: finished, JobID: runningID,
		Status: jobstore.StatusCompleted, EndedAt: &finished,
	}); err != nil {
		t.Fatalf("append the completion: %v", err)
	}

	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the completion: %v", err)
	}
	var ids []string
	for rec := range base.snapshot.Jobs.records(base.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if !slices.Contains(ids, runningID) {
		t.Fatalf("shell page after the completion = %v, want the still-running %q rendered as a shell job", ids, runningID)
	}
	// The row the page renders is the manager's live record, not the durable
	// completion: a page that shows a running job as unsupported or as finished
	// would be reporting the truncation rather than the job.
	for rec := range base.snapshot.Jobs.records(base.snapshot.SessionID, activityShellJob, 0) {
		if rec.JobID != runningID {
			continue
		}
		if rec.Status != jobstore.StatusRunning {
			t.Fatalf("rendered status = %q, want the live %q", rec.Status, jobstore.StatusRunning)
		}
		if rec.Type != jobstore.JobShell {
			t.Fatalf("rendered type = %q, want %q", rec.Type, jobstore.JobShell)
		}
	}
	if got := base.snapshot.Jobs.counts(base.snapshot.SessionID).shell; got != 1 {
		t.Fatalf("shell count = %d, want 1: the completion must not turn a running shell job into an unsupported record", got)
	}
	if got := base.snapshot.Jobs.counts(base.snapshot.SessionID).unsupported; got != 0 {
		t.Fatalf("unsupported count = %d, want 0: the same row must not be counted twice", got)
	}
}

// TestLiveActivityBase_WarmRefreshAfterARebuildCostsTheLiveSet pins the other
// cost of the stranded-row fallback. After a rebuild, the page must know which
// running jobs the fold can no longer place — a question whose honest answer
// walks the history. Asking it once per refresh would mean a poll that issues
// zero journal scans still visits every historical record, so the journal read
// would be incremental and the page behind it would not.
//
// The measurement is a count of records visited, not time: the fold counts how
// many records the durable-membership walk reads, and an unchanged refresh must
// read none after the first.
func TestLiveActivityBase_WarmRefreshAfterARebuildCostsTheLiveSet(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	// Long enough that a per-refresh walk would be visible in the count.
	seedLiveShellStarts(t, s, 600)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// The length the 600 seeded records occupy. The running job's record is
	// appended after it and truncated away below, so the rebuilt fold keeps the
	// whole history and still cannot place the running job — which is the case
	// where a per-refresh membership walk costs the history.
	seeded, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	const runningID = "job_warm_running"
	started := time.Unix(1_600_000_500, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: runningID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}); err != nil {
		t.Fatalf("append running job: %v", err)
	}
	s.jobManager.mu.Lock()
	s.jobManager.running[runningID] = &runningJob{
		rec: &jobstore.JobRecord{
			JobID: runningID, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: started,
		},
		done: make(chan struct{}), signal: func() {}, durableStarted: true,
	}
	s.jobManager.mu.Unlock()
	t.Cleanup(func() {
		s.jobManager.mu.Lock()
		delete(s.jobManager.running, runningID)
		s.jobManager.mu.Unlock()
	})

	// Cut the running job's own start record. The rebuild that follows strands it
	// while keeping every seeded row, and the first read after it is the one that
	// has to establish that.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	if err := os.Truncate(snapshot.Path, seeded.Size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	warm, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	var ids []string
	for rec := range warm.snapshot.Jobs.records(warm.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if len(ids) != 601 || !slices.Contains(ids, runningID) {
		t.Fatalf("records after the rebuild = %d of them, want the 600 seeded rows plus the stranded %q", len(ids), runningID)
	}

	// The warm refresh: nothing changed, so nothing publishes, and the stranded
	// set is the one already computed. Reading it must not touch the fold.
	visitedBefore := activityJobDurableVisits.Load()
	again, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	var againIDs []string
	for rec := range again.snapshot.Jobs.records(again.snapshot.SessionID, activityShellJob, 0) {
		againIDs = append(againIDs, rec.JobID)
	}
	if len(againIDs) != 601 || !slices.Contains(againIDs, runningID) {
		t.Fatalf("records on the warm refresh = %d of them, want the same 601 as the first read", len(againIDs))
	}
	if visited := activityJobDurableVisits.Load() - visitedBefore; visited != 0 {
		t.Fatalf("an unchanged refresh visited %d fold records, want 0", visited)
	}

	// The memo is only correct if a publication can retire it. Re-append the
	// running job's start record: the fold gains a durable row for it, so the
	// next read must place it in the durable order and stop reporting it as
	// stranded. A memo that answered from the previous revision would keep
	// treating it as missing and render it twice — once from the fold, once from
	// the stranded chunk.
	againStarted := time.Unix(1_600_000_700, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: againStarted, JobID: runningID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &againStarted,
	}); err != nil {
		t.Fatalf("re-append the running job: %v", err)
	}
	regained, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the job regained a durable row: %v", err)
	}
	occurrences := 0
	for rec := range regained.snapshot.Jobs.records(regained.snapshot.SessionID, activityShellJob, 0) {
		if rec.JobID == runningID {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("the running job appears %d times after it regained a durable row, want exactly 1", occurrences)
	}
	// 600 seeded rows plus this job's own start, which the fold now carries:
	// the stranded chunk is gone because its row is back in the durable order.
	if got, want := regained.snapshot.Jobs.counts(regained.snapshot.SessionID).shell, 601; got != want {
		t.Fatalf("shell count = %d after the job regained a durable row, want %d", got, want)
	}
}

// occurrenceCount reports how many times want appears in ids.
func occurrenceCount(ids []string, want string) int {
	count := 0
	for _, id := range ids {
		if id == want {
			count++
		}
	}
	return count
}

// TestLiveActivityBase_RefreshAfterAnOrdinaryAppendCostsTheLiveSet pins the cost
// of a refresh that follows an ordinary append rather than nothing.
//
// After a rebuild, each newly live job is a job ID the stranded memo has not
// answered yet, so it has to ask the fold whether it names that job. Asking by
// walking the list costs the whole history for one row that the fold's own
// job-ID index can name directly — and it does so under the fold mutex, which
// blocks waiters from joining a scan. The count asserted here is the number of
// records the walk visited: an indexed answer visits none.
func TestLiveActivityBase_RefreshAfterAnOrdinaryAppendCostsTheLiveSet(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 600)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	seeded, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// A rebuild, so the fold is in the stranded-aware mode where the memo is
	// consulted at all.
	const strandedID = "job_stranded_before_append"
	strandedAt := time.Unix(1_600_000_500, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: strandedAt, JobID: strandedID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &strandedAt,
	}); err != nil {
		t.Fatalf("append the job to strand: %v", err)
	}
	markRunningShell(t, s, strandedID, strandedAt)
	t.Cleanup(func() { dropRunningShell(s, strandedID) })
	if err := os.Truncate(seeded.Path, seeded.Size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rebuilt, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if !containsJob(rebuilt.snapshot.Jobs, rebuilt.snapshot.SessionID, strandedID) {
		t.Fatal("the rebuilt page did not strand the running job")
	}

	// An ordinary append: a new shell job starts and stays running, so the next
	// live snapshot holds an ID the memo has never been asked about.
	const appendedID = "job_running_after_append"
	appendedAt := time.Unix(1_600_000_800, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: appendedAt, JobID: appendedID, Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &appendedAt,
	}); err != nil {
		t.Fatalf("append the new running job: %v", err)
	}
	markRunningShell(t, s, appendedID, appendedAt)
	t.Cleanup(func() { dropRunningShell(s, appendedID) })

	visitedBefore := activityJobDurableVisits.Load()
	refreshed, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the ordinary append: %v", err)
	}
	if visited := activityJobDurableVisits.Load() - visitedBefore; visited != 0 {
		t.Fatalf("a refresh after an ordinary append visited %d fold records to place one new row, want 0", visited)
	}
	// Correctness with it: the appended job is durable, so it is placed in the
	// durable order exactly once, and the stranded one is still reported.
	var ids []string
	for rec := range refreshed.snapshot.Jobs.records(refreshed.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if count := occurrenceCount(ids, appendedID); count != 1 {
		t.Fatalf("shell page = %d rows with %q counted %d times, want it exactly once", len(ids), appendedID, count)
	}
	if !slices.Contains(ids, strandedID) {
		t.Fatalf("shell page lost the stranded %q after an ordinary append: %v", strandedID, ids)
	}
}

// TestLiveActivityBase_EachLiveSnapshotKeepsItsOwnStrandedJobs pins the answer
// the stranded memo has to give when two requests hold different live sets over
// one published fold.
//
// A running job whose start was truncated away is stranded: the fold cannot
// place it, so the page must still show it. Two requests sample the live set at
// different moments, and the job sets differ — a job that started between them
// is in the later snapshot and not the earlier one. A memo keyed on the fold's
// publication answers the second request from the first request's IDs, so the
// job only the second request knows about is never asked about and never
// reported as stranded. Keyed per job ID, each request gets answers about its
// own set.
func TestLiveActivityBase_EachLiveSnapshotKeepsItsOwnStrandedJobs(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 4)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	seeded, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	// Two running jobs, both with their start records cut away below. They join
	// the live set one at a time, so the two loads sample different snapshots
	// over the same published fold.
	const firstID, secondID = "job_stranded_first", "job_stranded_second"
	firstAt := time.Unix(1_600_000_600, 0).UTC()
	secondAt := time.Unix(1_600_000_700, 0).UTC()
	for _, want := range []struct {
		id string
		at time.Time
	}{{firstID, firstAt}, {secondID, secondAt}} {
		if err := s.jobManager.store.Append(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: want.at, JobID: want.id, Type: jobstore.JobShell,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &want.at,
		}); err != nil {
			t.Fatalf("append %s: %v", want.id, err)
		}
	}
	markRunningShell(t, s, firstID, firstAt)
	t.Cleanup(func() { dropRunningShell(s, firstID) })
	t.Cleanup(func() { dropRunningShell(s, secondID) })

	// Remove both starts, then rebuild: one fold, published once, cannot place
	// either job.
	if err := os.Truncate(seeded.Path, seeded.Size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rebuilt, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if !containsJob(rebuilt.snapshot.Jobs, rebuilt.snapshot.SessionID, firstID) {
		t.Fatal("the rebuilt page did not strand the running job")
	}

	// A second job becomes live WITHOUT the journal changing, so the fold is not
	// published again: the next load holds the same published fold but a larger
	// live set. Its own start was truncated away with the first one, so it has no
	// durable row and must be stranded too. A memo keyed on the publication alone
	// believes it has already answered for this fold and never asks about the ID
	// the earlier snapshot never had, so this job silently disappears.
	markRunningShell(t, s, secondID, secondAt)
	later, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !containsJob(later.snapshot.Jobs, later.snapshot.SessionID, secondID) {
		t.Fatal("a job that became live after the fold was published lost its stranded row: the memo answered for the wrong live set")
	}
	if !containsJob(later.snapshot.Jobs, later.snapshot.SessionID, firstID) {
		t.Fatal("the job both snapshots name lost its stranded row")
	}
}

// markRunningShell registers jobID as a running shell job in the manager, which
// is the live set a page overlays. The durable start record is the caller's to
// write; this only makes the job live.
func markRunningShell(t *testing.T, s *Session, jobID string, started time.Time) {
	t.Helper()
	s.jobManager.mu.Lock()
	s.jobManager.running[jobID] = &runningJob{
		rec: &jobstore.JobRecord{
			JobID: jobID, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: started,
		},
		done: make(chan struct{}), signal: func() {}, durableStarted: true,
	}
	s.jobManager.mu.Unlock()
}

// dropRunningShell retires jobID from the manager's live set.
func dropRunningShell(s *Session, jobID string) {
	s.jobManager.mu.Lock()
	delete(s.jobManager.running, jobID)
	s.jobManager.mu.Unlock()
}

// containsJob reports whether the page lists jobID as a shell record.
func containsJob(list *activityJobList, sessionID, jobID string) bool {
	for rec := range list.records(sessionID, activityShellJob, 0) {
		if rec.JobID == jobID {
			return true
		}
	}
	return false
}

// TestLiveActivityBase_MixedFoldIsNeverPublishedForTheOldGeneration pins the
// ordering that makes a scan's result safe to cache.
//
// A rewrite during an incremental scan replaces the prefix the fold already
// holds. The extension composes that cached prefix with the rewritten bytes — a
// list that never existed as one file — and would label it with the generation
// it started from. The caller that ran the scan resamples afterwards and
// retries, so it is safe. The hazard is any request holding that same
// generation: the mixed fold is labelled with it, so the cache answers that
// request with rows for content the store has disowned.
//
// The generation therefore has to be checked BEFORE the result is installed.
// This drives one extension with a validator that reports a newer generation,
// which is exactly the state a rewrite leaves behind, and asserts the
// publication did not happen: the last validated fold is untouched and the scan
// is marked stale so its waiter resamples instead of reading the untouched fold
// as its answer.
func TestLiveActivityBase_MixedFoldIsNeverPublishedForTheOldGeneration(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	fold := &s.jobManager.liveActivityFold
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// An append that the extension will fold. An ordinary append keeps the
	// generation, so the fold and this boundary still agree on it.
	delta := time.Unix(1_600_000_400, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: delta, JobID: "job_before_mixed", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &delta,
	}); err != nil {
		t.Fatalf("append before the mixed extension: %v", err)
	}
	grown, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}
	if grown.Generation != snapshot.Generation {
		t.Fatalf("an ordinary append moved the generation %d -> %d, so the fixture would not be testing a mixed fold", snapshot.Generation, grown.Generation)
	}

	fold.mu.Lock()
	before := fold.resultLocked()
	fold.mu.Unlock()

	// One extension whose validator reports the content changed under it.
	scan := &liveJobScan{
		done: make(chan struct{}), cancel: func() {}, attempted: grown.Size,
		gen: grown.Generation,
		validate: func(context.Context) (jobstore.CommittedJournalState, error) {
			changed := grown
			changed.Generation = grown.Generation + 1
			return changed, nil
		},
	}
	fold.mu.Lock()
	fold.scanning = scan
	fold.mu.Unlock()
	fold.run(context.Background(), scan, grown.Path)

	fold.mu.Lock()
	after := fold.resultLocked()
	published := fold.published
	fold.mu.Unlock()
	if !scan.stale {
		t.Fatal("the extension did not record that the content changed under it")
	}
	if after.jobs != before.jobs || published != before.published {
		t.Fatal("an extension whose bytes the store disowned was published as cache-visible")
	}
	var ids []string
	for rec := range after.jobs.records(s.ID(), activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if slices.Contains(ids, "job_before_mixed") {
		t.Fatalf("the disowned extension's row is in the published fold: %v", ids)
	}
}

// TestLiveActivityBase_OutOfOrderAppendAdvancesTheEpoch pins the other way a
// page's positions can move: not a truncation, but a start whose seq is older
// than one already folded. The journal scan returns it in file order, so the
// fold detects it belongs earlier and refolds from zero, inserting that row
// ahead of rows an earlier page already rendered. The caller used to discard
// that restart flag, so neither the epoch nor the revision moved and a
// continuation counted over the old order could repeat or skip a row.
func TestLiveActivityBase_OutOfOrderAppendAdvancesTheEpoch(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 2)
	before, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Append a start carrying an OLDER seq than the folded ones, which is what a
	// second writer to this journal looks like on disk. It goes through the raw
	// writer because the store assigns the next seq itself.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	started := time.Unix(1_600_000_000, 0).UTC()
	event := jobstore.Event{
		Kind: jobstore.EventJobStarted, Seq: 0, TS: started,
		JobID: "job_out_of_order", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := os.OpenFile(snapshot.Path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		t.Fatalf("append out-of-order event: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	after, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the out-of-order append: %v", err)
	}
	if after.snapshot.JobsEpoch == before.snapshot.JobsEpoch {
		t.Fatalf("epoch = %d after a refold that inserted a row ahead of folded ones, want an advance from %d", after.snapshot.JobsEpoch, before.snapshot.JobsEpoch)
	}
	// The row is there, and it is ahead of the jobs it precedes in seq order.
	var ids []string
	for rec := range after.snapshot.Jobs.records(after.snapshot.SessionID, activityShellJob, 0) {
		ids = append(ids, rec.JobID)
	}
	if len(ids) != 3 || ids[0] != "job_out_of_order" {
		t.Fatalf("records after the out-of-order append = %v, want job_out_of_order first of 3", ids)
	}
}

// TestLiveActivityBase_ObsoleteSnapshotIsResampledBeforeScanning pins what a
// stale boundary is: a boundary for content that no longer exists. A request
// samples a snapshot, stalls, and by the time it scans another request has
// repaired the journal and published a fold for the new content. The stalled
// request's own length describes the OLD file, so it must ask the store again
// rather than scan to a limit nothing backs any more.
//
// The assertion is the boundary the fold actually consumed, because that is
// what the damage is: with the obsolete limit, the scan reads to the length of
// a file that has been truncated and reports a generation that is not the
// file's.
func TestLiveActivityBase_ObsoleteSnapshotIsResampledBeforeScanning(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	stale, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// Truncate to one record and let a normal read publish the repaired fold, so
	// the store's committed generation moves past the one the stalled request
	// sampled.
	raw := mustReadCurrentJournal(t, stale.Path)
	firstLine := bytes.IndexByte(raw, '\n') + 1
	if firstLine <= 0 {
		t.Fatal("journal has no first line")
	}
	if err := os.Truncate(stale.Path, int64(firstLine)); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	repaired, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if got := repaired.snapshot.Jobs.len(); got != 1 {
		t.Fatalf("repaired load = %d records, want the 1 the file still has", got)
	}
	committed, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after repair: %v", err)
	}
	if committed.Size >= stale.Size {
		t.Fatalf("committed length %d did not fall below the sampled %d; the fixture never made the snapshot obsolete", committed.Size, stale.Size)
	}

	// The stalled request resumes with its obsolete snapshot. Its scan must run
	// to the store's current boundary, not the length it remembered.
	calls := spyLiveActivityScans(t)
	folded, err := s.jobManager.liveActivityFold.records(context.Background(), stale.Path, stale, func(ctx context.Context) (jobstore.CommittedJournalState, error) {
		return s.jobManager.store.CommittedJournal(ctx)
	})
	if err != nil {
		t.Fatalf("stalled read: %v", err)
	}
	if got := folded.jobs.len(); got != 1 {
		t.Fatalf("stalled read = %d records, want the repaired 1", got)
	}
	if len(*calls) != 0 {
		for _, call := range *calls {
			if call.endOffset != committed.Size {
				t.Fatalf("stalled read scanned to %d, want the store's current committed boundary %d", call.endOffset, committed.Size)
			}
		}
	}
	// The epoch is the published fold's: the stale request answered for the
	// current content, so it must not hand a page an epoch from before the
	// repair it did not perform.
	if folded.epoch != repaired.snapshot.JobsEpoch {
		t.Fatalf("stalled read epoch = %d, want the repaired fold's %d", folded.epoch, repaired.snapshot.JobsEpoch)
	}
}

// TestLiveActivityBase_RebuildsAfterAConfirmedTruncation pins the one case
// where a smaller committed boundary must NOT be served from the fold: the
// journal was truncated in place, so the rows the fold holds came from bytes the
// file no longer has. The store confirms it against the largest length it ever
// reported, and the read refolds from zero.
//
// The complementary case — a caller that merely sampled an older boundary
// during a normal append — must still hit the fold, and this test checks that
// too, because a fix that invalidated on every smaller sample would rescan the
// whole journal per racing request.
func TestLiveActivityBase_RebuildsAfterAConfirmedTruncation(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// A caller that samples an older boundary than the fold already consumed
	// during a normal append is served the fold, not a rescan.
	snapshot, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}
	stale := snapshot
	stale.Size = snapshot.Size - 1
	calls := spyLiveActivityScans(t)
	if _, err := s.jobManager.liveActivityFold.records(context.Background(), stale.Path, stale, func(ctx context.Context) (jobstore.CommittedJournalState, error) {
		return s.jobManager.store.CommittedJournal(ctx)
	}); err != nil {
		t.Fatalf("records at an older boundary: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("an older sampled boundary during a normal append scanned %d times, want 0: %+v", len(*calls), *calls)
	}

	// Now truncate the journal behind the store's back, removing the last two
	// jobs, and read again. The fold must not report rows the file has lost.
	path := snapshot.Path
	raw := mustReadCurrentJournal(t, path)
	firstLine := bytes.IndexByte(raw, '\n') + 1
	if firstLine <= 0 {
		t.Fatal("journal has no first line")
	}
	if err := os.Truncate(path, int64(firstLine)); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	*calls = nil
	base, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if got := base.snapshot.Jobs.len(); got != 1 {
		t.Fatalf("load after truncation = %d records, want the 1 the file still has", got)
	}
	// It rebuilt, so the scan started at byte zero rather than resuming past
	// content the file no longer holds.
	if len(*calls) == 0 {
		t.Fatal("the truncation was not rescanned")
	}
	for _, call := range *calls {
		if call.fromOffset != 0 {
			t.Fatalf("scan after truncation = %+v, want a rebuild from byte zero", call)
		}
	}
}

// mustReadCurrentJournal reads path as it is on disk now.
func mustReadCurrentJournal(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// TestLiveActivityBase_RetainsItsFoldAcrossManyJournals pins the fold's
// retention: a live tree reads every descendant's journal through the process
// LRU, so a root fold held in that LRU is evicted by a descendant-heavy tree and
// rescanned whole on the next refresh. The root's fold lives on its job manager
// instead, so reading many other journals leaves it intact.
func TestLiveActivityBase_RetainsItsFoldAcrossManyJournals(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	const total = 2 * activityMaxWorkUnits
	seedLiveShellStarts(t, s, total)

	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first loadLiveActivityBase: %v", err)
	}

	// Visit more distinct journals than the shared cache holds entries, the way
	// a deep live tree does, then return to the root.
	for i := range historicalJobFoldCacheEntries + 16 {
		path := filepath.Join(stateDir, fmt.Sprintf("descendant-%03d", i), "jobs.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// A real journal, not an absent path: an absent path is a tombstone
		// the cache does not store, so it could never evict anything.
		line, err := json.Marshal(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: time.Unix(1_600_000_000, 0).UTC(),
			JobID: fmt.Sprintf("job_desc_%03d", i), Type: jobstore.JobShell,
			OwnerSessionID:   fmt.Sprintf("descendant-%03d", i),
			VisibleToSession: fmt.Sprintf("descendant-%03d", i),
		})
		if err != nil {
			t.Fatalf("marshal descendant event: %v", err)
		}
		if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
			t.Fatalf("write descendant journal: %v", err)
		}
		if _, _, _, err := loadCachedJobRecords(context.Background(), path); err != nil {
			t.Fatalf("descendant fold %d: %v", i, err)
		}
	}

	calls := spyLiveActivityScans(t)
	again, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase after visiting descendants: %v", err)
	}
	if got := again.snapshot.Jobs.len(); got != total {
		t.Fatalf("load = %d records, want the complete %d", got, total)
	}
	if len(*calls) != 0 {
		t.Fatalf("the root journal was rescanned %d times after descendant reads, want 0: %+v", len(*calls), *calls)
	}
}

// TestLiveActivityBase_KeepsTheScanForARemainingWaiter pins the other half of
// the fold's lifetime: one caller leaving must not cancel a scan another caller
// still needs. Only the last waiter leaving stops the read.
func TestLiveActivityBase_KeepsTheScanForARemainingWaiter(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	seedLiveShellStarts(t, s, 3)

	// Every scan blocks until the test releases it, so both callers are inside
	// the same read when the first one is canceled.
	var scans atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scans.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(firstCtx, s)
		first <- err
	}()
	<-entered

	// A second caller joins the same cold fold rather than starting another.
	survivor := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(context.Background(), s)
		survivor <- err
	}()
	// Wait until the survivor has registered as a waiter on that same scan:
	// canceling the first caller before it does would leave the scan with no
	// waiters and legitimately stop it. Parking is the event under test, so
	// read it rather than guessing at a delay.
	testwait.Until(t, "the second caller to join the shared scan", func() bool {
		f := &s.jobManager.liveActivityFold
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.scanning != nil && f.scanning.waiters == 2
	})

	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller = %v, want context.Canceled", err)
	}

	// The survivor's scan is still alive: releasing it lets that caller finish
	// with the full result instead of inheriting the canceled caller's fate.
	close(release)
	if err := <-survivor; err != nil {
		t.Fatalf("surviving caller = %v, want the shared scan's result", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans = %d, want the one read both callers shared", got)
	}
}

// TestLiveActivityBase_SharedScanValidationOutlivesTheCanceledFirstCaller pins
// the context a shared scan validates with.
//
// A scan belongs to every request waiting on it, not to the one that started
// it: the first caller can be canceled while a second still holds the fold
// request open, and the scan's own context — detached from that first request —
// stays live until the last waiter leaves. Prepublication validation runs on
// the scan, so it must use the scan's context. Closing over the starting
// caller's context instead makes a canceled request decide a surviving one's
// answer, and only in the case validation does real work: a journal that gained
// a torn tail during the scan enters tail recovery, which stops on a canceled
// context, so the surviving caller is handed the canceled caller's error.
//
// The fixture forces exactly that window. The shared scan blocks mid-read; the
// starting caller is canceled; a partial record is appended behind the store's
// back; then the scan is released to validate. The surviving request must
// succeed with the complete fold, the shared scan must record that the content
// changed under it, and the store's tail repair must have removed the fragment.
func TestLiveActivityBase_SharedScanValidationOutlivesTheCanceledFirstCaller(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}))
	if s.jobManager == nil || s.jobManager.store == nil {
		t.Fatal("test session has no job store")
	}
	const seeded = 3
	seedLiveShellStarts(t, s, seeded)
	if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
		t.Fatalf("first load: %v", err)
	}
	journal, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal: %v", err)
	}

	// One committed append for the shared scan to fold, so the scan has work to
	// do and its boundary sits past the fold the first load published.
	delta := time.Unix(1_600_000_900, 0).UTC()
	if err := s.jobManager.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: delta, JobID: "job_shared_delta", Type: jobstore.JobShell,
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &delta,
	}); err != nil {
		t.Fatalf("append the delta the shared scan folds: %v", err)
	}
	prefix, err := s.jobManager.store.CommittedJournal(context.Background())
	if err != nil {
		t.Fatalf("CommittedJournal after the append: %v", err)
	}

	var scans atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		if scans.Add(1) == 1 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		return original(ctx, path, fromOffset, limits)
	}
	t.Cleanup(func() { scanJobJournal = original })

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(firstCtx, s)
		first <- err
	}()
	<-entered

	// A second caller joins that same read rather than starting another, and is
	// still waiting when the first one leaves.
	survivor := make(chan error, 1)
	go func() {
		_, err := loadLiveActivityBase(context.Background(), s)
		survivor <- err
	}()
	testwait.Until(t, "the second caller to join the shared scan", func() bool {
		f := &s.jobManager.liveActivityFold
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.scanning != nil && f.scanning.waiters == 2
	})
	f := &s.jobManager.liveActivityFold
	f.mu.Lock()
	shared := f.scanning
	f.mu.Unlock()

	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller = %v, want context.Canceled", err)
	}

	// A torn trailing record lands on the journal while the shared scan is in
	// flight and past its boundary, so the scan itself never reads it. The
	// prepublication boundary read is the next thing to touch the file, and it
	// has to repair the tail — the part of the read that stops on a canceled
	// context.
	torn := []byte(`{"kind":"job_started","seq":99,`)
	appendRaw(t, journal.Path, torn)
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans = %d before the shared scan was released, want the one blocked read", got)
	}

	close(release)
	if err := <-survivor; err != nil {
		t.Fatalf("surviving caller = %v, want the shared scan's result validated on the scan's own context", err)
	}
	// Validation is the first boundary read after the fragment landed: the
	// survivor cannot pass the scan's completion, so nothing else can have
	// touched the tail before it. A canceled context makes that read fail
	// outright, which is the error this test did not get.
	if shared.err != nil {
		t.Fatalf("shared scan recorded %v, want validation to complete on the scan's own context", shared.err)
	}
	if !shared.stale {
		t.Fatal("the shared scan published a fold without recording the content change validation saw")
	}

	// Read the journal BEFORE any later load can touch it: validation is the
	// first boundary read after the fragment landed, the survivor cannot pass
	// the scan's completion until it returned, and it is back to the committed
	// prefix with the fragment and nothing else cut.
	raw := mustReadCurrentJournal(t, journal.Path)
	if bytes.Contains(raw, torn) {
		t.Fatalf("the journal still holds the torn trailing record %q: validation never reached tail recovery on a live context", torn)
	}
	if len(raw) != int(prefix.Size) {
		t.Fatalf("journal is %d bytes after the repair, want the committed prefix %d: the tail repair must cut the fragment and nothing else", len(raw), prefix.Size)
	}

	// The surviving request answered for the repaired content: every seeded row
	// plus the delta.
	repaired, err := loadLiveActivityBase(context.Background(), s)
	if err != nil {
		t.Fatalf("load after the shared scan: %v", err)
	}
	if got, want := repaired.snapshot.Jobs.len(), seeded+1; got != want {
		t.Fatalf("records after the shared scan = %d, want the complete %d", got, want)
	}
}

// TestLiveActivityBase_UnreliableTimestampsRefoldASameLengthJournal pins the one
// case a length and a generation cannot decide.
//
// The store calibrates itself for a filesystem whose appends do not move the
// modification time, and answers its own reads by re-reading (see
// fileCursor.disabled). A reader outside the store has no cursor to disable. It
// compares a byte length and a content generation, and on such a filesystem a
// same-length in-place rewrite moves neither: appends there never moved the
// time, so the rewrite's time is the same frozen value the fold already
// recorded. Reusing the fold would keep serving the rows the rewrite removed for
// as long as the session stays idle.
//
// The counterweight is deliberate and asserted too: an ordinary append still
// extends the fold rather than refolding it. A different length is a change an
// append explains, so it is not this blind spot.
// TestLiveActivityBase_DoesNotReadTheIndexWhileAnExtensionOwnsIt pins H1.
//
// An extension mutates the fold's position map in place — that is what makes it
// cost the delta instead of the history — and it does so outside f.mu. A request
// that resolved the memo's indexed lookups against that map while an extension
// was running was therefore reading and writing the same map from two
// goroutines, which in Go terminates the process rather than returning an error.
// An ordinary activity read must not be able to crash the daemon.
//
// The observable property is the rule that removes the race: while an extension
// owns the index, lookups go to the immutable list instead. The list answers the
// same questions, so the rows are identical — what changes is that nothing reads
// the map the extension is writing.
func TestLiveActivityBase_DoesNotReadTheIndexWhileAnExtensionOwnsIt(t *testing.T) {
	started := time.Unix(1_600_000_000, 0).UTC()
	list := newActivityJobList([]*jobstore.JobRecord{
		{JobID: "job_000001", Type: jobstore.JobShell, StartedAt: started},
		{JobID: "job_000002", Type: jobstore.JobShell, StartedAt: started},
	})
	live := map[string]*jobstore.JobRecord{
		"job_000001": {JobID: "job_000001", Type: jobstore.JobShell, StartedAt: started},
		"job_000002": {JobID: "job_000002", Type: jobstore.JobShell, StartedAt: started},
	}

	var f liveJobFold
	positions := &activityJobPositions{
		of:   map[string]int{"job_000001": 0, "job_000002": 1},
		list: list,
	}
	result := liveJobFoldResult{jobs: list, positions: positions, published: 1}
	// Warm the memo with no extension running: the indexed path answers it.
	warm := f.liveRows(result, live)
	if len(warm) != 2 {
		t.Fatalf("warm live rows = %d answers, want 2", len(warm))
	}

	// A distinct live set, so the memo cannot answer from the warm call, and an
	// extension that owns the index while this request resolves its lookups.
	held := map[string]*jobstore.JobRecord{
		"job_000001": {JobID: "job_000001", Type: jobstore.JobShell, StartedAt: started},
	}
	f.scanning = &liveJobScan{done: make(chan struct{})}
	visitedBefore := activityJobDurableVisits.Load()
	rows := f.liveRows(result, held)
	if visited := activityJobDurableVisits.Load() - visitedBefore; visited == 0 {
		t.Fatal("a lookup was answered from the position index while an extension owned it; that read races the extension's in-place write to the same map")
	}
	// The answer is still the list's, so the rule costs correctness nothing.
	row, ok := rows["job_000001"]
	if !ok || !row.row {
		t.Fatalf("live rows while an extension owned the index = %+v, want job_000001 placed in the list", rows)
	}
}

// TestLiveActivityBase_RewriteThatChangesACountedRowAdvancesTheEpoch pins
// medium 2.
//
// A continuation's resume index is an ordinal among the SHELL rows a session
// owns, not among all rows. A rewrite can keep every job ID in place and in the
// same order while turning an earlier row from a shell job into an unsupported
// one, or handing it to a different owner. Every index after that row moves, so
// resuming at the old index names a different job and skips one the reader never
// saw.
//
// A fingerprint of the job IDs alone reads that rewrite as no change at all and
// leaves the epoch alone. The epoch has to cover what paging counts.
func TestLiveActivityBase_RewriteThatChangesACountedRowAdvancesTheEpoch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "jobs.jsonl")
	const session = "session-a"
	original := `{"seq":1,"kind":"job_started","job_id":"job_000001","type":"shell","owner_session_id":"session-a"}` + "\n" +
		`{"seq":2,"kind":"job_started","job_id":"job_000002","type":"shell","owner_session_id":"session-a"}` + "\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	state := func(size int64, gen uint64) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: size, Generation: gen,
			MetadataResolvesWrites: true,
		}
	}

	var f liveJobFold
	first, err := f.records(context.Background(), target, state(int64(len(original)), 1), nil)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := first.jobs.counts(session).shell; got != 2 {
		t.Fatalf("first read has %d owned shell rows, want the 2 the journal carries", got)
	}

	// The rewrite keeps both job IDs, in order and at the same length, and
	// changes the FIRST row's class. The resume index counts one fewer row
	// before job_000002, so any continuation past it now names the wrong job.
	rewritten := `{"seq":1,"kind":"job_started","job_id":"job_000001","type":"other","owner_session_id":"session-a"}` + "\n" +
		`{"seq":2,"kind":"job_started","job_id":"job_000002","type":"shell","owner_session_id":"session-a"}` + "\n"
	if len(rewritten) != len(original) {
		t.Fatalf("fixture rewrite is %d bytes, want the %d that keeps the length unchanged", len(rewritten), len(original))
	}
	if err := os.WriteFile(target, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("write rewrite: %v", err)
	}

	second, err := f.records(context.Background(), target, state(int64(len(rewritten)), 2), nil)
	if err != nil {
		t.Fatalf("read after the rewrite: %v", err)
	}
	if got := second.jobs.counts(session).shell; got != 1 {
		t.Fatalf("after the rewrite there are %d owned shell rows, want the 1 the rewrite leaves", got)
	}
	if second.epoch == first.epoch {
		t.Fatalf("epoch = %d after a rewrite that changed a row the resume index counts, want it above the %d the previous page was minted over: resuming would skip an unread job", second.epoch, first.epoch)
	}
}

// TestLiveActivityBase_AppendThatChangesACountedRowKeepsThePagingEpoch pins
// Medium 4.
//
// A warm extension accumulates each row's paging contribution, subtracting the
// old value and adding the new one so it stays O(delta). ApplyJob updates the
// record IN PLACE and returns the same pointer, so reading the contribution
// after it reads the new value twice: the difference is zero and the fold's sum
// keeps describing the row the event replaced.
//
// That sum is what a later verification rebuild is compared against. On a
// filesystem whose timestamps cannot vouch for the content, a read at an
// unchanged length refolds to be sure the bytes are the same, and the epoch
// moves only if the rows or their contributions moved. A sum left describing
// the old row makes a rebuild that found NOTHING changed look like a change, so
// the epoch advances and every outstanding continuation is refused — the
// continuation is invalidated by a read that proved nothing moved.
//
// So the assertion is the continuation's: after an append that changes a
// counted row, a verification read at an unchanged boundary must not advance
// the epoch.
func TestLiveActivityBase_AppendThatChangesACountedRowKeepsThePagingEpoch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "jobs.jsonl")
	const session = "session-a"
	original := `{"seq":1,"kind":"job_started","job_id":"job_000001","type":"shell","owner_session_id":"session-a"}` + "\n" +
		`{"seq":2,"kind":"job_started","job_id":"job_000002","type":"shell","owner_session_id":"session-a"}` + "\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	state := func(size int64, gen uint64, vouched bool) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: size, Generation: gen,
			MetadataResolvesWrites: vouched,
		}
	}

	var f liveJobFold
	first, err := f.records(context.Background(), target, state(int64(len(original)), 1, true), nil)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := first.jobs.counts(session).shell; got != 2 {
		t.Fatalf("first read has %d owned shell rows, want the 2 the journal carries", got)
	}

	// An append that changes job_000001's class. Nothing is removed and the
	// order is untouched: every row stays where it was, and only the row's own
	// contribution moves.
	delta := `{"seq":3,"kind":"job_started","job_id":"job_000001","type":"other","owner_session_id":"session-a"}` + "\n"
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := file.WriteString(delta); err != nil {
		_ = file.Close()
		t.Fatalf("append the class change: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close after append: %v", err)
	}

	extended, err := f.records(context.Background(), target, state(int64(len(original)+len(delta)), 1, true), nil)
	if err != nil {
		t.Fatalf("read after the append: %v", err)
	}
	if got := extended.jobs.counts(session).shell; got != 1 {
		t.Fatalf("after the append there are %d owned shell rows, want the 1 the append leaves", got)
	}

	// The fold's paging sum is its promise that it describes the rows it
	// published. Recount it from those rows — independently of the arithmetic
	// the extension used — and the two must agree. A sum left describing the
	// row the append replaced fails here, and in production it is what a later
	// rebuild is compared against: the rebuild recounts correctly, the sums
	// disagree over an unchanged list, the epoch advances, and every
	// outstanding continuation is refused by a read that proved nothing moved.
	var recount uint64
	for rec := range extended.jobs.all() {
		recount += foldJobPagingContribution(rec)
	}
	if got := f.fold.pagingSum; got != recount {
		t.Fatalf("the extended fold's paging sum = %d, but its published rows recount to %d: the sum still describes the row the append replaced, so a later rebuild over these same rows disagrees and refuses a valid continuation", got, recount)
	}

	// And the rows themselves are right: the append changed one row's class.
	if got := extended.jobs.counts(session).shell; got != 1 {
		t.Fatalf("after the append there are %d owned shell rows, want the 1 the append leaves", got)
	}
}

// TestLiveActivityBase_SameLengthRewriteThenAppendOnUnreliableMetadata pins
// medium 6.
//
// A same-length rewrite is invisible to a store whose timestamps cannot resolve
// writes: nothing moved, so nothing says the bytes moved either. The rewrite
// leaves the pre-write sample untouched, and an ordinary append afterwards books
// a LARGER length under the SAME generation. That is enough for a length-based
// rule to call the extension ordinary: the boundary grew, and the fold's
// recorded length differs from the caller's — so the extension resumes from the
// cached prefix.
//
// But that prefix was decoded from the file the rewrite replaced. Extending it
// serves rows the rewrite removed alongside the append that followed. On this
// filesystem nothing available can vouch for the prefix, so the only honest
// answer is to rebuild it.
func TestLiveActivityBase_SameLengthRewriteThenAppendOnUnreliableMetadata(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "jobs.jsonl")
	first := []byte(`{"seq":1,"kind":"job_started","job_id":"job_000001"}` + "\n")
	if err := os.WriteFile(target, first, 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	state := func(size int64) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: size, Generation: 0,
			// This filesystem cannot tell one of the store's own writes from no
			// write at all, so a length is not evidence the prefix is intact.
			MetadataResolvesWrites: false,
		}
	}

	var f liveJobFold
	if _, err := f.records(context.Background(), target, state(int64(len(first))), nil); err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Rewrite the prefix to a different job of exactly the same length, then
	// append: the store's generation does not move, only the length does.
	rewritten := []byte(`{"seq":1,"kind":"job_started","job_id":"job_999999"}` + "\n")
	if len(rewritten) != len(first) {
		t.Fatalf("fixture rewritten line is %d bytes, want the %d that keeps the length unchanged", len(rewritten), len(first))
	}
	appended := []byte(`{"seq":2,"kind":"job_started","job_id":"job_000002"}` + "\n")
	body := append(append([]byte(nil), rewritten...), appended...)
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatalf("write rewritten journal: %v", err)
	}

	result, err := f.records(context.Background(), target, state(int64(len(body))), nil)
	if err != nil {
		t.Fatalf("read after a same-length rewrite and an append: %v", err)
	}
	var ids []string
	for rec := range result.jobs.all() {
		ids = append(ids, rec.JobID)
	}
	if len(ids) != 2 || ids[0] != "job_999999" || ids[1] != "job_000002" {
		t.Fatalf("rows after the rewrite and append = %v, want [job_999999 job_000002]: the page must not keep the row the rewrite removed", ids)
	}
}

// TestLiveActivityBase_VerificationRebuildKeepsThePagingEpoch pins M5.
//
// On a filesystem whose timestamps cannot vouch for the content, a read at an
// unchanged length refolds the bytes to be sure they are the same bytes. That
// rebuild proves nothing moved — and the epoch is what a continuation is fenced
// on. Advancing it there means fetching the next page performs the same
// verification that refuses the previous page's token, so every page after the
// first is rejected for as long as the filesystem stays unreliable.
//
// The epoch moves for the PAGING consequence of a rebuild, so a rebuild that
// reproduces the same rows in the same order must leave it alone.
func TestLiveActivityBase_VerificationRebuildKeepsThePagingEpoch(t *testing.T) {
	const gen = uint64(7)
	dir := t.TempDir()
	target := filepath.Join(dir, "jobs.jsonl")
	body := []byte(`{"seq":1,"kind":"job_started","job_id":"job_000001"}` + "\n" +
		`{"seq":2,"kind":"job_started","job_id":"job_000002"}` + "\n")
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	state := func() jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: int64(len(body)), Generation: gen,
			// This store has proven its own appends cannot move the mtime, so
			// an unchanged length is not evidence the bytes are unchanged.
			MetadataResolvesWrites: false,
		}
	}

	var f liveJobFold
	first, err := f.records(context.Background(), target, state(), nil)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The second read at the same boundary cannot reuse the fold it inherited,
	// so it refolds those bytes. The rebuild must reproduce the same order.
	second, err := f.records(context.Background(), target, state(), nil)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.epoch != first.epoch {
		t.Fatalf("epoch = %d after a verification rebuild over unchanged bytes, want the %d it had: advancing it refuses the continuation minted by the page before it", second.epoch, first.epoch)
	}
	if got := second.jobs.len(); got != 2 {
		t.Fatalf("the verification rebuild produced %d rows, want the 2 on disk", got)
	}
}

// TestLiveActivityBase_SameLengthRewriteAtGenerationZero pins M3.
//
// The store's first published content is generation zero, and the fold's record
// of "the generation I refolded myself" started at zero too. Testing that value
// alone therefore treated a fold this request INHERITED as one it had built —
// and on an unreliable-timestamp store that is exactly the same-length rewrite
// blind spot. The bytes changed, nothing moved to say so, and the inherited rows
// were served as though this call had just read them.
//
// The rewrite below happens at generation zero, which is the case the value
// alone cannot tell apart from "never refolded".
func TestLiveActivityBase_SameLengthRewriteAtGenerationZero(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "jobs.jsonl")
	first := []byte(`{"seq":1,"kind":"job_started","job_id":"job_000001"}` + "\n")
	if err := os.WriteFile(target, first, 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	// Generation zero: the store's initial content.
	unreliable := func(size int64) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: size, Generation: 0,
			MetadataResolvesWrites: false,
		}
	}

	var f liveJobFold
	if _, err := f.records(context.Background(), target, unreliable(int64(len(first))), nil); err != nil {
		t.Fatalf("first read: %v", err)
	}
	second := []byte(`{"seq":1,"kind":"job_started","job_id":"job_999999"}` + "\n")
	if len(second) != len(first) {
		t.Fatalf("fixture rewrite changed the length (%d -> %d)", len(first), len(second))
	}
	if err := os.WriteFile(target, second, 0o644); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}
	got, err := f.records(context.Background(), target, unreliable(int64(len(second))), nil)
	if err != nil {
		t.Fatalf("read after the rewrite: %v", err)
	}
	var ids []string
	for _, chunk := range got.jobs.chunks {
		for _, rec := range chunk.records {
			if rec != nil {
				ids = append(ids, rec.JobID)
			}
		}
	}
	if len(ids) != 1 || ids[0] != "job_999999" {
		t.Fatalf("records after a same-length rewrite at generation zero = %v, want only job_999999: an inherited fold was treated as this request's own rebuild", ids)
	}
}

func TestLiveActivityBase_UnreliableTimestampsRefoldASameLengthJournal(t *testing.T) {
	const (
		path = "jobs.jsonl"
		gen  = uint64(7)
	)
	dir := t.TempDir()
	target := filepath.Join(dir, path)
	first := []byte(`{"seq":1,"kind":"job_started","job_id":"job_000001"}` + "\n")
	if err := os.WriteFile(target, first, 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	var f liveJobFold
	unreliable := func(size int64) jobstore.CommittedJournalState {
		return jobstore.CommittedJournalState{
			Path: target, Size: size, Generation: gen,
			// The store has proven its own appends cannot move the mtime here.
			MetadataResolvesWrites: false,
		}
	}
	trusting := func(size int64) jobstore.CommittedJournalState {
		state := unreliable(size)
		state.MetadataResolvesWrites = true
		return state
	}
	ids := func(result liveJobFoldResult) []string {
		var out []string
		if result.jobs == nil {
			return out
		}
		for _, chunk := range result.jobs.chunks {
			for _, rec := range chunk.records {
				if rec != nil {
					out = append(out, rec.JobID)
				}
			}
		}
		return out
	}

	// A first read at an unreliable-metadata store builds the fold from what is
	// on disk. Nothing is inherited here, so it is allowed to.
	if _, err := f.records(context.Background(), target, unreliable(int64(len(first))), nil); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The rewrite: same length, different job.
	second := []byte(`{"seq":1,"kind":"job_started","job_id":"job_999999"}` + "\n")
	if len(second) != len(first) {
		t.Fatalf("fixture rewrite changed the length (%d -> %d)", len(first), len(second))
	}
	if err := os.WriteFile(target, second, 0o644); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}
	got, err := f.records(context.Background(), target, unreliable(int64(len(second))), nil)
	if err != nil {
		t.Fatalf("read after the rewrite: %v", err)
	}
	if ids := ids(got); len(ids) != 1 || ids[0] != "job_999999" {
		t.Fatalf("records after a same-length rewrite at an unreliable-metadata store = %v, want only job_999999: the fold was reused at an unchanged length", ids)
	}

	// The counterweight: the very same reuse, at a length an append explains, is
	// still a cache hit. Build a fold over a longer journal and ask for a
	// shorter boundary, which is the ordinary append shape seen from the other
	// side (a caller that sampled before the append).
	longer := append(slices.Clone(second), []byte(`{"seq":2,"kind":"job_started","job_id":"job_000002"}`+"\n")...)
	if err := os.WriteFile(target, longer, 0o644); err != nil {
		t.Fatalf("extend journal: %v", err)
	}
	if _, err := f.records(context.Background(), target, unreliable(int64(len(longer))), nil); err != nil {
		t.Fatalf("read after the append: %v", err)
	}
	behind, err := f.records(context.Background(), target, unreliable(int64(len(second))), nil)
	if err != nil {
		t.Fatalf("read behind the append: %v", err)
	}
	if ids := ids(behind); len(ids) != 2 {
		t.Fatalf("records at a shorter boundary = %v, want the 2 the fold already holds: a differing length is not the blind spot and must stay a cache hit", ids)
	}

	// And on a store whose metadata can resolve a write, the unchanged length is
	// evidence: the same rewrite without touching the size is caught by the
	// generation, so no refold is needed here and the fold is reused as before.
	var trustingFold liveJobFold
	if _, err := trustingFold.records(context.Background(), target, trusting(int64(len(longer))), nil); err != nil {
		t.Fatalf("trusting first read: %v", err)
	}
	if _, err := trustingFold.records(context.Background(), target, trusting(int64(len(longer))), nil); err != nil {
		t.Fatalf("trusting second read: %v", err)
	}
}

// BenchmarkLiveActivityBase_RefreshRowsRead measures the cost this issue is
// about: how many journal rows one refresh of a live session's own activity
// reads, and what that costs.
//
// Two lanes, side by side on the SAME journal, because the comparison is the
// point and a single number would not show it:
//
//   - before: Store.LoadOrdered, the unbounded non-cancelable read the live
//     path used, which re-decodes and re-folds every record on every call.
//   - after: the incremental fold, which reads only what was appended since the
//     previous refresh.
//
// The lanes are the two refresh shapes a subscription actually repeats: a poll
// that finds the journal unchanged, and a poll that finds one appended event
// and then keeps polling at the new, stable boundary. The single-event delta
// read is asserted rather than benchmarked — repeated appends inside one
// benchmark iteration land in the same clock tick on a fast host, which the
// store correctly reads as "this filesystem cannot resolve a write" and answers
// by refolding; TestLiveActivityBase_ReadsOnlyTheAppendedDelta and
// TestLiveActivityBase_FoldsOnlyCommittedBytes pin that shape exactly, at one
// row read against a whole history.
//
// Rows read are counted at the scan seam — the events actually decoded out of
// the journal — rather than inferred from the list a page rendered, and they
// are reported through b.ReportMetric so the numbers land on the benchmark
// line. The append that moves the boundary is performed with the timer
// STOPPED: its fsync costs milliseconds and would otherwise be charged to the
// refresh, which would make the two lanes look alike for a reason that has
// nothing to do with either read.
func BenchmarkLiveActivityBase_RefreshRowsRead(b *testing.B) {
	const total = 3 * activityMaxWorkUnits
	for _, shape := range []string{"unchanged-journal", "after-one-append"} {
		b.Run("load-ordered/"+shape, func(b *testing.B) {
			benchRefresh(b, total, shape, false)
		})
		b.Run("incremental-fold/"+shape, func(b *testing.B) {
			benchRefresh(b, total, shape, true)
		})
	}
}

// benchRefresh seeds a session of total shell starts and then, per iteration,
// refreshes its live activity base either through LoadOrdered (fold == false)
// or through the incremental fold (fold == true).
//
// shape "unchanged-journal" appends nothing between refreshes.
// shape "after-one-append" appends exactly one event during setup, before the
// timer starts, and then measures the refreshes that follow it at the new
// boundary.
func benchRefresh(b *testing.B, total int, shape string, fold bool) {
	b.Helper()
	stateDir := b.TempDir()
	s := newBenchSession(b, stateDir)
	seedBenchShellStarts(b, s, total)

	// Count the events each refresh actually decodes, in BOTH lanes, and never
	// the records it returns.
	//
	// Those are different numbers for the baseline. LoadOrdered serves its
	// cached fold once the journal is unchanged, so it decodes nothing and
	// returns the whole history: a "rows" metric built from the returned slice
	// would report the baseline at 6000 rows per refresh no matter what it cost,
	// and comparing the incremental path against that number would be comparing
	// a decode count against a record count. The seams below are where each lane
	// really parses a line, so they count the work and the two metrics can be
	// read side by side.
	// scannedRows counts the events the incremental lane's scanner returns, and is
	// used only to check that lane's own resume behaviour. It is NOT a decode
	// count and must never be added to one: those events were decoded by the
	// scanner, and decodedRead already counts them at the store seam.
	var scannedRows int64
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		events, toOffset, err := original(ctx, path, fromOffset, limits)
		scannedRows += int64(len(events))
		return events, toOffset, err
	}
	b.Cleanup(func() { scanJobJournal = original })
	var storeDecodedRows int64
	originalDecode := jobstore.DecodeJournalEvent
	jobstore.DecodeJournalEvent = func(data []byte, v any) error {
		storeDecodedRows++
		return originalDecode(data, v)
	}
	b.Cleanup(func() { jobstore.DecodeJournalEvent = originalDecode })

	// Prime BOTH lanes so every measured iteration is the refresh shape a
	// subscription actually repeats, not a first cold read.
	//
	// The baseline must be primed for the comparison to mean anything. Its store
	// keeps a fold cache, so a repeat LoadOrdered returns the cached records
	// without decoding a line; leaving the first, cold decode inside the timed
	// loop would put the baseline's one-off read into an average of twenty and
	// report a cost per refresh that no steady-state reader pays. Priming makes
	// the two lanes answer the same question: what does one more refresh cost.
	if fold {
		if _, err := loadLiveActivityBase(context.Background(), s); err != nil {
			b.Fatalf("prime fold: %v", err)
		}
	} else {
		if _, err := s.jobManager.store.LoadOrdered(); err != nil {
			b.Fatalf("prime LoadOrdered: %v", err)
		}
	}
	if shape == "after-one-append" {
		if err := s.jobManager.store.Append(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: time.Unix(1_600_000_001, 0).UTC(),
			JobID: "job_bench_append", Type: jobstore.JobShell,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(),
		}); err != nil {
			b.Fatalf("append: %v", err)
		}
	}

	// decodedRead counts journal records PARSED, through the one seam every lane
	// reaches: the fold's scanner decodes what it reads, Store.LoadOrdered decodes
	// what it reads, and both are counted here rather than in the caller's own
	// bookkeeping. Counting per-lane is how the earlier version came to report a
	// full incremental rescan as zero decodes while counting the baseline's
	// returned records as reads.
	var decodedRead int64
	// recordsReturned counts records handed to the caller, which is the cloning
	// cost and not a read. Both lanes report it the same way, so the metric means
	// the same thing in both rows.
	var recordsReturned int64
	refresh := func() {
		atDecoded := storeDecodedRows
		defer func() { decodedRead += storeDecodedRows - atDecoded }()
		if fold {
			loaded, err := loadLiveActivityBase(context.Background(), s)
			if err != nil {
				b.Fatalf("loadLiveActivityBase: %v", err)
			}
			recordsReturned += int64(loaded.snapshot.Jobs.len())
			return
		}
		ordered, err := s.jobManager.store.LoadOrdered()
		if err != nil {
			b.Fatalf("LoadOrdered: %v", err)
		}
		recordsReturned += int64(len(ordered))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		refresh()
	}
	b.StopTimer()
	// Two different numbers, and neither substitutes for the other:
	//
	//   records-returned/refresh — records the caller receives. It is NOT a count of
	//   copies: the fold hands back a published list, the baseline deep-copies one,
	//   and that difference shows in B/op rather than here. Named for what it
	//   counts so it cannot be read as either.
	//   decoded/refresh — journal records actually parsed, which is the read cost
	//   itself.
	//
	// The baseline now reports 0 decoded per refresh and still pays the
	// records/refresh clone, which is the honest shape of the defect: the live
	// path's cost per refresh was never the decode alone.
	b.ReportMetric(float64(recordsReturned)/float64(b.N), "records-returned/refresh")
	b.ReportMetric(float64(decodedRead)/float64(b.N), "decoded/refresh")
	// The journal's event count is what "whole history" means here, so a reader
	// can see the ratio rather than infer it.
	journalRows := int64(total)
	if shape == "after-one-append" {
		journalRows++
	}
	b.ReportMetric(float64(journalRows), "journal-rows")
}

// newBenchSession builds the session these benchmarks drive. It mirrors
// newSession's zero-option fixture, which takes a *testing.T and so cannot be
// called from a benchmark.
func newBenchSession(b *testing.B, stateDir string) *Session {
	b.Helper()
	s, err := NewSession(benchClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(b.TempDir()), SessionConfig{
		StateDir:         stateDir,
		AgentsDocPath:    filepath.Join(b.TempDir(), "no-personal-AGENTS.md"),
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		b.Fatalf("NewSession: %v", err)
	}
	b.Cleanup(s.Close)
	return s
}

func seedBenchShellStarts(b *testing.B, s *Session, n int) {
	b.Helper()
	started := time.Unix(1_600_000_000, 0).UTC()
	seed := make([]jobstore.Event, 0, n)
	for i := range n {
		seed = append(seed, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started,
			JobID: fmt.Sprintf("job_bench_%06d", i), Type: jobstore.JobShell,
			OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
		})
	}
	if err := s.jobManager.store.AppendBatch(seed); err != nil {
		b.Fatalf("seed journal: %v", err)
	}
}
