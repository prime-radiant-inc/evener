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
	scanDelegateJournal = func(ctx context.Context, path string, limits delegatestore.ScanLimits) ([]delegatestore.Event, delegatestore.ReadDiagnostics, error) {
		atomic.AddInt32(&delegateScans, 1)
		return original(ctx, path, limits)
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
	scanJobJournal = func(ctx context.Context, path string, limits jobstore.ScanLimits) ([]jobstore.Event, error) {
		scannedPaths = append(scannedPaths, path)
		cancel() // cancel once the first session's own journal is reached
		return original(ctx, path, limits)
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
// #448's own root-cause reproduction: a session's jobs.jsonl carries more
// valid job_started records than activityMaxWorkUnits, followed by one
// malformed trailing line. Before this fix, the per-file scan ceiling
// (100,000 events) was unrelated to activityMaxWorkUnits (2000), so decoding
// ran past the budget line and only failed on the malformed content past it
// — proving the projection budget did not bound input scanning. Loading must
// now stop at the work-unit budget itself and report a truncated (not
// failed) tree, never reaching the malformed line.
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
	jobsPath := s1cov_writeJobLog(t, stateDir, rootID, events...)
	s1cov_corruptJobLog(t, jobsPath) // appends one malformed trailing line
	savePastActivityMeta(t, stateDir, rootID, "Root")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v, want a truncated tree, not a hard error", err)
	}
	if !got.Root.Branch.Truncated {
		t.Fatalf("Root.Branch.Truncated = false, want true (load stopped at the work-unit budget)")
	}
	if len(got.Root.Entries) != activityMaxWorkUnits {
		t.Fatalf("got %d entries, want exactly activityMaxWorkUnits=%d (decoding must stop there, never reaching the malformed line)", len(got.Root.Entries), activityMaxWorkUnits)
	}
}

// TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted covers
// the aggregate, cross-session half of #448's finding 1: the root session
// alone consumes the entire tree-wide work-unit budget, so a delegate child
// must never even be visited (its jobs.jsonl must never be opened) —
// previously only cycle detection bounded the number of sessions visited
// during load, so an unbounded tree of small sessions still forced O(sessions)
// file opens before projection's budget ever applied.
func TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootwidebudget"
	started := time.Unix(600, 0).UTC()

	events := make([]jobstore.Event, 0, activityMaxWorkUnits)
	for i := range activityMaxWorkUnits {
		jobID := fmt.Sprintf("job_%d", i)
		events = append(events, jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
			OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)

	childID := "childwidebudget"
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "should never be opened"))
	childJobsPath := s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	before, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)

	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, limits jobstore.ScanLimits) ([]jobstore.Event, error) {
		scannedPaths = append(scannedPaths, path)
		return original(ctx, path, limits)
	}
	defer func() { scanJobJournal = original }()

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	if len(scannedPaths) != 1 {
		t.Fatalf("scanned %d job journals, want exactly 1 (root only; budget exhausted before the child delegate is ever visited): %v", len(scannedPaths), scannedPaths)
	}
	after, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatalf("child job log changed despite never being opened: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
	if !got.Root.Branch.Truncated {
		t.Fatalf("Root.Branch.Truncated = false, want true")
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
	scanJobJournal = func(ctx context.Context, path string, limits jobstore.ScanLimits) ([]jobstore.Event, error) {
		scannedPaths = append(scannedPaths, path)
		return original(ctx, path, limits)
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
	scanJobJournal = func(ctx context.Context, path string, limits jobstore.ScanLimits) ([]jobstore.Event, error) {
		calls++
		if calls == 2 {
			// Cancel while loading child1's OWN journal — a descendant, not
			// the root — the exact case the swallow-into-Errors bug missed.
			cancel()
		}
		return original(ctx, path, limits)
	}
	defer func() { scanJobJournal = original }()

	if _, err := LoadSessionJobActivityTree(ctx, stateDir, rootID, appwire.JobsListParams{}); err == nil {
		t.Fatal("expected a non-nil error when a descendant's load is canceled, got nil (silently-partial success)")
	}
	if calls != 2 {
		t.Fatalf("scanJobJournal called %d times, want exactly 2 (root, then child1; child2 must never be reached once cancellation propagates)", calls)
	}
}

// TestLoadSessionJobActivityTree_DegradesToPartialWhenDelegateJournalExceedsScanLimit
// covers the ceilings decision for #448: delegatestore.Descriptor embeds the
// FULL FrozenRolePrompt/FrozenSkillBodies text on every delegate_created
// event, so a legitimately heavy-delegating root can plausibly approach the
// scan ceiling over time with no adversarial input at all. Hitting it must
// degrade the response (a diagnostic, tree still returned) rather than hard-
// failing the WHOLE activity tree for that root.
func TestLoadSessionJobActivityTree_DegradesToPartialWhenDelegateJournalExceedsScanLimit(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootdlgpartial"
	started := time.Unix(900, 0).UTC()

	restoreLimits := historicalDelegateScanLimits
	historicalDelegateScanLimits = delegatestore.ScanLimits{MaxEvents: 5}
	defer func() { historicalDelegateScanLimits = restoreLimits }()

	descriptors := make([]delegatestore.Descriptor, 0, 10)
	for i := range 10 {
		descriptors = append(descriptors, pastStableDescriptor(rootID, fmt.Sprintf("childdlgpartial%d", i), "task"))
	}
	writePastStableDelegates(t, stateDir, rootID, descriptors...)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v, want a degraded tree, not a hard error", err)
	}
	// Root's own shell job still renders: the delegate-journal ceiling must
	// not sink data this session owns directly.
	if len(got.Root.Entries) == 0 {
		t.Fatalf("Root.Entries is empty, want the root's own shell job to still render")
	}
	found := false
	for _, d := range got.Root.Diagnostics {
		if strings.Contains(d, "delegate_journal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Diagnostics = %v, want a delegate-journal-scan-limit diagnostic", got.Root.Diagnostics)
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
