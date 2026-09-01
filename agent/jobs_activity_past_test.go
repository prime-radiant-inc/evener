package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// TestLoadSessionJobActivityTree_ReadsSharedDelegateJournalOncePerRoot covers
// #448's evidence that loadHistoricalActivityBase re-read and re-folded the
// shared root delegates.jsonl once per VISITED session, making loading
// O(sessions x delegate events). root -> child1 -> child2 are three visited
// sessions sharing one delegates.jsonl at the root; the journal must be
// scanned exactly once regardless.
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
// covers #448's acceptance criterion that cancellation is checked between
// descendant sessions, not only between records within one journal: once the
// request context is canceled, no later session's jobs.jsonl is opened.
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

// TestLoadSessionJobActivityTree_BoundsSingleSessionScanAtWorkUnitBudget is
// #448's incremental-fold round's replacement for the original per-file-
// ceiling regression test: a session's jobs.jsonl carries more valid
// job_started records than activityMaxWorkUnits. Jesse's ruling on this
// round (200k events is a normal Tuesday; truncating legit history is
// unacceptable) means LOADING no longer stops at the budget at all — the
// whole session folds via historicalJobFoldCache regardless of size — so
// what this test now proves is that PROJECTION alone still bounds one
// response page at activityMaxWorkUnits entries, AND (the #812 fix this
// round closes) mints a continuation that actually advances: decoding it
// back out must show ResumeIndex == activityMaxWorkUnits, not an opaque
// token a client can't reason about.
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

// TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted covers
// the load-phase traversal-breadth bound: a root with more direct delegate
// children than activityMaxWorkUnits must stop VISITING them (never open
// the ones past the budget) rather than only trimming the rendered page
// afterward — previously only cycle detection bounded the number of
// sessions visited during load, so an unbounded tree of small sessions
// still forced O(sessions) file opens before projection's budget ever
// applied. #448's incremental-fold round changed what this budget counts
// during load (one unit per session VISITED, not one per job record loaded
// — see historicalActivityCache's doc comment — since a session's own
// journal is no longer capped at load time at all), so this now uses many
// CHILDREN rather than many JOBS to exhaust it, unlike before.
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

// TestLoadSessionJobActivityTree_BoundsRecursionDepth covers the depth half
// of #448's finding 1: a chain of sessions longer than activityMaxNewDepth
// must not be recursed into (and its jobs.jsonl must never be opened) past
// that depth during LOAD, not only trimmed afterward in projection.
// Asserting on the wire-visible depth alone would not catch a load-phase
// regression here: projection ALREADY correctly trims a too-deep tree to
// activityMaxNewDepth on its own (pre-existing, unrelated to this fix), so a
// tree loaded fully unbounded would still render with the same, already-
// truncated depth. The scan-call count is what actually distinguishes
// "loaded everything, trimmed at render time" from "never opened the files
// past the depth bound."
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
	// #448 (roborev): the delegate at the depth boundary must report an
	// honest Truncated+Continuation branch — the same shape projection's
	// pre-existing work-unit exhaustion already produces — not a generic
	// "child session unavailable" branch error, which is what a load phase
	// that leaves snapshot.Children unpopulated for a depth-skipped child
	// causes projection to fall back to.
	if stoppedAt == nil {
		t.Fatal("chain never reached a depth-truncated delegate")
	}
	if stoppedAt.Branch.Error != "" {
		t.Fatalf("depth-boundary delegate branch.Error = %q, want empty (a placeholder child, not a load error)", stoppedAt.Branch.Error)
	}
	if !stoppedAt.Branch.Truncated {
		t.Fatalf("depth-boundary delegate branch.Truncated = false, want true")
	}
	if stoppedAt.Branch.Continuation == "" {
		t.Fatal("depth-boundary delegate branch.Continuation is empty, want the token markActivityDelegateTruncated mints (whether a resubmitted depth continuation makes further progress is a separate question this test does not cover)")
	}
}

// TestLoadSessionJobActivityTree_ContinuationAtMaxDepthLoadsTargetsOwnChildren
// covers roborev's finding on #807's saturation commit: buildActivityFullSnapshot
// computes its load-phase depth from len(visited)-1 -- the ancestor chain
// from the ORIGINAL root -- while projection resets depth to 0 at the
// continuation target (startDepth = -len(cont.Path), reaching 0 exactly at
// the target). A continuation whose path reaches exactly activityMaxNewDepth
// hops therefore has the LOAD phase treat the target's OWN children as
// already past the depth bound (depth == activityMaxNewDepth), replacing
// them with empty placeholders, even though projection -- and any sane
// reading of "how deep beneath the page I'm resuming into" -- would allow a
// full activityMaxNewDepth further beneath the target.
func TestLoadSessionJobActivityTree_ContinuationAtMaxDepthLoadsTargetsOwnChildren(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Unix(700, 0).UTC()

	// index activityMaxNewDepth is the continuation TARGET (reached via
	// exactly activityMaxNewDepth hops from the root); index
	// activityMaxNewDepth+1 is the target's OWN child -- the node the
	// roborev finding says gets silently placeholder'd instead of loaded.
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
		Version: activityContinuationV1, RootID: sessionIDs[0], SessionID: targetID, Path: path,
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

// TestLoadSessionJobActivityTree_PropagatesCancellationFromDescendant covers
// #448's regression finding: a canceled request must surface as a real
// error even when the cancellation lands while loading a DESCENDANT (not the
// root) — the previous fix's own cancellation test only ever canceled the
// root's own scan, so it never exercised the buildActivityFullSnapshot loop
// that was catching a descendant's context.Canceled into snapshot.Errors and
// returning a silently-partial tree with err == nil.
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

// TestDecodeActivityContinuation_RejectsPathLongerThanMaxDepth covers
// roborev's finding on #807: continuation paths are client-controlled, so
// without this cap a long valid path could force buildActivityContinuationAt
// to open arbitrarily many historical sessions' files with no bound at all
// (ordinary, non-continuation traversal's own depth limit is enforced by
// buildActivityFullSnapshot's recursion, which this path-following code
// doesn't go through).
func TestDecodeActivityContinuation_RejectsPathLongerThanMaxDepth(t *testing.T) {
	path := make([]string, activityMaxNewDepth+1)
	for i := range path {
		path[i] = fmt.Sprintf("hop%d", i)
	}
	token := encodeActivityContinuation(activityContinuation{
		Version: activityContinuationV1, RootID: "root", SessionID: "session", Path: path,
	})
	if _, err := decodeActivityContinuation(token, "root"); err == nil {
		t.Fatal("expected an error for a continuation path longer than activityMaxNewDepth")
	}
}

// TestBuildActivityContinuationAt_ExhaustedBudgetStopsBeforeLoadingMoreHops
// covers roborev's finding on #807: each continuation hop must be charged
// against the shared load budget the same way buildActivityFullSnapshot
// charges each child it visits -- otherwise loadActivityBase (which opens
// files) runs once per hop with no bound of its own. A budget that is
// already exhausted before ANY hop is resolved must stop immediately,
// never reaching loadActivityBase for even the first one.
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
	cont := activityContinuation{Version: activityContinuationV1, RootID: rootID, SessionID: childID, Path: []string{"dlg_" + childID}}
	root := activitySessionLocator{stateDir: stateDir, sessionID: rootID}
	if _, _, _, err := buildActivityContinuationAt(root, cont, 0, map[string]bool{rootID: true}, false, cache); err == nil {
		t.Fatal("expected an error: the load budget is already exhausted before resolving even the first continuation hop")
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
