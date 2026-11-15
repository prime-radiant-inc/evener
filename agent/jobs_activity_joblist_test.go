package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// activityJobListTestEvents returns n seq-ordered events over a pool of jobs
// spread across several owners and types, with repeat events for the same
// job (finishes, notifications) so a fold both appends and replaces in place.
func activityJobListTestEvents(rng *rand.Rand, n int) []jobstore.Event {
	owners := []string{"root", "other", ""}
	types := []jobstore.JobType{jobstore.JobShell, jobstore.JobShell, jobstore.JobType("unknown"), jobstore.JobType(delegateResourceType)}
	started := time.Unix(1_700_000_000, 0).UTC()
	events := make([]jobstore.Event, 0, n)
	for i := range n {
		jobID := fmt.Sprintf("job_%d", rng.IntN(n/2+1))
		event := jobstore.Event{Seq: int64(i + 1), TS: started, JobID: jobID}
		switch rng.IntN(4) {
		case 0, 1:
			at := started.Add(time.Duration(rng.IntN(1000)) * time.Second)
			event.Kind = jobstore.EventJobStarted
			event.Type = types[rng.IntN(len(types))]
			event.OwnerSessionID = owners[rng.IntN(len(owners))]
			event.StartedAt = &at
		case 2:
			event.Kind = jobstore.EventJobFinished
			event.Status = jobstore.StatusCompleted
			event.TerminalGen = "gen"
		default:
			event.Kind = jobstore.EventJobNotificationPending
			event.TerminalGen = "gen"
		}
		if rng.IntN(40) == 0 {
			event.JobID = "" // a record no page can show
		}
		events = append(events, event)
	}
	return events
}

// requireActivityJobListMatchesFold checks list against a fresh full fold of
// events: the same records in the same order, and every session's view of
// them — counts, and the records each skip position starts at — the same as
// filtering that order directly.
func requireActivityJobListMatchesFold(t *testing.T, list *activityJobList, events []jobstore.Event) {
	t.Helper()
	want := newActivityJobList(slices.DeleteFunc(jobstore.FoldOrdered(events), func(rec *jobstore.JobRecord) bool { return rec.JobID == "" }))
	if got, wantAll := slices.Collect(list.all()), slices.Collect(want.all()); !sameActivityRecords(got, wantAll) {
		t.Fatalf("folded list = %v, want %v", activityRecordIDs(got), activityRecordIDs(wantAll))
	}
	for _, sessionID := range []string{"root", "other", ""} {
		var shells, unsupported []*jobstore.JobRecord
		for rec := range want.all() {
			if !activityJobOwnedBy(sessionID, rec.OwnerSessionID) {
				continue
			}
			switch classifyActivityJob(rec) {
			case activityShellJob:
				shells = append(shells, rec)
			case activityUnsupportedJob:
				unsupported = append(unsupported, rec)
			}
		}
		if got := list.counts(sessionID); got != (activityJobCounts{shell: len(shells), unsupported: len(unsupported)}) {
			t.Fatalf("session %q counts = %+v, want %d shell and %d unsupported", sessionID, got, len(shells), len(unsupported))
		}
		for skip := 0; skip <= len(shells); skip += max(1, len(shells)/7) {
			if got := slices.Collect(list.records(sessionID, activityShellJob, skip)); !sameActivityRecords(got, shells[skip:]) {
				t.Fatalf("session %q shell records from %d = %v, want %v", sessionID, skip, activityRecordIDs(got), activityRecordIDs(shells[skip:]))
			}
		}
		if got := slices.Collect(list.records(sessionID, activityUnsupportedJob, 0)); !sameActivityRecords(got, unsupported) {
			t.Fatalf("session %q unsupported records = %v, want %v", sessionID, activityRecordIDs(got), activityRecordIDs(unsupported))
		}
	}
}

// sameActivityRecords compares record values in order, an empty list
// matching a nil one.
func sameActivityRecords(got, want []*jobstore.JobRecord) bool {
	return len(got) == len(want) && (len(got) == 0 || reflect.DeepEqual(got, want))
}

// TestHistoricalJobFold_IncrementalExtensionMatchesFullFold folds one
// history in deltas of every size, spanning many chunks, and requires each
// step to equal a full fold of everything seen so far — while every earlier
// fold, which concurrent pages may still be reading, stays exactly as it
// was.
func TestHistoricalJobFold_IncrementalExtensionMatchesFullFold(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 11))
	events := activityJobListTestEvents(rng, 6*activityJobChunkRecords)
	type kept struct {
		fold   historicalJobFold
		folded int
	}
	var earlier []kept
	var fold historicalJobFold
	for folded := 0; folded < len(events); {
		size := min(len(events)-folded, 1+rng.IntN(activityJobChunkRecords))
		fold = fold.extend(events[folded : folded+size])
		folded += size
		requireActivityJobListMatchesFold(t, fold.jobs, events[:folded])
		earlier = append(earlier, kept{fold: fold, folded: folded})
	}
	// Records, counts and positions alike: a reader of any earlier fold
	// still sees exactly the history that fold covered.
	for _, k := range earlier {
		requireActivityJobListMatchesFold(t, k.fold.jobs, events[:k.folded])
	}
}

// TestActivityJobList_ResumePassesWholeChunksByTheirCounts pins how a late
// page reaches its first record: a chunk whose counts say it holds only
// records the page has already shown is passed without visiting its
// records. The first chunk here claims a full chunk of root's shell records
// but holds none, so resuming past that many records starts at the second
// chunk's first record; walking the records instead would skip into the
// second chunk and lose its records.
func TestActivityJobList_ResumePassesWholeChunksByTheirCounts(t *testing.T) {
	t.Parallel()
	later := make([]*jobstore.JobRecord, 0, activityJobChunkRecords)
	for i := range activityJobChunkRecords {
		later = append(later, &jobstore.JobRecord{JobID: fmt.Sprintf("job_%d", i), Type: jobstore.JobShell, OwnerSessionID: "root"})
	}
	list := &activityJobList{chunks: []*activityJobChunk{
		{owners: map[string]activityJobCounts{"root": {shell: activityJobChunkRecords}}},
		newActivityJobList(later).chunks[0],
	}}
	got := slices.Collect(list.records("root", activityShellJob, activityJobChunkRecords))
	if !sameActivityRecords(got, later) {
		t.Fatalf("resume past the first chunk yielded %d records starting %v, want all %d of the second chunk", len(got), activityRecordIDs(got[:min(len(got), 1)]), len(later))
	}
}

// TestHistoricalJobFold_ExtendingAnOlderFoldStillMatchesFullFold extends a
// fold that has already been extended once. Its job index now describes the
// newer list, so the second extension must not trust it.
func TestHistoricalJobFold_ExtendingAnOlderFoldStillMatchesFullFold(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 5))
	events := activityJobListTestEvents(rng, 3*activityJobChunkRecords)
	base := historicalJobFold{}.extend(events[:activityJobChunkRecords])
	_ = base.extend(events[activityJobChunkRecords : 2*activityJobChunkRecords])
	branch := base.extend(events[2*activityJobChunkRecords:])
	requireActivityJobListMatchesFold(t, branch.jobs, append(slices.Clone(events[:activityJobChunkRecords]), events[2*activityJobChunkRecords:]...))
}

// TestExtendHistoricalJobFold_OlderSeqInDeltaRefoldsWholeJournal appends an
// event whose seq is older than one already folded. A full fold sorts it
// first, so the cached fold refolds the journal rather than appending it.
func TestExtendHistoricalJobFold_OlderSeqInDeltaRefoldsWholeJournal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	started := time.Unix(1_700_000_000, 0).UTC()
	event := func(seq int64, jobID string) jobstore.Event {
		return jobstore.Event{Kind: jobstore.EventJobStarted, Seq: seq, TS: started, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: "root", StartedAt: &started}
	}
	appendEvents := func(events ...jobstore.Event) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	first := []jobstore.Event{event(5, "job_late"), event(6, "job_later")}
	appendEvents(first...)
	fold, offset, restarted, err := extendHistoricalJobFold(context.Background(), path, 0, historicalJobFold{})
	if err != nil {
		t.Fatal(err)
	}
	if restarted {
		t.Fatal("first fold reported a restart, want an ordinary fold from zero")
	}
	requireActivityJobListMatchesFold(t, fold.jobs, first)

	early := event(2, "job_early")
	appendEvents(early)
	fold, _, restarted, err = extendHistoricalJobFold(context.Background(), path, offset, fold)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted {
		t.Fatal("out-of-order append did not report the order-restarting refold")
	}
	requireActivityJobListMatchesFold(t, fold.jobs, append(first, early))
	if got := activityRecordIDs(slices.Collect(fold.jobs.all())); got[0] != "job_early" {
		t.Fatalf("order = %v, want the older seq first", got)
	}
}
