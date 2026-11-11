package agent

// Issue #5012: a nested delegate's OwnerSessionID names the physical root that
// admitted its runtime, while the session that ran its create is
// VisibleSessionID. The live and historical jobs-activity rosters must
// attribute the row to the creator, as job_list/job_status already do, not to
// the root.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func delegateCreatedBySession(delegateID, parentID, ownerSessionID, visibleSessionID, childSessionID string) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: delegateID,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   childSessionID,
			TranscriptRef:    encodeRef("", childSessionID),
			ParentDelegateID: parentID,
			OwnerSessionID:   ownerSessionID,
			VisibleSessionID: visibleSessionID,
			Task:             "creator/owner distinction",
			AgentType:        "general",
			ToolNameCeiling:  []string{"communicate"},
			Resumable:        true,
		}},
	}
}

func TestLiveActivityBaseAttributesNestedDelegateToCreator(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 10, 10)
	controller.rootRuntime = s
	s.delegateController = controller
	rootID := s.ID()

	controller.mu.Lock()
	_, err := controller.appendLocked(
		delegateCreatedBySession("dlg_middle", "", rootID, rootID, "child-middle"),
		delegateCreatedBySession("dlg_leaf", "dlg_middle", rootID, "child-middle", "child-leaf"),
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("seed delegates: %v", err)
	}

	base, err := loadLiveActivityBase(s)
	if err != nil {
		t.Fatalf("loadLiveActivityBase: %v", err)
	}
	if _, ok := base.snapshot.Delegates["dlg_middle"]; !ok {
		t.Fatalf("root roster = %v, want the delegate it created", delegateIDs(base.snapshot.Delegates))
	}
	if _, ok := base.snapshot.Delegates["dlg_leaf"]; ok {
		t.Fatalf("root roster = %v, want the nested delegate attributed to its creator, not the physical owner",
			delegateIDs(base.snapshot.Delegates))
	}
}

func TestHistoricalDelegateRosterAttributesNestedDelegateToCreator(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "creatorroot"
	middle := pastStableDescriptor(rootID, "childmiddle", "middle")
	leaf := pastStableDescriptor(rootID, "childleaf", "leaf")
	// The leaf hangs off the middle delegate and was created by the middle
	// delegate's session; only the physical owner is the root.
	leaf.ParentDelegateID = "dlg_middle"
	leaf.VisibleSessionID = "childmiddle"
	writePastDelegates(t, stateDir, rootID, middle, leaf)

	cache := newHistoricalActivityCache(context.Background(), "")
	rootRows, _, _, err := loadHistoricalStableActivity(cache, stateDir, rootID, rootID)
	if err != nil {
		t.Fatalf("loadHistoricalStableActivity(root): %v", err)
	}
	if _, ok := rootRows["dlg_middle"]; !ok {
		t.Fatalf("root roster = %v, want the delegate it created", delegateIDs(rootRows))
	}
	if _, ok := rootRows["dlg_leaf"]; ok {
		t.Fatalf("root roster = %v, want the nested delegate attributed to its creator", delegateIDs(rootRows))
	}

	creatorRows, _, _, err := loadHistoricalStableActivity(cache, stateDir, rootID, "childmiddle")
	if err != nil {
		t.Fatalf("loadHistoricalStableActivity(creator): %v", err)
	}
	if _, ok := creatorRows["dlg_leaf"]; !ok {
		t.Fatalf("creator roster = %v, want the delegate it created", delegateIDs(creatorRows))
	}
	if len(creatorRows) != 1 {
		t.Fatalf("creator roster = %v, want exactly its own child", delegateIDs(creatorRows))
	}
}

// Issue #5643: after #5012 the root roster holds only the rows the root
// created, so an intermediate delegate session whose jobs journal is missing
// or unreadable used to take its nested delegates with it. Their rows are
// still in the shared root journal, attributed to that creator, and the tree
// must keep rendering them under the degraded child.
func TestHistoricalActivityTreeKeepsNestedDelegatesWhenCreatorSessionUnavailable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		makeUnavailable func(t *testing.T, stateDir, sessionID string)
		recover         func(t *testing.T, stateDir, sessionID string)
	}{
		{
			name:            "missing jobs journal",
			makeUnavailable: func(*testing.T, string, string) {},
			recover: func(t *testing.T, stateDir, sessionID string) {
				t.Helper()
				started := time.Unix(9_001, 0).UTC()
				s1cov_writeJobLog(t, stateDir, sessionID, jobstore.Event{
					Kind: jobstore.EventJobStarted, TS: started, JobID: "job_middle", Type: jobstore.JobShell,
					OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &started,
				})
			},
		},
		{
			name: "unreadable jobs journal",
			makeUnavailable: func(t *testing.T, stateDir, sessionID string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			recover: func(t *testing.T, stateDir, sessionID string) {
				t.Helper()
				if err := os.Remove(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")); err != nil {
					t.Fatal(err)
				}
				started := time.Unix(9_002, 0).UTC()
				s1cov_writeJobLog(t, stateDir, sessionID, jobstore.Event{
					Kind: jobstore.EventJobStarted, TS: started, JobID: "job_middle", Type: jobstore.JobShell,
					OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &started,
				})
			},
		},
		{
			name: "missing session meta",
			makeUnavailable: func(t *testing.T, stateDir, sessionID string) {
				t.Helper()
				if err := os.Remove(schema.SessionMetaPath(stateDir, sessionID)); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			rootID := "unreadablecreatorroot"
			middleID := "childmiddleunreadable"
			leafID := "childleafreadable"
			middle := pastStableDescriptor(rootID, middleID, "middle task")
			leaf := pastStableDescriptor(rootID, leafID, "leaf task")
			leaf.ParentDelegateID = "dlg_" + strings.TrimPrefix(middleID, "child")
			leaf.VisibleSessionID = middleID
			writePastDelegates(t, stateDir, rootID, middle, leaf)

			started := time.Unix(9_000, 0).UTC()
			s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
				Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell,
				OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
			})
			s1cov_writeJobLog(t, stateDir, leafID, jobstore.Event{
				Kind: jobstore.EventJobStarted, TS: started, JobID: "job_leaf", Type: jobstore.JobShell,
				OwnerSessionID: leafID, VisibleToSession: leafID, StartedAt: &started,
			})
			savePastActivityMeta(t, stateDir, rootID, "Root")
			// The middle session exists (meta and transcript) but its own jobs
			// journal does not load. Its nested delegate row still lives in the
			// root journal.
			savePastActivityMetaWithTreeRevision(t, stateDir, middleID, "Middle", rootID, 0)
			savePastActivityMetaWithTreeRevision(t, stateDir, leafID, "Leaf", rootID, 0)
			tc.makeUnavailable(t, stateDir, middleID)

			tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
			if err != nil {
				t.Fatalf("LoadSessionJobActivityTree: %v", err)
			}
			middleDelegate := pastFindDelegate(t, tree.Root, middleID)
			if middleDelegate.Child == nil {
				t.Fatal("middle delegate child is absent: the nested delegate rows attributed to its creator were dropped with the unreadable session (#5643)")
			}
			diagnosed := false
			for _, diagnostic := range middleDelegate.Child.Diagnostics {
				if strings.Contains(diagnostic, middleID) {
					diagnosed = true
				}
			}
			if !diagnosed {
				t.Fatalf("degraded child diagnostics = %q, want one naming the unreadable session", middleDelegate.Child.Diagnostics)
			}
			if middleDelegate.Child.Branch.Error == "" {
				t.Fatal("degraded child branch error is empty; aggregation would report the job counts complete")
			}
			if middleDelegate.Child.Counts.Complete {
				t.Fatalf("degraded child counts = %+v, want Complete false", middleDelegate.Child.Counts)
			}

			leafDelegate := pastFindDelegate(t, *middleDelegate.Child, leafID)
			if leafDelegate.Child == nil {
				t.Fatal("nested leaf delegate has no child session, want the readable leaf session loaded beneath the degraded branch")
			}
			foundJob := false
			for _, entry := range leafDelegate.Child.Entries {
				if entry.Job != nil && entry.Job.JobID == "job_leaf" {
					foundJob = true
				}
			}
			if !foundJob {
				t.Fatalf("nested leaf entries = %+v, want job %q", leafDelegate.Child.Entries, "job_leaf")
			}
			if tc.recover == nil {
				return
			}
			tc.recover(t, stateDir, middleID)
			tree, err = LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
			if err != nil {
				t.Fatalf("LoadSessionJobActivityTree after recovery: %v", err)
			}
			recovered := pastFindDelegate(t, tree.Root, middleID)
			if recovered.Child == nil {
				t.Fatal("recovered child is absent")
			}
			if recovered.Child.Branch.Error != "" || !recovered.Child.Counts.Complete {
				t.Fatalf("recovered child branch = %q counts = %+v, want no error and Complete true", recovered.Child.Branch.Error, recovered.Child.Counts)
			}
		})
	}
}

// A live root seeds the delegate-journal root from its own resolved root
// before visiting children. Without that seed, a first historical child whose
// own meta is missing falls back to its own session ID, scans a nonexistent
// per-child journal, and hides the nested delegates the shared journal
// attributes to it.
func TestLiveActivityTreeKeepsNestedDelegatesWhenFirstHistoricalChildMetaMissing(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir}),
	)
	rootID := s.ID()
	middleID := "childlivemiddle"
	leafID := "childliveleaf"
	middleDelegateID := "dlg_" + strings.TrimPrefix(middleID, "child")
	leafDelegateID := "dlg_" + strings.TrimPrefix(leafID, "child")
	middle := pastStableDescriptor(rootID, middleID, "middle task")
	leaf := pastStableDescriptor(rootID, leafID, "leaf task")
	leaf.ParentDelegateID = middleDelegateID
	leaf.VisibleSessionID = middleID
	writePastDelegates(t, stateDir, rootID, middle, leaf)

	started := time.Unix(9_100, 0).UTC()
	s1cov_writeJobLog(t, stateDir, leafID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_live_leaf", Type: jobstore.JobShell,
		OwnerSessionID: leafID, VisibleToSession: leafID, StartedAt: &started,
	})
	savePastActivityMetaWithTreeRevision(t, stateDir, leafID, "Leaf", rootID, 0)

	controller, _ := newDelegateControllerTestHarness(t, 10, 10)
	controller.rootRuntime = s
	s.delegateController = controller
	controller.mu.Lock()
	_, err := controller.appendLocked(
		delegateCreatedBySession(middleDelegateID, "", rootID, rootID, middleID),
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("seed live delegate: %v", err)
	}

	rootLoc := activitySessionLocator{live: s, stateDir: stateDir, sessionID: rootID}
	snapshot, _, _, err := loadActivitySnapshotForParams(context.Background(), rootLoc, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("loadActivitySnapshotForParams: %v", err)
	}
	middleRow, ok := snapshot.Delegates[middleDelegateID]
	if !ok {
		t.Fatalf("live root roster = %v, want the row it created", delegateIDs(snapshot.Delegates))
	}
	childID, err := activityChildSessionForStable(middleRow)
	if err != nil {
		t.Fatalf("middle child link: %v", err)
	}
	child := snapshot.Children[childID]
	if child == nil {
		t.Fatal("first historical child is absent: its nested delegates from the shared journal were hidden (#5643)")
	}
	if _, ok := child.Delegates[leafDelegateID]; !ok {
		t.Fatalf("degraded child delegates = %v, want the nested delegate row", delegateIDs(child.Delegates))
	}
	leafChild := child.Children[leafID]
	if leafChild == nil {
		t.Fatal("nested leaf session did not load under the degraded child")
	}
	found := false
	for _, record := range leafChild.Jobs {
		if record != nil && record.JobID == "job_live_leaf" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nested leaf jobs = %+v, want job %q", leafChild.Jobs, "job_live_leaf")
	}
}

// A continuation minted while a degraded child's jobs journal was unreadable
// must be refused once that journal reads: its ResumeIndex was counted over
// an empty job list, and the recovered journal can renumber the delegate
// entries the page did render. The unreadable condition travels in the token
// for exactly the reason JobsAbsent does.
func TestHistoricalActivityContinuationRejectsPositionAfterJobsJournalRecovers(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "continuationdegraderoot"
	middleID := "childcontinuationdegraded"
	leafID := "childcontinuationleaf"
	middle := pastStableDescriptor(rootID, middleID, "middle task")
	leaf := pastStableDescriptor(rootID, leafID, "leaf task")
	leaf.ParentDelegateID = "dlg_" + strings.TrimPrefix(middleID, "child")
	leaf.VisibleSessionID = middleID
	writePastDelegates(t, stateDir, rootID, middle, leaf)

	started := time.Unix(9_001, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell,
		OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started,
	})
	s1cov_writeJobLog(t, stateDir, leafID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_leaf", Type: jobstore.JobShell,
		OwnerSessionID: leafID, VisibleToSession: leafID, StartedAt: &started,
	})
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, middleID, "Middle", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, leafID, "Leaf", rootID, 0)

	journalPath := filepath.Join(jobsDir(stateDir, middleID), "jobs.jsonl")
	if err := os.MkdirAll(journalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	params := appwire.JobsListParams{Continuation: encodeActivityContinuation(activityContinuation{
		Version:        activityContinuationVersion,
		RootID:         rootID,
		SessionID:      middleID,
		Path:           []string{"dlg_" + strings.TrimPrefix(middleID, "child")},
		JobsUnreadable: true,
	})}
	rootLoc := activitySessionLocator{stateDir: stateDir, sessionID: rootID}

	degraded, _, _, err := loadActivitySnapshotForParams(context.Background(), rootLoc, params)
	if err != nil {
		t.Fatalf("continuation while the child journal is unreadable: %v", err)
	}
	target := degraded.Children[middleID]
	if target == nil {
		t.Fatalf("degraded target %q is absent from the resumed page", middleID)
	}
	if len(target.Jobs) != 0 || !target.JobsJournalUnreadable {
		t.Fatalf("degraded target jobs=%d unreadable=%t, want an empty job list marked unreadable", len(target.Jobs), target.JobsJournalUnreadable)
	}
	if _, ok := target.Delegates["dlg_"+strings.TrimPrefix(leafID, "child")]; !ok {
		t.Fatalf("degraded target delegates = %v, want the nested delegate row", delegateIDs(target.Delegates))
	}

	if err := os.RemoveAll(journalPath); err != nil {
		t.Fatal(err)
	}
	s1cov_writeJobLog(t, stateDir, middleID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: "job_middle", Type: jobstore.JobShell,
		OwnerSessionID: middleID, VisibleToSession: middleID, StartedAt: &started,
	})
	if _, _, _, err := loadActivitySnapshotForParams(context.Background(), rootLoc, params); err == nil {
		t.Fatal("expected the continuation to be rejected after the jobs journal became readable")
	}
}

// Both mint sites must carry the degraded session's unreadable condition, or
// a continuation the service itself hands out is refused on its first resume
// with "the underlying journal changed" while the journal is still unreadable.
func TestActivityContinuationMintsCarryJobsUnreadable(t *testing.T) {
	t.Parallel()
	own := activitySessionEpochs{jobsUnreadable: true}

	var marked appwire.JobActivitySession
	markActivitySessionTruncated(&marked, newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 0), "child", nil, 0, own)
	assertJobsUnreadableContinuation(t, "markActivitySessionTruncated", marked.Branch.Continuation)

	trimmed := appwire.JobActivitySession{SessionID: "child"}
	mintActivityTrimContinuation(activityTrimmedEntry{session: &trimmed}, "root", 0, map[string]activitySessionEpochs{"child": own}, 0)
	assertJobsUnreadableContinuation(t, "mintActivityTrimContinuation", trimmed.Branch.Continuation)
}

func assertJobsUnreadableContinuation(t *testing.T, name, token string) {
	t.Helper()
	if token == "" {
		t.Fatalf("%s minted no continuation", name)
	}
	cont, err := decodeActivityContinuation(token, "root")
	if err != nil {
		t.Fatalf("%s continuation decode: %v", name, err)
	}
	if !cont.JobsUnreadable {
		t.Fatalf("%s continuation JobsUnreadable = false, want the degraded target's true", name)
	}
}

func delegateIDs(rows map[string]delegateSnapshot) []string {
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	return ids
}
