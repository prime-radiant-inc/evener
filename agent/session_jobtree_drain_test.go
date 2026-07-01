package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestDrainJobTreeNoJobsReturnsImmediately verifies the drain is a no-op when
// the session never delegated: no pending notifications and no in-flight
// delegates means the tree is already terminal, so DrainJobTree returns at once
// with an empty result rather than blocking.
func TestDrainJobTreeNoJobsReturnsImmediately(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	done := make(chan struct{})
	var res string
	var err error
	go func() {
		res, err = sess.DrainJobTree(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DrainJobTree blocked with no jobs in flight; expected immediate return")
	}
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if res != "" {
		t.Fatalf("expected empty result when no drain turn ran, got %q", res)
	}
}

// TestDrainJobTreeIgnoresBackgroundShell verifies the drain does not block on a
// long-lived background shell job: only delegates hold it open. A one-shot run
// whose model leaves a background shell running (e.g. a dev server) and then
// communicates must still exit promptly rather than hang until the shell dies.
func TestDrainJobTreeIgnoresBackgroundShell(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	// Inject a running background shell job into the manager's running map, then
	// remove it before the session closes (the bare record has none of the
	// lifecycle channels Close() would touch).
	jm := sess.jobManager
	jm.mu.Lock()
	jm.running["sh-1"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:  "sh-1",
		Type:   jobstore.JobShell,
		Status: jobstore.StatusRunning,
	}}
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.running, "sh-1")
		jm.mu.Unlock()
	}()

	if n, err := jm.outstandingDelegateCount(); err != nil || n != 0 {
		t.Fatalf("background shell must not count as an in-flight delegate, got %d (err %v)", n, err)
	}

	done := make(chan struct{})
	go func() {
		_, _ = sess.DrainJobTree(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DrainJobTree blocked on a background shell job; expected immediate return")
	}
}

// TestDrainJobTreeReturnsOnContextCancel verifies the wait is bounded: a
// delegate that never completes (never signals, never leaves the running map)
// must not hang the drain forever — a cancelled context releases it. This is
// the hang-safety backstop for a one-shot run whose delegated work stalls.
func TestDrainJobTreeReturnsOnContextCancel(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	// A delegate that will never signal completion.
	jm := sess.jobManager
	jm.mu.Lock()
	jm.running["del-stuck"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:  "del-stuck",
		Type:   jobstore.JobDelegate,
		Status: jobstore.StatusRunning,
	}}
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.running, "del-stuck")
		jm.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.DrainJobTree(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a context error when the delegate never completes, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DrainJobTree did not return after context cancellation; hang-safety backstop failed")
	}
}

// TestOutstandingDelegateCountCoversPendingNotifyWindow is the roborev
// regression: finalization deletes a delegate from the running map (jobs.go
// armFinalizedJob) BEFORE enqueueing its in-memory owner notification, leaving a
// window where the job is neither running nor pending in memory. The durable
// EventJobNotificationPending is written before that delete, so a delegate whose
// notification is NotifyPending but not yet in the in-memory queue must still
// count as outstanding — otherwise DrainJobTree could return in that window and
// let Close() skip the coordinator's final turn.
func TestOutstandingDelegateCountCoversPendingNotifyWindow(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	jm := sess.jobManager

	// Reproduce the post-delete/pre-enqueue state durably: the delegate finished
	// and its notification-pending event is recorded, but it is not in the running
	// map and not yet in the in-memory notification queue.
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	code := 0
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "del-window", Type: jobstore.JobDelegate, OwnerSessionID: sess.ID(), VisibleToSession: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "del-window", Status: jobstore.StatusCompleted, Reason: "communicated", ExitCode: &code, EndedAt: &ended, TerminalGen: "gen-window"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "del-window", TerminalGen: "gen-window"},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}

	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
	}
	if n, err := jm.outstandingDelegateCount(); err != nil || n != 1 {
		t.Fatalf("a NotifyPending delegate absent from the running map must still count as outstanding, got %d (err %v)", n, err)
	}
}

// TestDrainJobTreeWaitsForRunningDelegate verifies the drain re-drives the
// coordinator when a backgrounded delegate completes, rather than returning
// while the child is still running.
func TestDrainJobTreeWaitsForRunningDelegate(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
	}))

	// Background a delegate; it runs to completion in its own goroutine and
	// enqueues a completion notification on this session.
	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "do a small unit of work and communicate done",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.DrainJobTree(ctx); err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}

	// After draining, no delegate may remain in flight and nothing may be pending.
	if n, err := sess.jobManager.outstandingDelegateCount(); err != nil || n != 0 {
		t.Fatalf("expected 0 in-flight delegates after drain, got %d (err %v)", n, err)
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("expected 0 pending notifications after drain, got %d", p)
	}
}
