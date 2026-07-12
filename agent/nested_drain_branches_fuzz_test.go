//go:build serffuzz

package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzNdbNestedDrainBranches complements the end-to-end nested lifecycle
// program with the state combinations that do not require a model turn. The
// fixture uses only in-memory session state and durable temp-dir stores; drain
// rechecks are injected directly, so replay and fuzzing never wait on a clock.
// serf:fuzz native
func FuzzNdbNestedDrainBranches(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2, 3, 7, 255} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, mode uint8) {
		nbdExerciseLiveChildFilters(t, mode)
		nbdExerciseOutstandingCounts(t)
		nbdExerciseDrainWaitBranches(t, mode)
	})
}

func nbdExerciseLiveChildFilters(t *testing.T, mode uint8) {
	t.Helper()
	root := newSession(t)
	child := newSession(t)

	if owner, rec := (*Session)(nil).ownerJobManagerFor("missing"); owner != nil || rec != nil {
		t.Fatalf("nil owner lookup = (%p, %+v), want nils", owner, rec)
	}
	if got := liveSubagentSession(nil, "missing"); got != nil {
		t.Fatalf("nil live session = %p, want nil", got)
	}
	if got := (&Session{}).liveSubagentSessions(); got != nil {
		t.Fatalf("nil-manager live sessions = %v, want nil", got)
	}
	if got := (&Session{}).liveDirectSubagents(); got != nil {
		t.Fatalf("nil-manager direct subagents = %v, want nil", got)
	}

	live := &subagent{id: child.ID(), sess: child}
	closed := &subagent{id: "closed", sess: child, closed: true}
	root.subagents.mu.Lock()
	root.subagents.subs["nil-sub"] = nil
	root.subagents.subs["nil-session"] = &subagent{id: "nil-session"}
	root.subagents.subs[closed.id] = closed
	root.subagents.subs[live.id] = live
	root.subagents.mu.Unlock()
	defer func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, "nil-sub")
		delete(root.subagents.subs, "nil-session")
		delete(root.subagents.subs, closed.id)
		delete(root.subagents.subs, live.id)
		root.subagents.mu.Unlock()
	}()

	if got := root.liveSubagentSessions(); len(got) != 1 || got[0] != child {
		t.Fatalf("live sessions = %v, want child only", got)
	}
	if got := root.liveDirectSubagents(); len(got) != 1 || got[0] != live {
		t.Fatalf("live direct subagents = %v, want live child only", got)
	}
	if got := liveSubagentSession(root.subagents, child.ID()); got != child {
		t.Fatalf("live session = %p, want %p", got, child)
	}
	if got := liveSubagentSession(root.subagents, "missing"); got != nil {
		t.Fatalf("missing live session = %p, want nil", got)
	}
	if got := liveSubagentSession(root.subagents, closed.id); got != nil {
		t.Fatalf("closed live session = %p, want nil", got)
	}

	for mask := uint8(0); mask < 8; mask++ {
		seen := mask&1 != 0
		existingOwner := mask&2 != 0
		incomingOwner := mask&4 != 0
		want := !seen || (!existingOwner && incomingOwner)
		if got := keepIncomingDescendantRow(seen, existingOwner, incomingOwner); got != want {
			t.Fatalf("keep row (%v,%v,%v) = %v, want %v", seen, existingOwner, incomingOwner, got, want)
		}
	}

	// Vary insertion order without changing the oracle, exercising the map-copy
	// path independently of Go's randomized map iteration.
	if mode&1 != 0 {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, live.id)
		root.subagents.subs[live.id] = live
		root.subagents.mu.Unlock()
		if got := liveSubagentSession(root.subagents, live.id); got != child {
			t.Fatalf("reinserted live session = %p, want %p", got, child)
		}
	}
}

func nbdExerciseOutstandingCounts(t *testing.T) {
	t.Helper()
	sess := newSession(t)
	jm := sess.jobManager

	jm.mu.Lock()
	jm.running["nil-record"] = &runningJob{}
	jm.running["shell"] = &runningJob{rec: &jobstore.JobRecord{JobID: "shell", Type: jobstore.JobShell}}
	jm.running["delegate"] = &runningJob{rec: &jobstore.JobRecord{JobID: "delegate", Type: jobstore.JobDelegate}}
	jm.mu.Unlock()

	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, event := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "delegate", Type: jobstore.JobDelegate, OwnerSessionID: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "delegate", Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen-delegate"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "delegate", TerminalGen: "gen-delegate"},
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "pending", Type: jobstore.JobDelegate, OwnerSessionID: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "pending", Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen-pending"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "pending", TerminalGen: "gen-pending"},
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "forwarded", Type: jobstore.JobDelegate, OwnerSessionID: "child", StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "forwarded", Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen-forwarded"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "forwarded", TerminalGen: "gen-forwarded"},
	} {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append %s for %q: %v", event.Kind, event.JobID, err)
		}
	}

	if got, err := jm.outstandingDelegateCount(); err != nil || got != 2 {
		t.Fatalf("outstanding delegate count = %d, %v; want 2", got, err)
	}
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("tree outstanding = %v, %v; want true", outstanding, err)
	}

	jm.mu.Lock()
	delete(jm.running, "nil-record")
	delete(jm.running, "shell")
	delete(jm.running, "delegate")
	jm.mu.Unlock()
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close job store: %v", err)
	}
	if _, err := jm.outstandingDelegateCount(); err == nil {
		t.Fatal("closed store outstanding count succeeded, want error")
	}
	if _, err := sess.treeHasOutstandingWork(); err == nil {
		t.Fatal("closed store tree walk succeeded, want error")
	}
}

func nbdExerciseDrainWaitBranches(t *testing.T, mode uint8) {
	t.Helper()
	if result, err := (&Session{}).DrainJobTree(context.Background()); err != nil || result != "" {
		t.Fatalf("nil-manager drain = %q, %v", result, err)
	}

	quiescent := newSession(t)
	if result, err := quiescent.DrainJobTree(context.Background()); err != nil || result != "" {
		t.Fatalf("quiescent drain = %q, %v", result, err)
	}
	if outstanding, err := (&Session{}).treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("bare tree outstanding = %v, %v; want false", outstanding, err)
	}

	stuck := newSession(t)
	stuck.jobManager.mu.Lock()
	stuck.jobManager.running["stuck"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "stuck",
		Type:  jobstore.JobDelegate,
	}}
	stuck.jobManager.mu.Unlock()
	t.Cleanup(func() {
		stuck.jobManager.mu.Lock()
		delete(stuck.jobManager.running, "stuck")
		stuck.jobManager.mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	recheck := make(chan time.Time, 1)
	if mode&1 != 0 {
		recheck <- frozenTestTime
	}
	cancel()
	if result, err := stuck.drainJobTree(ctx, recheck); err != context.Canceled || result != "" {
		t.Fatalf("cancelled drain = %q, %v; want context.Canceled", result, err)
	}

	root := newSession(t)
	child := newSession(t)
	sub := &subagent{id: child.ID(), sess: child, driving: true}
	root.subagents.mu.Lock()
	root.subagents.subs[sub.id] = sub
	root.subagents.mu.Unlock()
	defer func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, sub.id)
		root.subagents.mu.Unlock()
	}()
	if outstanding, err := root.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("driving child outstanding = %v, %v; want true", outstanding, err)
	}
	sub.mu.Lock()
	sub.driving = false
	sub.mu.Unlock()
	child.enqueueJobNotification(jobNotification{JobID: "pending-child"})
	if outstanding, err := root.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("child notification outstanding = %v, %v; want true", outstanding, err)
	}
}
