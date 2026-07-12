//go:build serffuzz

package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func seed100JobsNestedMore(t *testing.T) {
	t.Helper()
	started := frozenTestTime
	start := func(t *testing.T, jm *jobManager, id, owner, parent string, typ jobstore.JobType, ref string) {
		t.Helper()
		if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: id, Type: typ, OwnerSessionID: owner, VisibleToSession: jm.sessionID, ParentJobID: parent, TranscriptRef: ref, StartedAt: &started}); err != nil {
			t.Fatal(err)
		}
	}

	// Owner lookup guards for absent runtimes and runtimes without a store.
	root := newSession(t)
	start(t, root.jobManager, "orphan", "absent", "parent", jobstore.JobShell, "")
	_, _ = root.ownerJobManagerFor("orphan")
	noStore := newSession(t)
	noStoreOriginal := noStore.jobManager.store
	noStore.jobManager.store = nil
	root.subagents.subs[noStore.ID()] = &subagent{id: noStore.ID(), sess: noStore}
	start(t, root.jobManager, "nostore", noStore.ID(), "parent", jobstore.JobShell, "")
	_, _ = root.ownerJobManagerFor("nostore")
	noStore.jobManager.store = noStoreOriginal

	// Global ordering/limit, nil projection fallback, depth errors, and dedupe.
	walk := newSession(t)
	start(t, walk.jobManager, "b", walk.ID(), "", jobstore.JobShell, "")
	start(t, walk.jobManager, "a", walk.ID(), "", jobstore.JobShell, "")
	if jobs, err := walk.walkDescendantJobs(listFilter{Limit: 1}); err != nil || len(jobs) != 1 {
		t.Fatalf("limited walk = %v, %v", jobs, err)
	}
	_ = projectDescendantWalkRow(walk, descendantWalkRow{rec: &jobstore.JobRecord{JobID: "nil-owner", OwnerSessionID: walk.ID(), StartedAt: started}})

	bareChild := &Session{}
	_ = walk.collectDescendantJobs(bareChild, 1, listFilter{}, map[string]descendantWalkRow{})
	closedChild := newSession(t)
	if err := closedChild.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_ = walk.collectDescendantJobs(closedChild, 1, listFilter{}, map[string]descendantWalkRow{})
	leaf := newSession(t)
	leafSubagents := leaf.subagents
	leaf.subagents = nil
	_ = walk.collectDescendantJobs(leaf, 1, listFilter{}, map[string]descendantWalkRow{})
	leaf.subagents = leafSubagents
	dup := map[string]descendantWalkRow{"a": {rec: &jobstore.JobRecord{JobID: "a"}, isOwner: true}}
	_ = walk.collectDescendantJobs(walk, 0, listFilter{}, dup)

	// A three-level tree exercises recursive resolution and both guidance forms.
	parent := newSession(t)
	child := newSession(t)
	grandchild := newSession(t)
	parent.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child}
	child.subagents.subs[grandchild.ID()] = &subagent{id: grandchild.ID(), sess: grandchild}
	start(t, child.jobManager, "deep", grandchild.ID(), "child-delegate", jobstore.JobShell, "")
	start(t, grandchild.jobManager, "deep", grandchild.ID(), "child-delegate", jobstore.JobShell, "")
	_, _, _, _, _ = parent.resolveDescendantJobOwner("deep")
	if err := parent.notControllableDescendantError("deep"); err == nil || !strings.Contains(err.Error(), "stop your direct delegate for session") {
		t.Fatalf("guidance without handle = %v", err)
	}
	start(t, parent.jobManager, "root-delegate", parent.ID(), "", jobstore.JobDelegate, encodeRef("", child.ID()))
	_ = parent.notControllableDescendantError("deep")
	greatGrandchild := newSession(t)
	grandchild.subagents.subs[greatGrandchild.ID()] = &subagent{id: greatGrandchild.ID(), sess: greatGrandchild}
	start(t, grandchild.jobManager, "deeper", greatGrandchild.ID(), "grandchild-delegate", jobstore.JobShell, "")
	start(t, greatGrandchild.jobManager, "deeper", greatGrandchild.ID(), "grandchild-delegate", jobstore.JobShell, "")
	_, _, _, _, _ = parent.resolveDescendantJobOwner("deeper")

	// The owner can disappear between resolution and the authoritative read.
	ownerRoot := newSession(t)
	ownerChild := newSession(t)
	ownerRoot.subagents.subs[ownerChild.ID()] = &subagent{id: ownerChild.ID(), sess: ownerChild}
	start(t, ownerRoot.jobManager, "owner-race", ownerChild.ID(), "delegate", jobstore.JobShell, "")
	start(t, ownerChild.jobManager, "owner-race", ownerChild.ID(), "delegate", jobstore.JobShell, "")
	originalFind := nestedFindJobRecord
	nestedFindJobRecord = func(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
		if jm == ownerChild.jobManager {
			return nil, jobstore.ErrStoreClosed
		}
		return originalFind(jm, jobID)
	}
	_, _, _ = ownerRoot.nestedOrLocalJobManager("owner-race")
	nestedFindJobRecord = func(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
		if jm == ownerChild.jobManager {
			return nil, errors.New("owner read fault")
		}
		return originalFind(jm, jobID)
	}
	_, _, _ = ownerRoot.nestedOrLocalJobManager("owner-race")
	nestedFindJobRecord = originalFind

	// A stale forwarded owner plus a deeper live owner produces guidance.
	start(t, parent.jobManager, "deep-forwarded", "gone-owner", "delegate", jobstore.JobShell, "")
	start(t, child.jobManager, "deep-forwarded", grandchild.ID(), "delegate", jobstore.JobShell, "")
	start(t, grandchild.jobManager, "deep-forwarded", grandchild.ID(), "delegate", jobstore.JobShell, "")
	_, _ = parent.stopNestedOrLocal("deep-forwarded")

	// Delegate cascade rejection branches and a stop append failure.
	start(t, parent.jobManager, "bad-ref", parent.ID(), "", jobstore.JobDelegate, "not-a-ref")
	_ = parent.delegateChildSessionToCascade("bad-ref")
	nilJM := newSession(t)
	nilJMOriginal := nilJM.jobManager
	nilJM.jobManager = nil
	parent.subagents.subs[nilJM.ID()] = &subagent{id: nilJM.ID(), sess: nilJM}
	start(t, parent.jobManager, "nil-jm", parent.ID(), "", jobstore.JobDelegate, encodeRef("", nilJM.ID()))
	_ = parent.delegateChildSessionToCascade("nil-jm")
	nilJM.jobManager = nilJMOriginal
	failing := newSession(t)
	run := &runningJob{rec: &jobstore.JobRecord{JobID: "stop-fault", Type: jobstore.JobDelegate, DelegateID: "dlg", Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}}
	failing.jobManager.running[run.rec.JobID] = run
	failing.jobManager.appendEvent = func(jobstore.Event) error { return errors.New("stop fault") }
	_, _ = parent.stopDelegateSubtree(failing)

	// Direct forwarding and recovery failures at both event positions.
	jm := newTestJM(t)
	jm.parentJobID = "parent"
	jm.forward = func(jobstore.Event) error { return errors.New("forward fault") }
	_ = jm.forwardLocked(jobstore.Event{})
	terminal := "terminal"
	start(t, jm, "terminal", jm.sessionID, "parent", jobstore.JobShell, "")
	if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: started, JobID: "terminal", Status: jobstore.StatusCompleted, TerminalGen: terminal}); err != nil {
		t.Fatal(err)
	}
	_ = jm.recoverForwardedTerminalEvents()
	calls := 0
	jm.forward = func(jobstore.Event) error {
		calls++
		if calls == 2 {
			return errors.New("finish fault")
		}
		return nil
	}
	_ = jm.recoverForwardedTerminalEvents()
	if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: started, JobID: "terminal", TerminalGen: terminal}); err != nil {
		t.Fatal(err)
	}
	jm.forward = func(jobstore.Event) error { return errors.New("pending fault") }
	_ = jm.recoverForwardedPendingNotifications()

	// Notification for an unknown/non-deliverable record returns without enqueue.
	jm2 := newTestJM(t)
	jm2.enqueue = func(jobNotification) { t.Fatal("unexpected enqueue") }
	_ = jm2.forwardEvent(jobstore.Event{Kind: jobstore.EventJobNotificationPending, JobID: "missing", TerminalGen: "gen"})
	jm3 := newTestJM(t)
	jm3.enqueue = func(jobNotification) {}
	forwardEventAfterAppend = func(got *jobManager) {
		if got == jm3 {
			_ = got.store.Close()
		}
	}
	_ = jm3.forwardEvent(jobstore.Event{Kind: jobstore.EventJobNotificationPending, JobID: "missing", TerminalGen: "gen"})
	forwardEventAfterAppend = nil
}
