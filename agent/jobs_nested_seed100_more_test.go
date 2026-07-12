//go:build serffuzz

package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

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
	_, _, _, _, _ = parent.resolveDescendantJobOwner("not-in-tree")

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
	_, _, _ = ownerRoot.nestedOrLocalJobManager("owner-race")

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
	run := &runningJob{rec: &jobstore.JobRecord{JobID: "stop-fault", Type: jobstore.JobDelegate, DelegateID: "dlg", Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}, durableStarted: true}
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

	// Remaining fuzz-only union guards from the nested job profile.
	_, _ = (*Session)(nil).ownerJobManagerFor("nil")
	guardRoot := newSession(t)
	guardChild := newSession(t)
	start(t, guardRoot.jobManager, "guard", guardChild.ID(), "parent", jobstore.JobShell, "")
	guardSubs := guardRoot.subagents
	guardRoot.subagents = nil
	_, _ = guardRoot.ownerJobManagerFor("guard")
	_, _, _ = guardRoot.nestedOrLocalJobManager("guard")
	_ = (&Session{}).liveDirectSubagents()
	directs := newSession(t)
	directs.subagents.subs["nil"] = nil
	directs.subagents.subs["empty"] = &subagent{id: "empty"}
	directClosed := newSession(t)
	directs.subagents.subs[directClosed.ID()] = &subagent{id: directClosed.ID(), sess: directClosed, closed: true}
	_ = directs.liveDirectSubagents()
	_ = liveSubagentSession(nil, "nil")
	_ = liveSubagentSession(directs.subagents, "absent")
	_ = liveSubagentSession(directs.subagents, "empty")
	_ = liveSubagentSession(directs.subagents, directClosed.ID())
	delete(directs.subagents.subs, "nil")
	delete(directs.subagents.subs, "empty")
	delete(directs.subagents.subs, directClosed.ID())
	guardRoot.subagents = guardSubs
	guardSub := &subagent{id: guardChild.ID(), sess: guardChild, closed: true}
	guardRoot.subagents.subs[guardChild.ID()] = guardSub
	_, _ = guardRoot.ownerJobManagerFor("guard")
	guardSub.closed = false
	if err := guardChild.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = guardRoot.ownerJobManagerFor("guard")

	// Root collection errors are surfaced; descendant errors remain best effort.
	_, _ = (&Session{}).walkDescendantJobs(listFilter{})
	closedWalk := newSession(t)
	if err := closedWalk.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = closedWalk.walkDescendantJobs(listFilter{})
	ordered := newSession(t)
	earlier := started.Add(-time.Second)
	if err := ordered.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: earlier, JobID: "earlier", Type: jobstore.JobShell, OwnerSessionID: ordered.ID(), VisibleToSession: ordered.ID(), StartedAt: &earlier}); err != nil {
		t.Fatal(err)
	}
	start(t, ordered.jobManager, "later", ordered.ID(), "", jobstore.JobShell, "")
	_, _ = ordered.walkDescendantJobs(listFilter{})
	_ = ordered.directDelegateJobForChild("absent")

	// Direct-owner stop success/error, dead forwarded terminal, and local stop.
	stopRoot := newSession(t)
	stopChild := newSession(t)
	stopRoot.subagents.subs[stopChild.ID()] = &subagent{id: stopChild.ID(), sess: stopChild}
	start(t, stopRoot.jobManager, "owned-stop", stopChild.ID(), "delegate", jobstore.JobShell, "")
	start(t, stopChild.jobManager, "owned-stop", stopChild.ID(), "delegate", jobstore.JobShell, "")
	stopRun := &runningJob{rec: &jobstore.JobRecord{JobID: "owned-stop", Type: jobstore.JobDelegate, DelegateID: "dlg", Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}, durableStarted: true}
	stopChild.jobManager.running[stopRun.rec.JobID] = stopRun
	stopChild.jobManager.appendEvent = func(jobstore.Event) error { return errors.New("owned stop fault") }
	_, _ = stopRoot.stopNestedOrLocal("owned-stop")
	delete(stopChild.jobManager.running, stopRun.rec.JobID)
	stopChild.jobManager.appendEvent = stopChild.jobManager.store.Append
	_, _ = stopRoot.stopNestedOrLocal("owned-stop")
	terminalTime := started
	if err := stopRoot.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: terminalTime, JobID: "owned-stop", Status: jobstore.StatusCompleted, TerminalGen: "term"}); err != nil {
		t.Fatal(err)
	}
	stopRoot.subagents.subs[stopChild.ID()].closed = true
	_, _ = stopRoot.stopNestedOrLocal("owned-stop")
	_, _ = stopRoot.stopNestedOrLocal("missing-local")
	_, _ = parent.stopNestedOrLocal("deep")
	deadOwner := newSession(t)
	start(t, deadOwner.jobManager, "dead-running", "gone", "delegate", jobstore.JobShell, "")
	_, _ = deadOwner.stopNestedOrLocal("dead-running")
	localTerminal := newSession(t)
	start(t, localTerminal.jobManager, "local-done", localTerminal.ID(), "", jobstore.JobShell, "")
	if err := localTerminal.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: started, JobID: "local-done", Status: jobstore.StatusCompleted, TerminalGen: "done"}); err != nil {
		t.Fatal(err)
	}
	_, _ = localTerminal.stopNestedOrLocal("local-done")

	// stopChildren load failure and child stop failure/success aggregation.
	closedStopChildren := newSession(t)
	if err := closedStopChildren.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = closedStopChildren.stopChildren("delegate")
	childrenRoot := newSession(t)
	childrenChild := newSession(t)
	childrenRoot.subagents.subs[childrenChild.ID()] = &subagent{id: childrenChild.ID(), sess: childrenChild}
	start(t, childrenRoot.jobManager, "dead-child", "gone-child", "delegate", jobstore.JobShell, "")
	for _, id := range []string{"child-fail", "child-ok"} {
		start(t, childrenRoot.jobManager, id, childrenChild.ID(), "delegate", jobstore.JobShell, "")
		start(t, childrenChild.jobManager, id, childrenChild.ID(), "delegate", jobstore.JobShell, "")
	}
	start(t, childrenRoot.jobManager, "other-parent", childrenRoot.ID(), "other", jobstore.JobShell, "")
	start(t, childrenRoot.jobManager, "already-done", childrenRoot.ID(), "delegate", jobstore.JobShell, "")
	if err := childrenRoot.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: started, JobID: "already-done", Status: jobstore.StatusCompleted, TerminalGen: "done"}); err != nil {
		t.Fatal(err)
	}
	childFailRun := &runningJob{rec: &jobstore.JobRecord{JobID: "child-fail", Type: jobstore.JobDelegate, DelegateID: "dlg", Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}, durableStarted: true}
	childrenChild.jobManager.running["child-fail"] = childFailRun
	childrenChild.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.JobID == "child-fail" {
			return errors.New("child fault")
		}
		return childrenChild.jobManager.store.Append(e)
	}
	_, _ = childrenRoot.stopChildren("delegate")

	// Closed delegate store, stale parent id, nil subtree manager, and closed lookup.
	_ = closedStopChildren.delegateChildSessionToCascade("delegate")
	staleRoot := newSession(t)
	staleChild := newSession(t)
	staleRoot.subagents.subs[staleChild.ID()] = &subagent{id: staleChild.ID(), sess: staleChild}
	start(t, staleRoot.jobManager, "stale", staleRoot.ID(), "", jobstore.JobDelegate, encodeRef("", staleChild.ID()))
	staleChild.jobManager.setParentJobID("newer")
	_ = staleRoot.delegateChildSessionToCascade("stale")
	noManager := &Session{}
	_, _ = staleRoot.stopDelegateSubtree(noManager)
	cascadeChild := newSession(t)
	cascadeGrandchild := newSession(t)
	cascadeChild.subagents.subs[cascadeGrandchild.ID()] = &subagent{id: cascadeGrandchild.ID(), sess: cascadeGrandchild}
	cascadeRun := &runningJob{rec: &jobstore.JobRecord{JobID: "cascade-ok", Type: jobstore.JobShell, Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}, durableStarted: true}
	cascadeGrandchild.jobManager.running[cascadeRun.rec.JobID] = cascadeRun
	_, _ = staleRoot.stopDelegateSubtree(cascadeChild)
	_ = closedStopChildren.directDelegateJobForChild("child")

	// Recovery skip rows and successful owner-scoped enqueue.
	recovery := newTestJM(t)
	_ = recovery.recoverForwardedTerminalEvents()
	_ = recovery.recoverForwardedPendingNotifications()
	recovery.parentJobID = "parent"
	recovery.forward = func(jobstore.Event) error { return nil }
	start(t, recovery, "running", recovery.sessionID, "parent", jobstore.JobShell, "")
	_ = recovery.recoverForwardedTerminalEvents()
	_ = recovery.recoverForwardedPendingNotifications()
	_ = recovery.shouldRecoverForwardedTerminalRecord(&jobstore.JobRecord{JobID: "no-gen", ParentJobID: "parent", OwnerSessionID: recovery.sessionID, Status: jobstore.StatusCompleted}, "parent")
	ended := started
	_ = recovery.recoveredEventTime(&jobstore.JobRecord{EndedAt: &ended})
	enqueued := false
	enqueueJM := newTestJM(t)
	enqueueJM.enqueue = func(jobNotification) { enqueued = true }
	start(t, enqueueJM, "local-terminal", enqueueJM.sessionID, "", jobstore.JobShell, "")
	if err := enqueueJM.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: started, JobID: "local-terminal", Status: jobstore.StatusCompleted, TerminalGen: "gen"}); err != nil {
		t.Fatal(err)
	}
	_ = enqueueJM.forwardEvent(jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: started, JobID: "local-terminal", TerminalGen: "gen"})
	if !enqueued {
		t.Fatal("local forwarded notification was not enqueued")
	}
	_ = enqueueJM.forwardEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "early", Type: jobstore.JobShell})
	descendantJM := newTestJM(t)
	descendantJM.enqueue = func(jobNotification) { t.Fatal("descendant notification enqueued locally") }
	start(t, descendantJM, "descendant-terminal", "child-owner", "delegate", jobstore.JobShell, "")
	if err := descendantJM.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: started, JobID: "descendant-terminal", Status: jobstore.StatusCompleted, TerminalGen: "gen"}); err != nil {
		t.Fatal(err)
	}
	_ = descendantJM.forwardEvent(jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: started, JobID: "descendant-terminal", TerminalGen: "gen"})
}
