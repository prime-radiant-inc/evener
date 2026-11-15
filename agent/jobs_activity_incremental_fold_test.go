package agent

import (
	"bufio"
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

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
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
