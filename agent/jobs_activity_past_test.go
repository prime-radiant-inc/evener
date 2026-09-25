package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

func TestLoadSessionJobActivityTree_FollowsOnlyStableDelegateChildren(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootpast"
	childID := "childpast"
	strayID := "straypast"
	started := time.Unix(100, 0).UTC()
	ended := started.Add(time.Second)

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, childID, "child task"),
		pastStableDescriptor(rootID, strayID, "stray task"),
	)
	if err := os.WriteFile(transcriptPath(stateDir, childID), []byte("malformed eligible child transcript\n"), 0o600); err != nil {
		t.Fatalf("corrupt child transcript: %v", err)
	}
	if err := os.Remove(transcriptPath(stateDir, strayID)); err != nil {
		t.Fatalf("remove stray transcript: %v", err)
	}
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_shell", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started, Description: "root shell"},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "child shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_child_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.SessionID != rootID || len(got.Root.Entries) != 3 {
		t.Fatalf("root activity = %+v", got.Root)
	}
	child := pastFindDelegate(t, got.Root, childID)
	if child.Child == nil || len(child.Child.Entries) != 1 || child.Child.Entries[0].Job == nil || child.Child.Entries[0].Job.JobID != "job_child_shell" {
		t.Fatalf("child subtree = %+v", child)
	}
	stray := pastFindDelegate(t, got.Root, strayID)
	if stray.Child != nil || stray.Branch.Error == "" {
		t.Fatalf("missing child delegate = %+v", stray)
	}
}

func TestLoadSessionJobActivityTree_RejectsOutOfStateDirChild(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootboundary"
	childID := "childboundary"
	outsideStateDir := t.TempDir()
	started := time.Unix(200, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "outside"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	writeRawSessionMeta(t, filepath.Join(stateDir, "sessions", childID+".meta.json"), schema.SessionMeta{
		ID: childID, Name: "Outside", WorktreePath: filepath.Join(outsideStateDir, "evil"),
	})
	childJobsPath := s1cov_writeJobLog(t, outsideStateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_outside_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	before, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := pastFindDelegate(t, got.Root, childID)
	if delegate.Child != nil || delegate.Branch.Error == "" {
		t.Fatalf("delegate = %+v", delegate)
	}
	after, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatalf("outside job log changed: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

func TestLoadSessionJobActivityTree_UsesMaxPersistedRootRevisionAcrossDescendants(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootrevision"
	childID := "childrevision"
	started := time.Unix(250, 0).UTC()
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "revision"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	savePastActivityMetaWithTreeRevision(t, stateDir, rootID, "Root", "", 3)
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 7)

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 7 {
		t.Fatalf("revision=%d, want 7", got.Revision)
	}
}

// TestLoadSessionJobActivityTree_ReadsSharedDelegateJournalOncePerRoot
// asserts loadHistoricalActivityBase scans the shared root delegates.jsonl
// exactly once per root, not once per visited session: root -> child1 ->
// child2 are three visited sessions sharing one delegates.jsonl at the
// root, and re-reading/re-folding it per session would make loading
// O(sessions x delegate events).
func TestLoadSessionJobActivityTree_ReadsSharedDelegateJournalOncePerRoot(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootonce"
	child1ID := "child1once"
	child2ID := "child2once"
	started := time.Unix(300, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, child1ID, "child1 task"),
		pastStableDescriptor(child1ID, child2ID, "child2 task"),
	)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child1ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child1", Type: jobstore.JobShell, OwnerSessionID: child1ID, VisibleToSession: child1ID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child2ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child2", Type: jobstore.JobShell, OwnerSessionID: child2ID, VisibleToSession: child2ID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, child1ID, "Child1", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, child2ID, "Child2", rootID, 0)

	var delegateScans int32
	original := scanDelegateJournal
	scanDelegateJournal = func(ctx context.Context, path string, fromOffset int64, limits delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
		atomic.AddInt32(&delegateScans, 1)
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanDelegateJournal = original }()

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	child1 := pastFindDelegate(t, got.Root, child1ID)
	if child1.Child == nil {
		t.Fatalf("expected child1 subtree, got %+v", child1)
	}
	child2 := pastFindDelegate(t, *child1.Child, child2ID)
	if child2.Child == nil {
		t.Fatalf("expected child2 subtree, got %+v", child2)
	}
	if delegateScans != 1 {
		t.Fatalf("delegates.jsonl scanned %d times across 3 visited sessions, want exactly 1", delegateScans)
	}
}

// TestLoadSessionJobActivityTree_StopsOpeningLaterSessionsAfterCancellation
// asserts cancellation is checked between descendant sessions, not only
// between records within one journal: once the request context is
// canceled, no later session's jobs.jsonl is opened.
func TestLoadSessionJobActivityTree_StopsOpeningLaterSessionsAfterCancellation(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootcancel"
	child1ID := "child1cancel"
	child2ID := "child2cancel"
	started := time.Unix(400, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, child1ID, "child1 task"),
		pastStableDescriptor(rootID, child2ID, "child2 task"),
	)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child1ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child1", Type: jobstore.JobShell, OwnerSessionID: child1ID, VisibleToSession: child1ID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child2ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child2", Type: jobstore.JobShell, OwnerSessionID: child2ID, VisibleToSession: child2ID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, child1ID, "Child1", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, child2ID, "Child2", rootID, 0)

	ctx, cancel := context.WithCancel(context.Background())
	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scannedPaths = append(scannedPaths, path)
		cancel() // cancel once the first session's own journal is reached
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	if _, err := LoadSessionJobActivityTree(ctx, stateDir, rootID, appwire.JobsListParams{}); err == nil {
		t.Fatal("expected an error from a canceled request")
	}
	if len(scannedPaths) != 1 {
		t.Fatalf("scanned %d job journals after cancellation, want exactly 1 (root only): %v", len(scannedPaths), scannedPaths)
	}
}

// TestLoadSessionJobActivityTree_BoundsSingleSessionScanAtWorkUnitBudget
// asserts PROJECTION bounds one response page at activityMaxWorkUnits
// entries and mints a continuation that actually advances, even though
// LOADING itself never stops at the budget: a session's jobs.jsonl
// carries more valid job_started records than activityMaxWorkUnits, and
// the whole session folds via historicalJobFoldCache regardless of size
// (200k events is a normal Tuesday; truncating legitimate history is
// unacceptable). Decoding the minted continuation back out must show
// ResumeIndex == activityMaxWorkUnits, not an opaque token a client can't
// reason about.
func TestLoadSessionJobActivityTree_BoundsSingleSessionScanAtWorkUnitBudget(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootbudget"
	started := time.Unix(500, 0).UTC()

	events := make([]jobstore.Event, 0, activityMaxWorkUnits+1)
	for i := range activityMaxWorkUnits + 1 {
		jobID := fmt.Sprintf("job_%d", i)
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
			OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v, want a truncated tree, not a hard error", err)
	}
	if !got.Root.Branch.Truncated {
		t.Fatalf("Root.Branch.Truncated = false, want true (projection stopped at the work-unit budget)")
	}
	if len(got.Root.Entries) != activityMaxWorkUnits {
		t.Fatalf("got %d entries, want exactly activityMaxWorkUnits=%d", len(got.Root.Entries), activityMaxWorkUnits)
	}
	if got.Root.Branch.Continuation == "" {
		t.Fatal("Root.Branch.Continuation is empty, want a real, advancing continuation")
	}
	cont, err := decodeActivityContinuation(got.Root.Branch.Continuation, rootID)
	if err != nil {
		t.Fatalf("decodeActivityContinuation: %v", err)
	}
	if cont.ResumeIndex != activityMaxWorkUnits {
		t.Fatalf("continuation ResumeIndex = %d, want %d (all %d rendered entries accounted for)", cont.ResumeIndex, activityMaxWorkUnits, activityMaxWorkUnits)
	}
}

// TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted
// covers the load-phase traversal-breadth bound: a root with more direct
// delegate children than activityMaxWorkUnits must stop VISITING them
// (never open the ones past the budget) rather than only trimming the
// rendered page afterward, so an unbounded tree of small sessions cannot
// force O(sessions) file opens before projection's own budget ever
// applies. The budget counts one unit per session VISITED during load,
// not one per job record loaded (see historicalActivityCache's doc
// comment: a session's own journal is never capped at load time), so
// this test exhausts it with many CHILDREN rather than many JOBS.
func TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootwidebudget"
	started := time.Unix(600, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	descriptors := make([]delegatestore.Descriptor, 0, activityMaxWorkUnits+1)
	childJobsPaths := make([]string, 0, activityMaxWorkUnits+1)
	for i := range activityMaxWorkUnits + 1 {
		childID := fmt.Sprintf("childwidebudget%d", i)
		descriptors = append(descriptors, pastStableDescriptor(rootID, childID, "may never be opened"))
		childJobsPaths = append(childJobsPaths, s1cov_writeJobLog(t, stateDir, childID,
			jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + childID, Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
		))
		savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)
	}
	writePastStableDelegates(t, stateDir, rootID, descriptors...)

	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scannedPaths = append(scannedPaths, path)
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	// Root's own rendered page ALSO truncates: 1 job + activityMaxWorkUnits+1
	// delegate entries is itself over the (separate, projection-side)
	// activityMaxWorkUnits budget.
	if !got.Root.Branch.Truncated {
		t.Fatalf("Root.Branch.Truncated = false, want true")
	}
	// root's own journal (1) + at most activityMaxWorkUnits children (the
	// load-phase budget's own unit count), never all activityMaxWorkUnits+1.
	wantScanned := activityMaxWorkUnits + 1
	if len(scannedPaths) != wantScanned {
		t.Fatalf("scanned %d job journals, want exactly %d (root plus at most activityMaxWorkUnits=%d children; at least one child must never be visited): %v", len(scannedPaths), wantScanned, activityMaxWorkUnits, scannedPaths)
	}
	openedChildJournal := make(map[string]bool, len(scannedPaths))
	for _, p := range scannedPaths {
		openedChildJournal[p] = true
	}
	neverOpened := 0
	for _, p := range childJobsPaths {
		if !openedChildJournal[p] {
			neverOpened++
		}
	}
	if neverOpened != 1 {
		t.Fatalf("%d child journals were never opened, want exactly 1 (activityMaxWorkUnits+1 children, activityMaxWorkUnits budget)", neverOpened)
	}
}

// TestLoadSessionJobActivityTree_ContinuationReportsTokenRevisionWithoutFullWalk
// pins that a continuation page reports the revision its token was minted
// against and does NOT re-walk the tree to recompute one. A historical
// revision is a max over a bounded, work-budget-shaped snapshot, so
// recomputing it on resume can differ for the same valid continuation
// (resolving the path's hops spends budget before the walk; a descendant
// appended between requests moves the max). A consumer that fences a page by
// revision would then discard a valid page and refetch the root forever, so
// the revision is echoed from the token instead -- and the walk that used to
// recompute it is gone.
//
// Fixture: root has exactly activityMaxWorkUnits (2000) direct stable
// delegates -- one continuation target ("aaaspecial", named to sort first)
// plus activityMaxWorkUnits-1 (1999) plain "wide" leaves (named to sort after
// it). The continuation resolves to aaaspecial via exactly one hop. The walk
// the revision used to require scanned every sibling; now none of the wide
// siblings is opened, and the page carries the token's revision verbatim.
func TestLoadSessionJobActivityTree_ContinuationReportsTokenRevisionWithoutFullWalk(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "revisionsharebudgetroot"
	started := time.Unix(600, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	specialID := "aaaspecial"
	var descriptors []delegatestore.Descriptor
	descriptors = append(descriptors, pastStableDescriptor(rootID, specialID, "continuation target"))
	specialJobsPath := s1cov_writeJobLog(t, stateDir, specialID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + specialID, Type: jobstore.JobShell, OwnerSessionID: specialID, VisibleToSession: specialID, StartedAt: &started},
	)
	savePastActivityMetaWithTreeRevision(t, stateDir, specialID, "Special", rootID, 0)

	const wideCount = activityMaxWorkUnits - 1
	wideJobsPaths := make([]string, 0, wideCount)
	for i := range wideCount {
		childID := fmt.Sprintf("widechild%04d", i)
		descriptors = append(descriptors, pastStableDescriptor(rootID, childID, "wide sibling"))
		wideJobsPaths = append(wideJobsPaths, s1cov_writeJobLog(t, stateDir, childID,
			jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + childID, Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
		))
		savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Wide", rootID, 0)
	}
	writePastStableDelegates(t, stateDir, rootID, descriptors...)

	const tokenRevision = 7
	cont := activityContinuation{
		Version: activityContinuationVersion, RootID: rootID, SessionID: specialID,
		Path: []string{"dlg_" + specialID}, Revision: tokenRevision,
	}
	token := encodeActivityContinuation(cont)

	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scannedPaths = append(scannedPaths, path)
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: token})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	if tree.Revision != tokenRevision {
		t.Fatalf("Revision = %d, want %d (a continuation must report the revision its token was minted against, not a recomputed one)", tree.Revision, tokenRevision)
	}

	scanned := make(map[string]bool, len(scannedPaths))
	for _, p := range scannedPaths {
		scanned[p] = true
	}
	if !scanned[specialJobsPath] {
		t.Fatalf("the continuation target's journal %q was never scanned; the page was not loaded at all: %v", specialJobsPath, scannedPaths)
	}
	neverScanned := 0
	for _, p := range wideJobsPaths {
		if !scanned[p] {
			neverScanned++
		}
	}
	if neverScanned != wideCount {
		t.Fatalf("neverScanned = %d, want all %d wide siblings unopened -- a continuation must not re-walk the tree to recompute its revision: %v", neverScanned, wideCount, scannedPaths)
	}
}

// TestLoadSessionJobActivityTree_BoundsRecursionDepth asserts a chain of
// sessions longer than activityMaxNewDepth is not recursed into during
// LOAD (its jobs.jsonl must never be opened past that depth), not only
// trimmed afterward in projection. Asserting on the wire-visible depth
// alone would not catch a load-phase regression here: projection ALREADY
// trims a too-deep tree to activityMaxNewDepth on its own, independently
// of load-time behavior, so a tree loaded fully unbounded would still
// render with the same, already-truncated depth. The scan-call count is
// what actually distinguishes "loaded everything, trimmed at render
// time" from "never opened the files past the depth bound."
func TestLoadSessionJobActivityTree_BoundsRecursionDepth(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Unix(700, 0).UTC()

	const chainLen = activityMaxNewDepth + 5
	sessionIDs := make([]string, chainLen)
	for i := range sessionIDs {
		sessionIDs[i] = fmt.Sprintf("depthchain%d", i)
	}
	var descriptors []delegatestore.Descriptor
	for i, id := range sessionIDs {
		s1cov_writeJobLog(t, stateDir, id,
			jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + id, Type: jobstore.JobShell, OwnerSessionID: id, VisibleToSession: id, StartedAt: &started},
		)
		if i == 0 {
			savePastActivityMeta(t, stateDir, id, "Root")
		} else {
			savePastActivityMetaWithTreeRevision(t, stateDir, id, "Node", sessionIDs[0], 0)
		}
		if i+1 < len(sessionIDs) {
			descriptors = append(descriptors, pastStableDescriptor(id, sessionIDs[i+1], "next"))
		}
	}
	writePastStableDelegates(t, stateDir, sessionIDs[0], descriptors...)

	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		scannedPaths = append(scannedPaths, path)
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionIDs[0], appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	// Nodes at depth 0..activityMaxNewDepth (inclusive: activityMaxNewDepth+1
	// nodes total) load fully; the node AT activityMaxNewDepth is the one
	// whose own delegates don't get descended into, matching projection's
	// existing depth semantics (kept identical on purpose, see below).
	wantScanned := activityMaxNewDepth + 1
	if len(scannedPaths) != wantScanned {
		t.Fatalf("scanned %d job journals, want exactly %d (chain nodes past the depth bound must never be opened): %v", len(scannedPaths), wantScanned, scannedPaths)
	}
	depth := 0
	session := got.Root
	var stoppedAt *appwire.JobActivityDelegate
	for {
		var delegate *appwire.JobActivityDelegate
		for i := range session.Entries {
			if session.Entries[i].Delegate != nil {
				delegate = session.Entries[i].Delegate
			}
		}
		if delegate == nil || delegate.Child == nil {
			stoppedAt = delegate
			break
		}
		session = *delegate.Child
		depth++
	}
	if depth > activityMaxNewDepth {
		t.Fatalf("loaded chain %d levels deep, want at most activityMaxNewDepth=%d", depth, activityMaxNewDepth)
	}
	// The delegate at the depth boundary reports an honest truncated branch
	// that says where to read the rest — not a generic "child session
	// unavailable" branch error, and not a continuation: a depth-truncated
	// token would name that child as a fresh root at position 0, which is
	// the page a request for the child returns anyway, and this page never
	// loaded that child's journals to fence one with.
	if stoppedAt == nil {
		t.Fatal("chain never reached a depth-truncated delegate")
	}
	if stoppedAt.Branch.Error != "" {
		t.Fatalf("depth-boundary delegate branch.Error = %q, want empty (the bound is not a failure)", stoppedAt.Branch.Error)
	}
	if !stoppedAt.Branch.Truncated {
		t.Fatalf("depth-boundary delegate branch.Truncated = false, want true")
	}
	if stoppedAt.Branch.Continuation != "" {
		t.Fatalf("depth-boundary delegate offers a continuation (%q); the bound hands the reader the session to request instead", stoppedAt.Branch.Continuation)
	}
	if len(stoppedAt.Diagnostics) == 0 || !strings.Contains(stoppedAt.Diagnostics[0], stoppedAt.ChildSessionID) {
		t.Fatalf("depth-boundary delegate diagnostics = %q, want one naming the session to request", stoppedAt.Diagnostics)
	}
	// And what that diagnostic tells the reader to do returns the page it
	// promises: the child rendered from its own top, with its own budget.
	direct, err := LoadSessionJobActivityTree(context.Background(), stateDir, stoppedAt.ChildSessionID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("requesting %s directly, as the diagnostic says to: %v", stoppedAt.ChildSessionID, err)
	}
	if direct.Root.SessionID != stoppedAt.ChildSessionID {
		t.Fatalf("direct request returned session %q, want %q", direct.Root.SessionID, stoppedAt.ChildSessionID)
	}
	if len(direct.Root.Entries) == 0 {
		t.Fatalf("direct request for %s returned no entries; the diagnostic promises the page the bound withheld", stoppedAt.ChildSessionID)
	}
}

// TestLoadSessionJobActivityTree_ContinuationAtMaxDepthLoadsTargetsOwnChildren
// asserts a continuation whose path reaches exactly activityMaxNewDepth
// hops still lets the LOAD phase load the target's OWN children, rather
// than treating them as already past the depth bound.
// buildActivityFullSnapshot computes its load-phase depth from
// len(visited)-1 -- the ancestor chain from the ORIGINAL root -- while
// projection resets depth to 0 at the continuation target (startDepth =
// -len(cont.Path), reaching 0 exactly at the target); without accounting
// for that reset, the load phase would treat the target's children as
// depth == activityMaxNewDepth (already past the bound) and replace them
// with empty placeholders, even though projection -- and any sane
// reading of "how deep beneath the page I'm resuming into" -- allows a
// full activityMaxNewDepth further beneath the target.
func TestLoadSessionJobActivityTree_ContinuationAtMaxDepthLoadsTargetsOwnChildren(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Unix(700, 0).UTC()

	// index activityMaxNewDepth is the continuation TARGET (reached via
	// exactly activityMaxNewDepth hops from the root); index
	// activityMaxNewDepth+1 is the target's OWN child -- the node that
	// must be loaded for real, not left as a silent placeholder.
	const chainLen = activityMaxNewDepth + 2
	sessionIDs := make([]string, chainLen)
	for i := range sessionIDs {
		sessionIDs[i] = fmt.Sprintf("maxdepthchain%d", i)
	}
	var descriptors []delegatestore.Descriptor
	for i, id := range sessionIDs {
		s1cov_writeJobLog(t, stateDir, id,
			jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + id, Type: jobstore.JobShell, OwnerSessionID: id, VisibleToSession: id, StartedAt: &started},
		)
		if i == 0 {
			savePastActivityMeta(t, stateDir, id, "Root")
		} else {
			savePastActivityMetaWithTreeRevision(t, stateDir, id, "Node", sessionIDs[0], 0)
		}
		if i+1 < len(sessionIDs) {
			descriptors = append(descriptors, pastStableDescriptor(id, sessionIDs[i+1], "next"))
		}
	}
	writePastStableDelegates(t, stateDir, sessionIDs[0], descriptors...)

	// writePastStableDelegates assigns delegate IDs as "dlg_" + childSessionID.
	path := make([]string, activityMaxNewDepth)
	for i := range path {
		path[i] = "dlg_" + sessionIDs[i+1]
	}
	targetID := sessionIDs[activityMaxNewDepth]
	cont := activityContinuation{
		Version: activityContinuationVersion, RootID: sessionIDs[0], SessionID: targetID, Path: path,
	}
	token := encodeActivityContinuation(cont)

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionIDs[0], appwire.JobsListParams{Continuation: token})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}

	// Descend Root -> Child -> Child -> ... exactly activityMaxNewDepth
	// times to reach the continuation target's own projected node.
	session := got.Root
	for i := range activityMaxNewDepth {
		var delegate *appwire.JobActivityDelegate
		for j := range session.Entries {
			if session.Entries[j].Delegate != nil {
				delegate = session.Entries[j].Delegate
			}
		}
		if delegate == nil || delegate.Child == nil {
			t.Fatalf("descent stopped at hop %d, want to reach the continuation target at hop %d", i, activityMaxNewDepth)
		}
		session = *delegate.Child
	}
	if session.SessionID != targetID {
		t.Fatalf("reached session %q, want the continuation target %q", session.SessionID, targetID)
	}

	// The target's OWN child (one level beneath it, well within
	// activityMaxNewDepth relative to the target) must be genuinely
	// loaded -- not a depth-truncated placeholder.
	var targetDelegate *appwire.JobActivityDelegate
	for i := range session.Entries {
		if session.Entries[i].Delegate != nil {
			targetDelegate = session.Entries[i].Delegate
		}
	}
	if targetDelegate == nil {
		t.Fatal("continuation target has no delegate entry for its own child")
	}
	if targetDelegate.Branch.Truncated {
		t.Fatalf("target's own child branch.Truncated = true, want false -- it is only 1 level beneath the continuation target, nowhere near activityMaxNewDepth relative to it")
	}
	if targetDelegate.Child == nil {
		t.Fatal("target's own child was not loaded at all")
	}
	wantJobID := "job_" + sessionIDs[activityMaxNewDepth+1]
	found := false
	for _, entry := range targetDelegate.Child.Entries {
		if entry.Job != nil && entry.Job.JobID == wantJobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("target's own child entries = %+v, want to find job %q -- the child was loaded as an empty placeholder instead of its real content", targetDelegate.Child.Entries, wantJobID)
	}
}

// TestLoadSessionJobActivityTree_PropagatesCancellationFromDescendant
// asserts a canceled request surfaces as a real error even when the
// cancellation lands while loading a DESCENDANT, not the root: a
// cancellation-during-the-root-scan test alone would never exercise the
// buildActivityFullSnapshot loop's handling of a descendant's own
// context.Canceled, which must propagate rather than being caught into
// snapshot.Errors and returned as a silently-partial tree with err ==
// nil.
func TestLoadSessionJobActivityTree_PropagatesCancellationFromDescendant(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootdesccancel"
	child1ID := "child1desccancel"
	child2ID := "child2desccancel"
	started := time.Unix(800, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, child1ID, "child1 task"),
		pastStableDescriptor(child1ID, child2ID, "child2 task"),
	)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child1ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child1", Type: jobstore.JobShell, OwnerSessionID: child1ID, VisibleToSession: child1ID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child2ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child2", Type: jobstore.JobShell, OwnerSessionID: child2ID, VisibleToSession: child2ID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, child1ID, "Child1", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, child2ID, "Child2", rootID, 0)

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, fromOffset int64, limits jobstore.ScanLimits) ([]jobstore.Event, int64, error) {
		calls++
		if calls == 2 {
			// Cancel while loading child1's OWN journal — a descendant, not
			// the root — the exact case the swallow-into-Errors bug missed.
			cancel()
		}
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanJobJournal = original }()

	if _, err := LoadSessionJobActivityTree(ctx, stateDir, rootID, appwire.JobsListParams{}); err == nil {
		t.Fatal("expected a non-nil error when a descendant's load is canceled, got nil (silently-partial success)")
	}
	if calls != 2 {
		t.Fatalf("scanJobJournal called %d times, want exactly 2 (root, then child1; child2 must never be reached once cancellation propagates)", calls)
	}
}

// TestDecodeActivityContinuation_RejectsPathLongerThanMaxDepth asserts
// continuation paths are bounded: they are client-controlled, so without
// this cap a long valid path could force buildActivityContinuationAt to
// open arbitrarily many historical sessions' files with no bound at all
// (ordinary, non-continuation traversal's own depth limit is enforced by
// buildActivityFullSnapshot's recursion, which this path-following code
// doesn't go through).
func TestDecodeActivityContinuation_RejectsPathLongerThanMaxDepth(t *testing.T) {
	// activityMaxContinuationPathLength (activityMaxNewDepth+1), not
	// activityMaxNewDepth itself, is the real limit: the extra hop is slack
	// this build still accepts (see that constant's doc comment), so this
	// test's own path must exceed THAT to prove genuinely-too-long paths are
	// rejected without also rejecting a token the decoder still honours.
	path := make([]string, activityMaxContinuationPathLength+1)
	for i := range path {
		path[i] = fmt.Sprintf("hop%d", i)
	}
	token := encodeActivityContinuation(activityContinuation{
		Version: activityContinuationVersion, RootID: "root", SessionID: "session", Path: path,
	})
	if _, err := decodeActivityContinuation(token, "root"); err == nil {
		t.Fatal("expected an error for a continuation path longer than activityMaxContinuationPathLength")
	}
}

// TestBuildActivityContinuationAt_ExhaustedBudgetStopsBeforeLoadingMoreHops
// asserts each continuation hop is charged against the shared load
// budget the same way buildActivityFullSnapshot charges each child it
// visits -- otherwise loadActivityBase (which opens files) would run
// once per hop with no bound of its own. A budget that is already
// exhausted before ANY hop is resolved must stop immediately, never
// reaching loadActivityBase for even the first one.
func TestBuildActivityContinuationAt_ExhaustedBudgetStopsBeforeLoadingMoreHops(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "budgetpathroot"
	childID := "budgetpathchild"
	started := time.Unix(6_000_000_000, 0).UTC()
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "task"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	// The child has its own valid, readable jobs.jsonl too: without this,
	// loadActivityBase's "child session unavailable" error (a MISSING FILE,
	// unrelated to budget) would make this test pass for the wrong reason.
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)

	cache := newHistoricalActivityCache(context.Background(), rootID)
	cache.budget.usedWork = cache.budget.maxWorkUnits // pre-exhausted

	// Path holds the DELEGATE ID (writePastStableDelegates assigns
	// "dlg_" + childSessionID here), not the child session ID itself --
	// buildActivityContinuationAt looks each hop up in
	// loaded.snapshot.StableDelegates, which is keyed by delegate ID.
	cont := activityContinuation{Version: activityContinuationVersion, RootID: rootID, SessionID: childID, Path: []string{"dlg_" + childID}}
	root := activitySessionLocator{stateDir: stateDir, sessionID: rootID}
	if _, _, _, err := buildActivityContinuationAt(root, cont, 0, map[string]bool{rootID: true}, false, cache); err == nil {
		t.Fatal("expected an error: the load budget is already exhausted before resolving even the first continuation hop")
	}
}

// TestLoadSessionJobActivityTree_OversizedDelegateJournalLineDegradesWithDiagnostic
// asserts an oversized single line in the shared delegates.jsonl
// (tripping MaxLineBytes, which is always-on) does not hard-fail the
// whole activity tree: the posture is "loud but CONTAINED" -- the tree
// still renders, and the diagnostic is surfaced prominently via the same
// Diagnostics mechanism the torn-tail case already uses.
func TestLoadSessionJobActivityTree_OversizedDelegateJournalLineDegradesWithDiagnostic(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "activitypathologicaldelegateroot"
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, "child1", "a task long enough to exceed a tiny test line cap"))
	started := time.Unix(7_000_000_000, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	original := scanDelegateJournal
	scanDelegateJournal = func(ctx context.Context, path string, fromOffset int64, limits delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
		limits.MaxLineBytes = 20
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanDelegateJournal = original }()

	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v, want nil error -- an oversized delegate journal line must degrade the branch, not hard-fail the whole tree", err)
	}
	if len(tree.Root.Entries) == 0 {
		t.Fatalf("Root.Entries is empty, want the root's own job still rendered despite the delegate journal failure")
	}
	found := false
	for _, d := range tree.Root.Diagnostics {
		if strings.Contains(d, "delegates.jsonl") && strings.Contains(d, "line") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Root.Diagnostics = %v, want one identifying the oversized delegates.jsonl line, surfaced prominently rather than a silent empty delegate list", tree.Root.Diagnostics)
	}
}

func TestLoadSessionJobActivityTree_ForwardedFallbackSurfacesAtRoot(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootforwarded"
	childID := "childforwarded"
	started := time.Unix(400, 0).UTC()
	ended := started.Add(time.Second)

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "gone child"))
	// The root journal forwarded the descendant's job at start and terminal
	// time; the descendant's own jobs.jsonl never made it to this state dir, so
	// the forwarded copy at the root is the only surviving record.
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_forwarded_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: rootID, StartedAt: &started, Description: "forwarded"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_forwarded_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var matches []appwire.JobActivityJob
	for _, entry := range got.Root.Entries {
		if entry.Job != nil && entry.Job.JobID == "job_forwarded_shell" {
			matches = append(matches, *entry.Job)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("forwarded job appears %d times at root, want 1; entries=%+v", len(matches), got.Root.Entries)
	}
	job := matches[0]
	if job.Authority != string(jobstore.AuthorityForwardedFallback) || !job.Incomplete {
		t.Fatalf("job = %+v, want authority=%q incomplete=true", job, jobstore.AuthorityForwardedFallback)
	}
	if !slices.Contains(job.IntegrityReasons, "owner_unavailable") {
		t.Fatalf("integrity reasons = %v, want owner_unavailable", job.IntegrityReasons)
	}
}

// TestLoadSessionJobActivityTree_EqualStartedAtOrdersByJobID pins the
// cross-journal ordering guarantee: once records are merged from more than
// one owner journal, a per-journal append sequence is only meaningful within
// a single journal, so equal-StartedAt records must tie-break on JobID. The
// root owns "job_z_root" and holds the forwarded copy of the absent child's
// "job_a_child"; both start at the same instant. A per-journal sequence
// tie-break would order by the root journal's append position (job_z_root
// before job_a_child, since the forwarded event is appended later); the
// (StartedAt, JobID) tie-break renders job_a_child first.
func TestLoadSessionJobActivityTree_EqualStartedAtOrdersByJobID(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "roottie"
	childID := "childtie"
	started := time.Unix(500, 0).UTC()
	ended := started.Add(time.Second)

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "gone child"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_z_root", Type: jobstore.JobShell, OwnerSessionID: rootID, StartedAt: &started, Description: "root"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_z_root", Status: jobstore.StatusCompleted, EndedAt: &ended},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_a_child", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: rootID, StartedAt: &started, Description: "forwarded"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_a_child", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range got.Root.Entries {
		if entry.Job != nil {
			ids = append(ids, entry.Job.JobID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("root job entries = %v, want both jobs", ids)
	}
	if ids[0] != "job_a_child" || ids[1] != "job_z_root" {
		t.Fatalf("entry order = %v, want [job_a_child job_z_root] (equal StartedAt ties break on JobID, not a per-journal sequence)", ids)
	}
}

func pastStableDescriptor(ownerSessionID, childSessionID, task string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		ChildSessionID:   childSessionID,
		TranscriptRef:    encodeRef("", childSessionID),
		OwnerSessionID:   ownerSessionID,
		VisibleSessionID: ownerSessionID,
		Task:             task,
		AgentType:        "general",
		ToolNameCeiling:  []string{"communicate"},
		Resumable:        true,
	}
}

func writePastStableDelegates(t *testing.T, stateDir, rootSessionID string, descriptors ...delegatestore.Descriptor) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		writer, err := transcript.NewWriter(transcriptPath(stateDir, descriptor.ChildSessionID), transcript.Header{
			SessionID:       descriptor.ChildSessionID,
			ParentSessionID: descriptor.OwnerSessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := delegatestore.Open(filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]delegatestore.Event, 0, len(descriptors))
	for i, descriptor := range descriptors {
		events = append(events, delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			TS:         time.Unix(int64(i+1), 0).UTC(),
			DelegateID: "dlg_" + strings.TrimPrefix(descriptor.ChildSessionID, "child"),
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		})
	}
	if _, _, err := store.AppendBatch(state, events); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func savePastActivityMeta(t *testing.T, stateDir, sessionID, name string) {
	t.Helper()
	savePastActivityMetaWithTreeRevision(t, stateDir, sessionID, name, "", 0)
}

// TestLoadSessionJobActivityTree_WorkContinuationWalksRetainedJobsOnce drives
// the public historical entry point through a session whose retained job count
// exceeds activityMaxWorkUnits, following each minted continuation exactly as a
// client would. The first page stops at the work-unit bound and must hand back a
// continuation that resumes AFTER the entries it already rendered; following it
// must deliver every remaining job exactly once, in order, and terminate rather
// than replaying the page's prefix. Every page must also stay within the
// activityMaxWorkUnits bound, and the walk's revision -- seeded nonzero so a
// regression that echoes the first page's number but mints later tokens with
// 0 cannot pass -- must be stable on every page AND inside every minted
// continuation.
func TestLoadSessionJobActivityTree_WorkContinuationWalksRetainedJobsOnce(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "workbudgetwalkroot"
	const jobCount = activityMaxWorkUnits + 1
	const walkRevision = 5
	events := make([]jobstore.Event, 0, jobCount)
	for i := range jobCount {
		ts := time.Unix(int64(1000+i), 0).UTC()
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%04d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMetaWithTreeRevision(t, stateDir, rootID, "Root", "", walkRevision)

	var delivered []string
	seenContinuations := map[string]bool{}
	continuation := ""
	pages := 0
	for {
		pages++
		if pages > 3 {
			t.Fatalf("walked %d pages without terminating -- a work-unit continuation is replaying instead of advancing", pages)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(tree.Root.Entries) > activityMaxWorkUnits {
			t.Fatalf("page %d returned %d entries, want at most activityMaxWorkUnits=%d", pages, len(tree.Root.Entries), activityMaxWorkUnits)
		}
		if tree.Revision != walkRevision {
			t.Fatalf("page %d reports revision %d, want %d -- a continuation page must report the revision its walk began at, or a revision-fencing client discards valid pages and refetches the root forever", pages, tree.Revision, walkRevision)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry without a Job: %+v", pages, entry)
			}
			delivered = append(delivered, entry.Job.JobID)
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			break
		}
		if seenContinuations[next] {
			t.Fatalf("page %d re-minted a continuation already handed out -- the walk can never advance past it", pages)
		}
		seenContinuations[next] = true
		cont, err := decodeActivityContinuation(next, rootID)
		if err != nil {
			t.Fatalf("page %d: decodeActivityContinuation: %v", pages, err)
		}
		if cont.Revision != walkRevision {
			t.Fatalf("page %d minted a continuation carrying revision %d, want %d -- the token must carry the walk's revision, or the next page's echo would fall back to 0", pages, cont.Revision, walkRevision)
		}
		continuation = next
	}
	if pages < 2 {
		t.Fatalf("got %d page(s), want at least 2 -- the fixture must be large enough to exhaust the work-unit budget", pages)
	}
	if len(delivered) != jobCount {
		t.Fatalf("delivered %d entries across %d pages, want exactly %d (zero overlap, zero gap): %v", len(delivered), pages, jobCount, delivered)
	}
	for i, id := range delivered {
		want := fmt.Sprintf("job_%04d", i)
		if id != want {
			t.Fatalf("delivered[%d] = %q, want %q -- every page must resume after the last rendered entry: %v", i, id, want, delivered)
		}
	}
}

func savePastActivityMetaWithTreeRevision(t *testing.T, stateDir, sessionID, name, rootID string, revision uint64) {
	t.Helper()
	meta := schema.SessionMeta{ID: sessionID, ProfileID: "openai", Model: "gpt-5.2", Name: name, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(), JobTreeRevision: revision}
	if strings.TrimSpace(rootID) != "" {
		meta.JobTreeRootSessionID = rootID
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta(%s): %v", sessionID, err)
	}
}

func pastFindDelegate(t *testing.T, root appwire.JobActivitySession, childID string) appwire.JobActivityDelegate {
	t.Helper()
	for _, entry := range root.Entries {
		if entry.Delegate != nil && entry.Delegate.ChildSessionID == childID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate child=%q in %+v", childID, root.Entries)
	return appwire.JobActivityDelegate{}
}

func writeRawSessionMeta(t *testing.T, path string, meta schema.SessionMeta) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

// TestLoadSessionJobActivityTree_SizeTrimResumesAfterRemovedEntriesWithoutOverlap
// asserts trimActivityTrailingEntry's minted continuation actually resumes
// after the removed entry: it must set ResumeIndex, or a resumed load
// re-renders the target session from entry 0, redelivering everything the
// first page already returned. This drives the actual trim-then-resume
// round trip through the real entry point (LoadSessionJobActivityTree),
// following each minted continuation exactly as a client would, rather
// than asserting on the decoded token's fields directly. One session with
// jobCount jobs, each carrying a description large enough that the whole
// set exceeds activityMaxEncodedBytes but no single entry does (so a page
// never needs to trim its own only entry). The observable assertion:
// walking every page via its own minted continuation delivers each job
// exactly once, in order, with zero overlap and zero gap.
func TestLoadSessionJobActivityTree_SizeTrimResumesAfterRemovedEntriesWithoutOverlap(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "sizetrimroot"
	const jobCount = 20
	description := strings.Repeat("d", 250_000)
	var events []jobstore.Event
	for i := range jobCount {
		ts := time.Unix(int64(100+i), 0).UTC()
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts, Description: description,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	var delivered []string
	continuation := ""
	pages := 0
	for {
		pages++
		if pages > jobCount+1 {
			t.Fatalf("too many pages (%d) without terminating -- resume is looping instead of advancing", pages)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry without a Job: %+v", pages, entry)
			}
			delivered = append(delivered, entry.Job.JobID)
		}
		raw, err := json.Marshal(tree)
		if err != nil {
			t.Fatalf("page %d: marshal: %v", pages, err)
		}
		if len(raw) > activityMaxEncodedBytes {
			t.Fatalf("page %d encoded %d bytes, over the %d-byte bound", pages, len(raw), activityMaxEncodedBytes)
		}
		if tree.Root.Branch.Continuation == "" {
			break
		}
		continuation = tree.Root.Branch.Continuation
	}
	if pages < 2 {
		t.Fatalf("got %d page(s), want at least 2 -- the fixture must be large enough to force a size trim", pages)
	}
	if len(delivered) != jobCount {
		t.Fatalf("delivered %d entries across %d pages, want exactly %d (zero overlap, zero gap): %v", len(delivered), pages, jobCount, delivered)
	}
	for i, id := range delivered {
		want := fmt.Sprintf("job_%02d", i)
		if id != want {
			t.Fatalf("delivered[%d] = %q, want %q -- entries must arrive in order across pages with zero overlap and zero gap: %v", i, id, want, delivered)
		}
	}
}

// TestLoadSessionJobActivityTree_SizeTrimAdvancesPastEntryLargerThanAPage
// pins paging progress across an entry whose own encoding exceeds
// activityMaxEncodedBytes: no page can ever carry it, so the size trim must
// advance past it — reporting the omission through Branch.Error — instead of
// re-minting a continuation that points back at the same entry and hands the
// client an identical page forever. The fixture is one oversized shell job
// followed by an ordinary one; walking every minted continuation must reach
// the ordinary job exactly once and terminate.
func TestLoadSessionJobActivityTree_SizeTrimAdvancesPastEntryLargerThanAPage(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "oversizedentryroot"
	oversizedStarted := time.Unix(3000, 0).UTC()
	normalStarted := oversizedStarted.Add(time.Second)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: oversizedStarted, JobID: "job_oversized",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &oversizedStarted, Description: strings.Repeat("x", activityMaxEncodedBytes+(256<<10)),
		},
		jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: normalStarted, JobID: "job_normal",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &normalStarted, Description: "reachable after the oversized job",
		},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	var delivered []string
	var branchErrors []string
	continuation := ""
	pages := 0
	for {
		pages++
		if pages > 4 {
			t.Fatalf("walked %d pages without terminating -- resume is looping instead of advancing past the oversized entry", pages)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry without a Job: %+v", pages, entry)
			}
			delivered = append(delivered, entry.Job.JobID)
		}
		if tree.Root.Branch.Error != "" {
			branchErrors = append(branchErrors, tree.Root.Branch.Error)
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			break
		}
		if next == continuation {
			t.Fatalf("page %d re-minted page %d's continuation %q -- the client can never advance past the oversized entry", pages, pages-1, next)
		}
		continuation = next
	}
	if len(delivered) != 1 || delivered[0] != "job_normal" {
		t.Fatalf("delivered %v across %d pages, want exactly [job_normal] -- the oversized entry is unrepresentable and everything after it must still arrive once", delivered, pages)
	}
	if len(branchErrors) == 0 {
		t.Fatal("no page reported a branch error -- skipping an entry must be visible to the client")
	}
	named := false
	for _, branchError := range branchErrors {
		if strings.Contains(branchError, "job_oversized") {
			named = true
		}
	}
	if !named {
		t.Fatalf("branch errors %q name no skipped entry, want one naming job_oversized", branchErrors)
	}
}

// TestLoadSessionJobActivityTree_SizeTrimResumesAtAbsoluteIndexOnLaterPages
// pins that a size trim on a RESUMED page mints a position in the session's
// own entry numbering, not in the numbering of the page it just rendered.
// A resumed page's entries start at the continuation's ResumeIndex, so a
// trim that reports the index it trimmed within that page points back into
// the page it just returned — the client re-requests it, gets the identical
// page and the identical token, and never reaches the tail. The fixture is
// sized so a size trim lands on a page that is itself a resume, which a
// two-page walk never reaches.
func TestLoadSessionJobActivityTree_SizeTrimResumesAtAbsoluteIndexOnLaterPages(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "absoluteresumeroot"
	const jobCount = 60
	description := strings.Repeat("d", 250_000)
	var events []jobstore.Event
	for i := range jobCount {
		ts := time.Unix(int64(100+i), 0).UTC()
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts, Description: description,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	var delivered []string
	seen := map[string]int{}
	continuation := ""
	pages := 0
	for {
		pages++
		if pages > jobCount {
			t.Fatalf("walked %d pages without reaching job_%02d -- resume is not advancing", pages, jobCount-1)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry without a Job: %+v", pages, entry)
			}
			delivered = append(delivered, entry.Job.JobID)
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			break
		}
		if earlier, repeat := seen[next]; repeat {
			t.Fatalf("page %d minted page %d's continuation again (%q) -- a size trim on a resumed page reported its position within that page instead of the session's own entry numbering", pages, earlier, next)
		}
		seen[next] = pages
		continuation = next
	}
	if pages < 3 {
		t.Fatalf("got %d page(s), want at least 3 -- the fixture must be large enough to force a size trim on a resumed page", pages)
	}
	if len(delivered) != jobCount {
		t.Fatalf("delivered %d entries across %d pages, want exactly %d (zero overlap, zero gap): %v", len(delivered), pages, jobCount, delivered)
	}
	for i, id := range delivered {
		want := fmt.Sprintf("job_%02d", i)
		if id != want {
			t.Fatalf("delivered[%d] = %q, want %q -- every page must resume where the previous one stopped: %v", i, id, want, delivered)
		}
	}
}

// TestLoadSessionJobActivityTree_SizeTrimKeepsNestedEntryOverflowedByASibling
// pins that a page's overage is charged to the page, not to whichever entry
// the trim happens to reach last. A trim that empties a DEEPER session's
// rendered list says nothing about that entry's own size: the continuation
// it mints re-targets that session, and the next page is filtered to it, so
// the sibling that actually blew the budget is gone and the entry fits.
// Skipping it there loses it permanently, under a false "too large" error.
// The fixture is a root shell job just under the limit followed by a
// delegate whose child holds one small job.
func TestLoadSessionJobActivityTree_SizeTrimKeepsNestedEntryOverflowedByASibling(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "siblingoverageroot"
	childID := "siblingoveragechild"
	started := time.Unix(4000, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "child task"))
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_big",
		Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
		StartedAt: &started, Description: strings.Repeat("r", 3_900_000),
	})
	s1cov_writeJobLog(t, stateDir, childID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child",
		Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID,
		StartedAt: &started, Description: strings.Repeat("c", 400_000),
	})
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	// A client follows every continuation the tree hands it, wherever in the
	// tree it was minted: this page's own is on the CHILD's branch, not the
	// root's.
	var walk pastActivityWalk
	pending := []string{""}
	requested := map[string]bool{"": true}
	pages := 0
	for len(pending) > 0 {
		pages++
		if pages > 6 {
			t.Fatalf("walked %d pages without exhausting the tree; delivered %v", pages, walk.jobs)
		}
		continuation := pending[0]
		pending = pending[1:]
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		before := len(walk.continuations)
		collectPastActivityPage(t, &tree.Root, &walk)
		for _, next := range walk.continuations[before:] {
			if next == continuation {
				t.Fatalf("page %d re-minted the continuation it was loaded with (%q)", pages, next)
			}
			if requested[next] {
				continue
			}
			requested[next] = true
			pending = append(pending, next)
		}
	}
	if len(walk.branchErrors) != 0 {
		t.Fatalf("branch errors %q across %d pages, want none -- the child's job is renderable on a page filtered to it; the root's sibling is what did not fit", walk.branchErrors, pages)
	}
	counts := map[string]int{}
	for _, id := range walk.jobs {
		counts[id]++
	}
	if len(walk.jobs) != 2 || counts["job_root_big"] != 1 || counts["job_child"] != 1 {
		t.Fatalf("delivered %v across %d pages, want job_root_big and job_child exactly once each", walk.jobs, pages)
	}
}

// TestLoadSessionJobActivityTree_SizeTrimSkipsNestedEntryLargerThanAPage is
// the other half of the sibling-overage case above: an entry too large for
// any page, nested one hop down, still has to be skipped so paging
// terminates — one page later than a root-level one, because the first trim
// only re-targets the child. The page that re-targets it carries nothing but
// that entry, which is where "too large" becomes a fact rather than a guess.
func TestLoadSessionJobActivityTree_SizeTrimSkipsNestedEntryLargerThanAPage(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "nestedoversizedroot"
	childID := "nestedoversizedchild"
	started := time.Unix(5000, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "child task"))
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_small",
		Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
		StartedAt: &started, Description: "small root job",
	})
	s1cov_writeJobLog(t, stateDir, childID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_huge",
		Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID,
		StartedAt: &started, Description: strings.Repeat("h", activityMaxEncodedBytes+(256<<10)),
	})
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	var walk pastActivityWalk
	pending := []string{""}
	requested := map[string]bool{"": true}
	pages := 0
	for len(pending) > 0 {
		pages++
		if pages > 6 {
			t.Fatalf("walked %d pages without exhausting the tree -- the nested oversized entry is not being skipped", pages)
		}
		continuation := pending[0]
		pending = pending[1:]
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		before := len(walk.continuations)
		collectPastActivityPage(t, &tree.Root, &walk)
		for _, next := range walk.continuations[before:] {
			if next == continuation {
				t.Fatalf("page %d re-minted the continuation it was loaded with (%q)", pages, next)
			}
			if requested[next] {
				continue
			}
			requested[next] = true
			pending = append(pending, next)
		}
	}
	if len(walk.jobs) != 1 || walk.jobs[0] != "job_root_small" {
		t.Fatalf("delivered %v across %d pages, want only job_root_small (the child's entry fits no page)", walk.jobs, pages)
	}
	named := false
	for _, branchError := range walk.branchErrors {
		if strings.Contains(branchError, "job_child_huge") {
			named = true
		}
	}
	if !named {
		t.Fatalf("branch errors %q name no skipped entry, want one naming job_child_huge", walk.branchErrors)
	}
}

// pastActivityWalk accumulates what a paging client sees across pages: every
// job it was handed, every branch error, and every continuation any branch in
// the tree minted.
type pastActivityWalk struct {
	jobs          []string
	branchErrors  []string
	continuations []string
}

// collectPastActivityPage records session's whole subtree into walk, so a
// paging test can assert on a nested session's entries and follow a
// continuation minted below the root without knowing which page carried it.
func collectPastActivityPage(t *testing.T, session *appwire.JobActivitySession, walk *pastActivityWalk) {
	t.Helper()
	if session == nil {
		return
	}
	walk.record(session.Branch)
	for i := range session.Entries {
		entry := &session.Entries[i]
		if entry.Job != nil {
			walk.jobs = append(walk.jobs, entry.Job.JobID)
		}
		if entry.Delegate == nil {
			continue
		}
		walk.record(entry.Delegate.Branch)
		collectPastActivityPage(t, entry.Delegate.Child, walk)
	}
}

func (w *pastActivityWalk) record(branch appwire.JobActivityBranchState) {
	if branch.Error != "" {
		w.branchErrors = append(w.branchErrors, branch.Error)
	}
	if branch.Continuation != "" {
		w.continuations = append(w.continuations, branch.Continuation)
	}
}

// TestLoadSessionJobActivityTree_NestedContinuationSurvivesNonzeroFoldEpochs
// pins that a continuation minted on a RESUMED page carries the fold-cache
// generations the next request checks it against. A resumed page's ancestor
// chain is a filtered snapshot, and a filter that drops the epoch fields
// mints zeros: the next request then reads a real, nonzero generation for
// the same journals and rejects a perfectly fresh token as stale, stranding
// the client mid-tree. The fold caches are keyed by path and only bump a
// generation when a journal is rewritten rather than appended to, so the
// fixture warms them and then rewrites the delegate journal in place.
func TestLoadSessionJobActivityTree_NestedContinuationSurvivesNonzeroFoldEpochs(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "epochfilterroot"
	childID := "epochfilterchild"
	started := time.Unix(6000, 0).UTC()
	const childJobCount = 40
	description := strings.Repeat("e", 250_000)

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "child task"))
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_small",
		Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
		StartedAt: &started, Description: "small root job",
	})
	childEvents := make([]jobstore.Event, 0, childJobCount)
	for i := range childJobCount {
		ts := started.Add(time.Duration(i) * time.Second)
		childEvents = append(childEvents, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_child_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID,
			StartedAt: &ts, Description: description,
		})
	}
	childJobsPath := s1cov_writeJobLog(t, stateDir, childID, childEvents...)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	// The child names the root, so both sessions read the root's shared
	// delegate journal and report the same generation for it — the value a
	// nested continuation is checked against.
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)

	// Warm the fold caches at these journals, then move both generations by
	// letting each cache observe the journal gone and restoring it, so every
	// page below is checked against a nonzero generation rather than the
	// zeros a never-rewritten journal reports.
	warm := func() {
		if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{}); err != nil {
			t.Fatalf("warm the fold caches: %v", err)
		}
	}
	warm()
	bumpFoldGeneration(t, filepath.Join(jobsDir(stateDir, rootID), "delegates.jsonl"), warm)
	// A session load stats a missing jobs.jsonl and skips it without asking
	// the fold cache, so that cache has to observe the absence itself.
	bumpFoldGeneration(t, childJobsPath, func() {
		if _, err := historicalJobFoldCache.Get(context.Background(), childJobsPath, extendHistoricalJobFold); err != nil {
			t.Fatalf("observe the child jobs journal gone: %v", err)
		}
	})

	var walk pastActivityWalk
	pending := []string{""}
	requested := map[string]bool{"": true}
	pages := 0
	for len(pending) > 0 {
		pages++
		if pages > 8 {
			t.Fatalf("walked %d pages without exhausting the tree; delivered %d jobs", pages, len(walk.jobs))
		}
		continuation := pending[0]
		pending = pending[1:]
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v -- a continuation minted on a resumed page must carry the generations the next request checks", pages, err)
		}
		before := len(walk.continuations)
		collectPastActivityPage(t, &tree.Root, &walk)
		for _, next := range walk.continuations[before:] {
			if next == continuation {
				t.Fatalf("page %d re-minted the continuation it was loaded with (%q)", pages, next)
			}
			if requested[next] {
				continue
			}
			requested[next] = true
			pending = append(pending, next)
		}
	}
	if pages < 3 {
		t.Fatalf("got %d page(s), want at least 3 -- the fixture must force a trim on a page that is itself a resume", pages)
	}
	sawJobsGeneration, sawDelegatesGeneration := false, false
	for _, token := range walk.continuations {
		cont, decodeErr := decodeActivityContinuation(token, rootID)
		if decodeErr != nil {
			t.Fatalf("decode a minted continuation: %v", decodeErr)
		}
		sawJobsGeneration = sawJobsGeneration || cont.JobsEpoch != 0
		sawDelegatesGeneration = sawDelegatesGeneration || cont.DelegatesEpoch != 0
	}
	if !sawJobsGeneration || !sawDelegatesGeneration {
		t.Fatalf("minted continuations carried a nonzero jobs generation: %t, delegates generation: %t -- both must be nonzero or the fixture stopped moving a generation and this walk proves nothing about nonzero ones", sawJobsGeneration, sawDelegatesGeneration)
	}
	if len(walk.branchErrors) != 0 {
		t.Fatalf("branch errors %q, want none", walk.branchErrors)
	}
	counts := map[string]int{}
	for _, id := range walk.jobs {
		counts[id]++
	}
	if len(walk.jobs) != childJobCount+1 || counts["job_root_small"] != 1 {
		t.Fatalf("delivered %d jobs across %d pages, want %d exactly once each", len(walk.jobs), pages, childJobCount+1)
	}
	for i := range childJobCount {
		if id := fmt.Sprintf("job_child_%02d", i); counts[id] != 1 {
			t.Fatalf("%s delivered %d times, want exactly once", id, counts[id])
		}
	}
}

// TestLoadSessionJobActivityTree_SizeTrimSkipsOversizedEntryOnAResumedPage
// puts the skip on a page that is itself a resume, where the position it
// advances to has to be the entry's own rather than this page's index 0.
// Skipping to 1 there rewinds the walk to the second entry of the whole
// session, re-delivering a page already seen and arriving back at the same
// oversized entry forever. The fixture fills one page with ordinary jobs so
// the oversized one lands on page 2, with a tail job behind it that only a
// correctly advanced position can reach.
func TestLoadSessionJobActivityTree_SizeTrimSkipsOversizedEntryOnAResumedPage(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "resumedskiproot"
	const fillerCount = 20
	filler := strings.Repeat("f", 250_000)
	var events []jobstore.Event
	for i := range fillerCount {
		ts := time.Unix(int64(8000+i), 0).UTC()
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts, Description: filler,
		})
	}
	oversizedAt := time.Unix(9000, 0).UTC()
	tailAt := oversizedAt.Add(time.Second)
	events = append(events,
		jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: oversizedAt, JobID: "job_oversized",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &oversizedAt, Description: strings.Repeat("x", activityMaxEncodedBytes+(256<<10)),
		},
		jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: tailAt, JobID: "job_tail",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &tailAt, Description: "reachable only past the oversized job",
		},
	)
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	var delivered []string
	var branchErrors []string
	seen := map[string]int{}
	continuation := ""
	pages := 0
	for {
		pages++
		if pages > fillerCount {
			t.Fatalf("walked %d pages without reaching job_tail; delivered %v", pages, delivered)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry without a Job: %+v", pages, entry)
			}
			delivered = append(delivered, entry.Job.JobID)
		}
		if tree.Root.Branch.Error != "" {
			branchErrors = append(branchErrors, tree.Root.Branch.Error)
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			break
		}
		if earlier, repeat := seen[next]; repeat {
			t.Fatalf("page %d minted page %d's continuation again (%q) -- the skip rewound to this page's own index instead of the entry's", pages, earlier, next)
		}
		seen[next] = pages
		continuation = next
	}
	if pages < 3 {
		t.Fatalf("got %d page(s), want at least 3 -- the oversized entry must land on a page that is itself a resume", pages)
	}
	want := make([]string, 0, fillerCount+1)
	for i := range fillerCount {
		want = append(want, fmt.Sprintf("job_%02d", i))
	}
	want = append(want, "job_tail")
	if len(delivered) != len(want) {
		t.Fatalf("delivered %d entries across %d pages, want %d (every job but the oversized one, exactly once): %v", len(delivered), pages, len(want), delivered)
	}
	for i, id := range delivered {
		if id != want[i] {
			t.Fatalf("delivered[%d] = %q, want %q: %v", i, id, want[i], delivered)
		}
	}
	named := false
	for _, branchError := range branchErrors {
		if strings.Contains(branchError, "job_oversized") {
			named = true
		}
	}
	if !named {
		t.Fatalf("branch errors %q name no skipped entry, want one naming job_oversized", branchErrors)
	}
}

// TestLoadSessionJobActivityTree_CapsAnOversizedPromptLabel pins the fix for
// the envelope whose own label alone used to exceed the limit. A session with
// no generated name labels itself with its OriginalPrompt verbatim, so a
// pasted multi-megabyte prompt made the response go out over
// activityMaxEncodedBytes with nothing left to drop: the label is a fixed part
// of every page reporting the session, including the ancestor chain of a
// continuation. Projection now caps it — saying so with an ellipsis — so the
// page fits and its entries still render. (The trim-level behavior when a
// response's fixed parts genuinely exceed the limit remains pinned by
// TestTrimActivityTreeToFit_EnvelopeTooLargeLeavesConsistentCounts and
// TestMarkActivityEnvelopeTooLarge_WithdrawsTheContinuation.)
func TestLoadSessionJobActivityTree_CapsAnOversizedPromptLabel(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "oversizedlabelroot"
	started := time.Unix(11000, 0).UTC()
	events := make([]jobstore.Event, 0, 2)
	for i := range 2 {
		ts := started.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts, Description: "small job",
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: rootID, ProfileID: "openai", Model: "gpt-5.2",
		OriginalPrompt: strings.Repeat("p", activityMaxEncodedBytes+(256<<10)),
		CreatedAt:      time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	if len(tree.Root.Entries) != 2 {
		t.Fatalf("page rendered %d entries, want 2 -- the label is capped, so nothing had to be dropped", len(tree.Root.Entries))
	}
	if tree.Root.Branch.Error != "" {
		t.Fatalf("branch error = %q, want none: the label is capped, not reported as unfittable", tree.Root.Branch.Error)
	}
	if n := len([]rune(tree.Root.Label)); n > activityMaxLabelRunes {
		t.Fatalf("label = %d runes, want at most %d", n, activityMaxLabelRunes)
	}
	if !strings.HasSuffix(tree.Root.Label, "…") {
		t.Fatalf("label %q does not say it was cut", tree.Root.Label)
	}
}

// TestLoadSessionJobActivityTree_TrimmingADelegateKeepsItsChildReachable pins
// that dropping a delegate whose child was already trimmed to a continuation
// does not strand the child's remaining entries. The delegate's branch — and
// the continuation minted inside it — goes with the entry, so what has to
// carry the child forward is the owner's own position: it resumes AT the
// delegate, whose subtree is then rendered again from its own top with the
// room the dropped siblings freed. The fixture forces exactly that order: the
// child is trimmed to empty, the delegate is dropped next, and the owner's
// remaining entries are trimmed after it.
func TestLoadSessionJobActivityTree_TrimmingADelegateKeepsItsChildReachable(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "strandroot"
	childID := "strandchild"
	const rootJobCount = 20
	const childJobCount = 10
	description := strings.Repeat("s", 250_000)
	started := time.Unix(12000, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "child task"))
	rootEvents := make([]jobstore.Event, 0, rootJobCount)
	for i := range rootJobCount {
		ts := started.Add(time.Duration(i) * time.Second)
		rootEvents = append(rootEvents, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID,
			StartedAt: &ts, Description: description,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, rootEvents...)
	childEvents := make([]jobstore.Event, 0, childJobCount)
	for i := range childJobCount {
		ts := started.Add(time.Duration(100+i) * time.Second)
		childEvents = append(childEvents, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("child_%02d", i),
			Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID,
			StartedAt: &ts, Description: description,
		})
	}
	s1cov_writeJobLog(t, stateDir, childID, childEvents...)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)

	var walk pastActivityWalk
	pending := []string{""}
	requested := map[string]bool{"": true}
	pages := 0
	for len(pending) > 0 {
		pages++
		if pages > rootJobCount {
			t.Fatalf("walked %d pages without exhausting the tree; delivered %d jobs", pages, len(walk.jobs))
		}
		continuation := pending[0]
		pending = pending[1:]
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		before := len(walk.continuations)
		collectPastActivityPage(t, &tree.Root, &walk)
		for _, next := range walk.continuations[before:] {
			if next == continuation {
				t.Fatalf("page %d re-minted the continuation it was loaded with (%q)", pages, next)
			}
			if requested[next] {
				continue
			}
			requested[next] = true
			pending = append(pending, next)
		}
	}
	if len(walk.branchErrors) != 0 {
		t.Fatalf("branch errors %q, want none -- every entry here is renderable on some page", walk.branchErrors)
	}
	counts := map[string]int{}
	for _, id := range walk.jobs {
		counts[id]++
	}
	if len(walk.jobs) != rootJobCount+childJobCount {
		t.Fatalf("delivered %d jobs across %d pages, want %d exactly once each: %v", len(walk.jobs), pages, rootJobCount+childJobCount, counts)
	}
	for i := range rootJobCount {
		if id := fmt.Sprintf("job_%02d", i); counts[id] != 1 {
			t.Fatalf("%s delivered %d times, want exactly once", id, counts[id])
		}
	}
	for i := range childJobCount {
		if id := fmt.Sprintf("child_%02d", i); counts[id] != 1 {
			t.Fatalf("%s delivered %d times, want exactly once -- the child's entries must survive its delegate being trimmed", id, counts[id])
		}
	}
}

// TestLoadSessionJobActivityTree_ResumedPageMintsADecodablePath measures the
// two bounds against each other. The depth budget a resumed page spends is
// relative to the continuation's target -- the target is projection's own
// depth 0 however many hops led to it -- while the path a trim mints is
// absolute, counted from the tree's root through the filtered ancestor
// chain. A page resumed two hops down can therefore reach an absolute depth
// of len(Path)+activityMaxNewDepth, and decodeActivityContinuation rejects
// anything past activityMaxContinuationPathLength. If that arithmetic is
// reachable, the page hands back a token its own decoder refuses and the
// subtree below it is stranded.
func TestLoadSessionJobActivityTree_ResumedPageMintsADecodablePath(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Unix(800, 0).UTC()
	const chainLen = activityMaxNewDepth + 3
	sessionIDs := make([]string, chainLen)
	for i := range sessionIDs {
		sessionIDs[i] = fmt.Sprintf("deep%d", i)
	}
	description := strings.Repeat("d", 250_000)
	var descriptors []delegatestore.Descriptor
	for i, id := range sessionIDs {
		events := []jobstore.Event{{
			Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + id,
			Type: jobstore.JobShell, OwnerSessionID: id, VisibleToSession: id, StartedAt: &started,
		}}
		if i == len(sessionIDs)-1 {
			// The deepest session carries enough to force a trim, so the
			// entry that gets dropped is the one whose path is longest.
			for j := range 24 {
				ts := started.Add(time.Duration(j) * time.Second)
				events = append(events, jobstore.Event{
					Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%s_%02d", id, j),
					Type: jobstore.JobShell, OwnerSessionID: id, VisibleToSession: id, StartedAt: &ts,
					Description: description,
				})
			}
		}
		s1cov_writeJobLog(t, stateDir, id, events...)
		if i == 0 {
			savePastActivityMeta(t, stateDir, id, "Root")
		} else {
			savePastActivityMetaWithTreeRevision(t, stateDir, id, "Node", sessionIDs[0], 0)
		}
		if i+1 < len(sessionIDs) {
			descriptors = append(descriptors, pastStableDescriptor(id, sessionIDs[i+1], "next"))
		}
	}
	writePastStableDelegates(t, stateDir, sessionIDs[0], descriptors...)

	// Resume two hops down, which is what makes the absolute path the trim
	// mints longer than the depth budget alone would allow.
	resume := encodeActivityContinuation(activityContinuation{
		Version:   activityContinuationVersion,
		RootID:    sessionIDs[0],
		SessionID: sessionIDs[2],
		Path:      []string{"dlg_" + sessionIDs[1], "dlg_" + sessionIDs[2]},
	})
	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionIDs[0], appwire.JobsListParams{Continuation: resume})
	if err != nil {
		t.Fatalf("resume two hops down: %v", err)
	}
	var walk pastActivityWalk
	collectPastActivityPage(t, &tree.Root, &walk)
	for _, token := range walk.continuations {
		if _, decodeErr := decodeActivityContinuation(token, sessionIDs[0]); decodeErr != nil {
			t.Fatalf("the page minted a token its own decoder refuses: %v -- the subtree below it cannot be requested", decodeErr)
		}
	}

	// The deepest session is the one whose absolute path runs past the
	// limit, so it is the one that has to name itself instead.
	deepest := &tree.Root
	hops := 0
	for {
		var next *appwire.JobActivitySession
		for i := range deepest.Entries {
			if delegate := deepest.Entries[i].Delegate; delegate != nil && delegate.Child != nil {
				next = delegate.Child
			}
		}
		if next == nil {
			break
		}
		deepest = next
		hops++
	}
	if hops != activityMaxNewDepth+2 {
		t.Fatalf("the page reached %d hops below the root, want %d -- the fixture must resume 2 hops down and spend the whole depth budget past that", hops, activityMaxNewDepth+2)
	}
	if !deepest.Branch.Truncated {
		t.Fatalf("session %q lost an entry to the trim but is not marked truncated", deepest.SessionID)
	}
	if deepest.Branch.Continuation != "" {
		t.Fatalf("session %q minted a continuation whose path (%d hops) exceeds %d; it must report itself instead", deepest.SessionID, hops, activityMaxContinuationPathLength)
	}
	want := activityUnreachableByPathDiagnostic(deepest.SessionID)
	copies := 0
	for _, diagnostic := range deepest.Diagnostics {
		if diagnostic == want {
			copies++
		}
	}
	// The trim drops one entry per pass and strikes this session on every
	// one of them, so an unguarded append reports the same sentence once
	// per dropped entry.
	if copies != 1 {
		t.Fatalf("diagnostics %q on session %q carry %d copies of %q, want exactly 1", deepest.Diagnostics, deepest.SessionID, copies, want)
	}
}

// seedAbsentJournalActivityRoot writes a root whose entries are big enough to
// force a trim, with no jobs.jsonl of its own, and returns the root's
// jobs.jsonl path and its delegate IDs in render order.
//
// Delegate prose is capped at projection time (activityMaxDelegateProseRunes),
// so an oversized descriptor task no longer makes a page big. The child's
// shell-job description travels the same envelope uncapped, so that is what
// forces the trim these fixtures need.
func seedAbsentJournalActivityRoot(t *testing.T, stateDir, rootID string, delegates int) (string, []string) {
	t.Helper()
	started := time.Unix(900, 0).UTC()
	description := strings.Repeat("d", 400_000)
	var descriptors []delegatestore.Descriptor
	var ids []string
	for i := range delegates {
		childID := fmt.Sprintf("%schild%02d", rootID, i)
		descriptors = append(descriptors, pastStableDescriptor(rootID, childID, "inspect child"))
		ids = append(ids, "dlg_"+childID)
		s1cov_writeJobLog(t, stateDir, childID, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: "job_" + childID,
			Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started,
			Description: description,
		})
		savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)
	}
	savePastActivityMeta(t, stateDir, rootID, "Root")
	writePastStableDelegates(t, stateDir, rootID, descriptors...)
	rootJobsPath := filepath.Join(jobsDir(stateDir, rootID), "jobs.jsonl")
	if _, err := os.Stat(rootJobsPath); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v, want the journal absent", rootJobsPath, err)
	}
	sort.Strings(ids)
	return rootJobsPath, ids
}

// TestLoadSessionJobActivityTree_PaginatesWithNoJobJournal pins that a
// session with no jobs.jsonl at all can still be paged to the end. Its
// delegates are entries like any other, and a page that trims them has to
// hand back something the next request can use; refusing to mint over the
// absent journal leaves everything past the first page unreachable forever,
// since the journal is never going to appear.
func TestLoadSessionJobActivityTree_PaginatesWithNoJobJournal(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "nojobsroot"
	_, want := seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)

	walkActivityDelegatesToExhaustion(t, stateDir, rootID, want)
}

// walkActivityDelegatesToExhaustion follows rootID's continuations until a
// page mints none, and fails unless every delegate in want was delivered.
func walkActivityDelegatesToExhaustion(t *testing.T, stateDir, rootID string, want []string) {
	t.Helper()
	delivered := map[string]bool{}
	continuation := ""
	for page := 1; ; page++ {
		if page > 8 {
			t.Fatalf("walked %d pages without exhausting the tree; delivered %d of %d delegates", page, len(delivered), len(want))
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Delegate != nil {
				delivered[entry.Delegate.DelegateID] = true
			}
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			if tree.Root.Branch.Truncated {
				t.Fatalf("page %d is truncated but minted no continuation; diagnostics %q", page, tree.Root.Diagnostics)
			}
			break
		}
		if next == continuation {
			t.Fatalf("page %d re-minted the continuation it was loaded with", page)
		}
		continuation = next
	}
	for _, id := range want {
		if !delivered[id] {
			t.Fatalf("delegate %q was never delivered across the walk", id)
		}
	}
}

// TestLoadSessionJobActivityTree_PaginatesAfterTheJobJournalIsDeleted pins
// that a journal disappearing leaves the rest of the tree reachable. The
// fold cache records a deletion by advancing the path's generation, and
// advancing it again on every later look would refuse each page's own
// continuation on the request that carries it: the walk would never move
// past page two, and the delegates would be unreachable for good.
func TestLoadSessionJobActivityTree_PaginatesAfterTheJobJournalIsDeleted(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "deletedjobsroot"
	rootJobsPath, want := seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)
	started := time.Unix(900, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_a",
		Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
	})
	// Fold the journal while it is there, so its deletion is something the
	// cache has to record rather than a path it never knew.
	if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("warm the fold: %v", err)
	}
	if err := os.Remove(rootJobsPath); err != nil {
		t.Fatal(err)
	}

	walkActivityDelegatesToExhaustion(t, stateDir, rootID, want)
}

// TestLoadSessionJobActivityTree_RefusesWhenJournalPresenceChanges pins what
// a generation cannot say. An absent journal reports 0 and a journal folded
// for the first time reports 0, so presence travels in the token beside the
// generations and is compared the same way: a journal that appeared, or one
// that went away, moves the entries the position counts against.
func TestLoadSessionJobActivityTree_RefusesWhenJournalPresenceChanges(t *testing.T) {
	t.Run("job journal appears after the mint", func(t *testing.T) {
		stateDir := t.TempDir()
		rootID := "appearsroot"
		rootJobsPath, _ := seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)
		token := firstActivityContinuation(t, stateDir, rootID)

		started := time.Unix(900, 0).UTC()
		s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_a",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
		})
		if _, err := os.Stat(rootJobsPath); err != nil {
			t.Fatalf("stat %s: %v, want the journal present now", rootJobsPath, err)
		}
		if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: token}); err == nil {
			t.Fatal("resumed a token minted before the job journal existed; its position counts delegates only, and jobs now render ahead of them")
		}
	})

	t.Run("job journal goes away after the mint", func(t *testing.T) {
		stateDir := t.TempDir()
		rootID := "vanishesroot"
		rootJobsPath, _ := seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)
		started := time.Unix(900, 0).UTC()
		s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_a",
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
		})
		token := firstActivityContinuation(t, stateDir, rootID)

		if err := os.Remove(rootJobsPath); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: token}); err == nil {
			t.Fatal("resumed a token whose job journal has been deleted; the entries its position counts are gone with it")
		}
	})

	t.Run("delegate journal goes away after the mint", func(t *testing.T) {
		stateDir := t.TempDir()
		rootID := "dlgvanishesroot"
		seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)
		token := firstActivityContinuation(t, stateDir, rootID)

		if err := os.Remove(filepath.Join(jobsDir(stateDir, rootID), "delegates.jsonl")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: token}); err == nil {
			t.Fatal("resumed a token whose delegate journal has been deleted; every entry its position counts came from that journal")
		}
	})
}

// firstActivityContinuation returns the continuation the first, unpaged
// request for rootID mints, failing the test when there is none.
func firstActivityContinuation(t *testing.T, stateDir, rootID string) string {
	t.Helper()
	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if tree.Root.Branch.Continuation == "" {
		t.Fatalf("the first page minted no continuation; truncated=%t diagnostics=%q", tree.Root.Branch.Truncated, tree.Root.Diagnostics)
	}
	return tree.Root.Branch.Continuation
}

// TestLoadSessionJobActivityTree_RefusesAResumeAfterTheDelegateJournalBecomesUnreadable
// pins what the degraded read may and may not claim. A delegate journal with
// a line no scanner will read is contained rather than fatal: the page is
// served with an empty delegate set and a diagnostic. But the generation it
// reports for that journal is invented — it read nothing — and reporting 0
// makes it indistinguishable from a journal folded once and never rewritten,
// so a continuation minted while the journal was readable is accepted against
// a page whose delegate list is now empty, and its position counts entries
// that are not there.
func TestLoadSessionJobActivityTree_RefusesAResumeAfterTheDelegateJournalBecomesUnreadable(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "unreadabledlgroot"
	seedAbsentJournalActivityRoot(t, stateDir, rootID, 12)
	token := firstActivityContinuation(t, stateDir, rootID)
	cont, err := decodeActivityContinuation(token, rootID)
	if err != nil {
		t.Fatalf("decode the minted token: %v", err)
	}
	if cont.DelegatesEpoch != 0 {
		t.Fatalf("minted delegates generation %d, want 0 -- this test is about a token that cannot be told apart from the degraded read's invented 0", cont.DelegatesEpoch)
	}

	// Something lands in the journal that the scanner refuses. The appended
	// bytes are what make the fold look again; the scan override is what
	// makes that look fail, standing in for a line past the reader's cap
	// without writing one.
	delegatesPath := filepath.Join(jobsDir(stateDir, rootID), "delegates.jsonl")
	f, err := os.OpenFile(delegatesPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"unreadable\":true}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	original := scanDelegateJournal
	scanDelegateJournal = func(context.Context, string, int64, delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
		return nil, 0, delegatestore.ReadDiagnostics{}, fmt.Errorf("delegates.jsonl line 13: %w", delegatestore.ErrLineTooLong)
	}
	defer func() { scanDelegateJournal = original }()

	// The contained read still serves a page.
	degraded, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("a page over an unreadable delegate journal must still be served: %v", err)
	}
	found := false
	for _, diagnostic := range degraded.Root.Diagnostics {
		if strings.Contains(diagnostic, "delegate_journal_line_too_long") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics %q, want one naming the unreadable journal", degraded.Root.Diagnostics)
	}

	// The resume is not.
	if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: token}); err == nil {
		t.Fatal("resumed a token minted while the delegate journal was readable; the page it resumes into has no delegates at all, so its position counts entries that are not there")
	}
}

// TestLoadSessionJobActivityTree_PagesWithAnUnreadableDelegateJournal pins
// that a session whose delegate journal cannot be read still pages to the
// end. The unreadable state is stable — nothing is going to repair that
// journal between two requests — so it is not a reason to refuse a resume,
// and a mint that forgets to record it hands out a token this build then
// refuses forever. The work-unit cutoff is the mint site the trim does not
// exercise, so the fixture drives it: more shell jobs than the budget
// renders.
func TestLoadSessionJobActivityTree_PagesWithAnUnreadableDelegateJournal(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "unreadablepagingroot"
	started := time.Unix(1200, 0).UTC()
	const jobCount = activityMaxWorkUnits + 120
	events := make([]jobstore.Event, 0, jobCount)
	for i := range jobCount {
		ts := started.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("job_%04d", i),
			Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &ts,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, "unreadablepagingchild", "task"))
	savePastActivityMetaWithTreeRevision(t, stateDir, "unreadablepagingchild", "Child", rootID, 0)

	// Unreadable before the first page is built, and still unreadable when
	// the next one asks: the state never changes across this walk.
	original := scanDelegateJournal
	scanDelegateJournal = func(context.Context, string, int64, delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
		return nil, 0, delegatestore.ReadDiagnostics{}, fmt.Errorf("delegates.jsonl line 1: %w", delegatestore.ErrLineTooLong)
	}
	defer func() { scanDelegateJournal = original }()

	delivered := map[string]bool{}
	continuation := ""
	for page := 1; ; page++ {
		if page > 6 {
			t.Fatalf("walked %d pages without exhausting the tree; delivered %d of %d jobs", page, len(delivered), jobCount)
		}
		tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("page %d: %v -- the delegate journal was unreadable when this token was minted and is unreadable still, so nothing about it changed", page, err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job != nil {
				delivered[entry.Job.JobID] = true
			}
		}
		next := tree.Root.Branch.Continuation
		if next == "" {
			if tree.Root.Branch.Truncated {
				t.Fatalf("page %d is truncated but minted no continuation; diagnostics %q", page, tree.Root.Diagnostics)
			}
			break
		}
		if next == continuation {
			t.Fatalf("page %d re-minted the continuation it was loaded with", page)
		}
		continuation = next
	}
	if len(delivered) != jobCount {
		t.Fatalf("delivered %d jobs across the walk, want %d", len(delivered), jobCount)
	}
}
