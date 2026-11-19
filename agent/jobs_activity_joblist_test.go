package agent

import (
	"context"
	"encoding/json"
	"errors"
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
// mustExtendFold extends fold with events and fails the test if the extension
// reports an error. extend observes cancellation (see historicalJobFold.extend),
// so tests that want a fold must say they expect it to finish.
func mustExtendFold(t *testing.T, fold historicalJobFold, events []jobstore.Event) historicalJobFold {
	t.Helper()
	next, err := fold.extend(t.Context(), events)
	if err != nil {
		t.Fatalf("extend fold: %v", err)
	}
	return next
}

// TestHistoricalJobFold_CanceledExtensionLeavesNoPositionIndex is the durable
// half of an abandoned fold. Cancellation inside the reduction must report, and
// the fold's shared position index must be detached before it does: the index is
// written in place, so a fold that stops halfway leaves entries naming positions
// in a list nobody publishes, and the next extension that trusts it replaces a
// slot outside the list it is building. The rows are checked after the cancel
// because a stale index does not corrupt the count — it corrupts the next
// extension's placement.
func TestHistoricalJobFold_CanceledExtensionLeavesNoPositionIndex(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(13, 17))
	base := activityJobListTestEvents(rng, activityJobChunkRecords)
	delta := activityJobListTestEvents(rng, 3*activityJobChunkRecords)
	for i := range delta {
		delta[i].JobID = fmt.Sprintf("job_new_%04d", i)
		delta[i].Seq = int64(len(base) + i + 1)
	}
	fold := mustExtendFold(t, historicalJobFold{}, base)
	if fold.positions == nil || fold.positions.list != fold.jobs {
		t.Fatal("the base fold has no position index for its own list")
	}

	ctx, cancel := context.WithCancel(t.Context())
	// Cancellation is observed between chunks of events, so cancel before the
	// first check rather than racing a real request's timing.
	cancel()
	next, err := fold.extend(ctx, delta)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extend on a canceled context = %v, want context.Canceled", err)
	}
	if next.jobs != nil {
		t.Fatal("a canceled extension returned a list")
	}
	if fold.positions.list == fold.jobs {
		t.Fatal("a canceled extension left the position index attached to the published list")
	}

	// The published fold still describes exactly the history it covered, and an
	// extension after the cancel rebuilds from that fold rather than from the
	// index the canceled one abandoned.
	requireActivityJobListMatchesFold(t, fold.jobs, base)
	extended := mustExtendFold(t, fold, delta)
	requireActivityJobListMatchesFold(t, extended.jobs, append(slices.Clone(base), delta...))
}

// TestSortJobEventsBySeq_LeavesOrderedEventsAlone pins the cheap half of the
// sort: a journal scan already yields ascending seqs, so the order must be
// detected rather than permuted. The slice's backing array is passed through
// unchanged, so an already-ordered delta costs one comparison pass and no
// reallocation.
func TestSortJobEventsBySeq_LeavesOrderedEventsAlone(t *testing.T) {
	t.Parallel()
	if !jobEventsOrdered([]jobstore.Event{{Seq: 1}, {Seq: 2}, {Seq: 3}}) {
		t.Fatal("an ascending delta was not detected as ordered")
	}
	if jobEventsOrdered([]jobstore.Event{{Seq: 2}, {Seq: 1}}) {
		t.Fatal("a descending delta was detected as ordered")
	}

	// The ordered fast path must not permute. Events carrying a marker field
	ordered := []jobstore.Event{{Seq: 1, JobID: "a"}, {Seq: 2, JobID: "b"}, {Seq: 3, JobID: "c"}}
	if err := sortJobEventsBySeq(t.Context(), ordered); err != nil {
		t.Fatalf("sortJobEventsBySeq: %v", err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if ordered[i].JobID != want {
			t.Fatalf("ordered delta = %v, want a,b,c untouched", []string{ordered[0].JobID, ordered[1].JobID, ordered[2].JobID})
		}
	}

	scrambled := []jobstore.Event{{Seq: 3}, {Seq: 1}, {Seq: 2}}
	if err := sortJobEventsBySeq(t.Context(), scrambled); err != nil {
		t.Fatalf("sortJobEventsBySeq: %v", err)
	}
	for i, want := range []int64{1, 2, 3} {
		if scrambled[i].Seq != want {
			t.Fatalf("sorted seqs = %v, want 1,2,3", []int64{scrambled[0].Seq, scrambled[1].Seq, scrambled[2].Seq})
		}
	}

	// A canceled context is reported rather than sorted under.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unordered := []jobstore.Event{{Seq: 2}, {Seq: 1}}
	if err := sortJobEventsBySeq(ctx, unordered); !errors.Is(err, context.Canceled) {
		t.Fatalf("sortJobEventsBySeq on a canceled context = %v, want context.Canceled", err)
	}
}

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
		fold = mustExtendFold(t, fold, events[folded:folded+size])
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
	base := mustExtendFold(t, historicalJobFold{}, events[:activityJobChunkRecords])
	_ = mustExtendFold(t, base, events[activityJobChunkRecords:2*activityJobChunkRecords])
	branch := mustExtendFold(t, base, events[2*activityJobChunkRecords:])
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

// newActivityJobList lists records, already one per job ID in the order a
// page shows them (mergeActivityRecords' output), dropping nil records. The
// list holds the records themselves, so the caller hands over records it
// will not mutate.
func newActivityJobList(records []*jobstore.JobRecord) *activityJobList {
	b := newActivityJobListBuilder(nil, nil)
	for _, rec := range records {
		if rec != nil {
			b.push(rec)
		}
	}
	return b.list()
}

// TestActivityJobList_StrandedAppendLeavesThePublishedChunksAlone pins M1.
//
// A rebuilt page that finds a running job with no durable row has to append a
// chunk holding it. The chunk slice it appends to is the fold's own, and a slice
// grown by append usually carries spare capacity — so the append writes its chunk
// pointer past the fold's length, INTO the array the fold is still serving from.
//
// Two things follow, and this asserts both. The published fold grows a chunk it
// never built, and a second request over that same fold with a different live
// snapshot overwrites that slot with its own stranded set, so a page built from
// either list renders the other request's jobs.
func TestActivityJobList_StrandedAppendLeavesThePublishedChunksAlone(t *testing.T) {
	t.Parallel()
	const root = "root"
	// Enough rows to span several chunks, so the chunk slice has room past its
	// length for an append to write into.
	records := make([]*jobstore.JobRecord, 0, 3*activityJobChunkRecords)
	for i := range 3 * activityJobChunkRecords {
		records = append(records, &jobstore.JobRecord{
			JobID: fmt.Sprintf("job_durable_%05d", i), Type: jobstore.JobShell, OwnerSessionID: root,
		})
	}
	published := newActivityJobList(records)
	if len(published.chunks) < 2 {
		t.Fatalf("fixture built %d chunks, want several so the slice can have spare capacity", len(published.chunks))
	}
	before := len(published.chunks)
	publishedLen := published.len()

	// A running job with no durable row, which the overlay appends as a chunk.
	strandedA := &jobstore.JobRecord{JobID: "job_stranded_a", Type: jobstore.JobShell, OwnerSessionID: root}
	first := published.withLiveRebuilt(
		map[string]*jobstore.JobRecord{strandedA.JobID: strandedA},
		map[string]struct{}{strandedA.JobID: {}},
		nil,
	)
	if got := first.len(); got != publishedLen+1 {
		t.Fatalf("the overlay holds %d rows, want the %d durable rows plus the stranded one", got, publishedLen+1)
	}
	// The published fold must not have grown the stranded chunk.
	if got := len(published.chunks); got != before {
		t.Fatalf("published fold holds %d chunks after another list appended a stranded one, want %d: the append wrote into the shared backing array", got, before)
	}
	// A second request over the same fold, with a different stranded job, must
	// not overwrite the first request's slot.
	strandedB := &jobstore.JobRecord{JobID: "job_stranded_b", Type: jobstore.JobShell, OwnerSessionID: root}
	second := published.withLiveRebuilt(
		map[string]*jobstore.JobRecord{strandedB.JobID: strandedB},
		map[string]struct{}{strandedB.JobID: {}},
		nil,
	)
	var ids []string
	for rec := range first.all() {
		ids = append(ids, rec.JobID)
	}
	if !slices.Contains(ids, strandedA.JobID) {
		t.Fatalf("the first overlay lost %q after a second request over the same fold: its slot now names the other request's job", strandedA.JobID)
	}
	if slices.Contains(ids, strandedB.JobID) {
		t.Fatalf("the first overlay renders %q, which only the second request's live snapshot holds", strandedB.JobID)
	}
	var secondIDs []string
	for rec := range second.all() {
		secondIDs = append(secondIDs, rec.JobID)
	}
	if !slices.Contains(secondIDs, strandedB.JobID) {
		t.Fatalf("the second overlay lost its own stranded %q", strandedB.JobID)
	}
}

// TestActivityJobList_SubstitutionLeavesThePublishedChunksAlone pins the
// overlay's obligation to the list it was handed.
//
// A rebuilt page has to substitute a row whose durable projection differs from
// the live one: a truncation past a running job's start can leave its completion
// as the only durable row, classified as an unsupported record under no owner
// while the live record is a shell job under one. Substituting it must write the
// SUBSTITUTING list, not the fold: slices.Clip keeps the backing array, so a
// chunk pointer written through a clipped slice lands in the published fold's own
// slice. Two symptoms follow, and this asserts both — the completed job keeps
// rendering as running for a reader holding the old list, and the fold's own
// counts stop describing the fold's own rows.
func TestActivityJobList_SubstitutionLeavesThePublishedChunksAlone(t *testing.T) {
	t.Parallel()
	const root = "root"
	// A completion-only row: it names the job and nothing about the projection.
	published := newActivityJobList([]*jobstore.JobRecord{
		{JobID: "job_done", OwnerSessionID: root},
	})
	if got := published.counts(root).shell; got != 0 {
		t.Fatalf("published shell count = %d, want 0 before any substitution", got)
	}
	// The live set still holds the job as a running shell job, so its row has to
	// be substituted.
	running := &jobstore.JobRecord{JobID: "job_done", Type: jobstore.JobShell, OwnerSessionID: root}
	live := map[string]*jobstore.JobRecord{"job_done": running}
	row := published.durableRows(map[string]struct{}{"job_done": {}})["job_done"]
	if !row.row {
		t.Fatal("the fold does not name job_done, so this fixture cannot test substitution")
	}
	substituted := published.withLiveRebuilt(live, nil, map[string]activityJobDurableRow{"job_done": row})
	if got := substituted.counts(root).shell; got != 1 {
		t.Fatalf("substituted shell count = %d, want 1: the live shell job replaces the unclassified row", got)
	}
	// The published list is a different value and must be unchanged. Reading it
	// after the substitution is what a second page still holding the fold does.
	if got := published.counts(root).shell; got != 0 {
		t.Fatalf("published shell count = %d after a substitution on another list, want 0: the write reached the shared fold", got)
	}
	got := slices.Collect(published.records(root, activityShellJob, 0))
	if len(got) != 0 {
		t.Fatalf("published records = %v, want none: the substitution mutated the published list", activityRecordIDs(got))
	}
}

// TestLiveJobFoldResult_DoesNotAnswerForAnotherListsIndex pins the guard that
// makes a stale request safe.
//
// Two requests can hold different folds of the same journal: one sampled before a
// completion was published, one after. A position index describes exactly one
// list's slots, so an answer taken from it names a slot in THAT list. Applying
// that slot to a different — possibly shorter, possibly empty — list is how the
// panic happens: the older request applies a completion at an index only the
// newer list has. The index therefore has to be refused unless it belongs to the
// list being answered, and the walk that follows must answer instead.
func TestLiveJobFoldResult_DoesNotAnswerForAnotherListsIndex(t *testing.T) {
	t.Parallel()
	const root = "root"
	older := newActivityJobList(nil) // the truncated-away case: the page is empty
	newer := newActivityJobList([]*jobstore.JobRecord{
		{JobID: "job_finished", OwnerSessionID: root},
	})
	positions := &activityJobPositions{
		of:   map[string]int{"job_finished": 0},
		list: newer,
	}
	stale := liveJobFoldResult{jobs: older, positions: positions}
	if row, ok := stale.durableRowAt("job_finished"); ok {
		t.Fatalf("a result holding the empty list answered %+v from another list's index, want no answer", row)
	}
	// The walk is what a refusal falls back to, and it must agree: the empty list
	// does not name the job.
	if row := older.durableRows(map[string]struct{}{"job_finished": {}})["job_finished"]; row.row {
		t.Fatal("the empty list claims a row for job_finished")
	}
	// The index's own list is still answered directly.
	current := liveJobFoldResult{jobs: newer, positions: positions}
	row, ok := current.durableRowAt("job_finished")
	if !ok || !row.row || row.chunk != 0 || row.slot != 0 {
		t.Fatalf("the owning list answered %+v, %v, want the row at chunk 0 slot 0", row, ok)
	}
}

// TestActivityJobList_SubstitutionCannotReachPastItsOwnList is B2's panic, at
// the site that panics.
//
// A request holds the fold its page was built from. A concurrent request can
// publish a later one — a completion for a job whose start was truncated away —
// while the first is still rendering. If the first then resolves that job's slot
// in the CURRENT fold and applies it to its own list, the slot belongs to a list
// that need not have that row at all. When the first request's list is empty,
// there is no slot to write: it panics on an index the durable row named.
//
// Scoping the lookup to the list the result carries turns that into an ordinary
// "this list does not name it", which the stranded fallback already handles.
func TestActivityJobList_SubstitutionCannotReachPastItsOwnList(t *testing.T) {
	t.Parallel()
	const root = "root"
	empty := newActivityJobList(nil)
	if len(empty.chunks) != 0 {
		t.Fatalf("fixture list has %d chunks, want the empty case", len(empty.chunks))
	}
	// The slot a concurrent publication named: chunk 0, slot 0, of a list this
	// request does not hold.
	row := activityJobDurableRow{row: true, chunk: 0, slot: 0, class: activityShellJob, owner: root}
	live := map[string]*jobstore.JobRecord{
		"job_finished": {JobID: "job_finished", Type: jobstore.JobShell, OwnerSessionID: root},
	}
	// The substitution must not panic, and must not invent a row in the empty
	// list.
	got := empty.withLiveRebuilt(live, nil, map[string]activityJobDurableRow{"job_finished": row})
	if got == nil {
		t.Fatal("substituting onto an empty list returned nil")
	}
	var ids []string
	for _, chunk := range got.chunks {
		for _, rec := range chunk.records {
			if rec != nil {
				ids = append(ids, rec.JobID)
			}
		}
	}
	if len(ids) != 0 {
		t.Fatalf("the empty list gained %v from a substitution, want none", ids)
	}
	// The guard is what makes that reachable instead of a panic: the result does
	// not accept a slot from a list it does not carry.
	stale := liveJobFoldResult{jobs: empty}
	if _, ok := stale.durableRowAt("job_finished"); ok {
		t.Fatal("a result with no position index answered from one")
	}
}
