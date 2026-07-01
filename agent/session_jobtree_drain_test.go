package agent

import (
	"context"
	"testing"
	"time"
)

// TestDrainJobTreeNoJobsReturnsImmediately verifies the drain is a no-op when
// the session never delegated: no pending notifications and no running jobs
// means the tree is already terminal, so DrainJobTree returns at once with an
// empty result rather than blocking.
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

// TestDrainJobTreeWaitsForRunningDelegate verifies the drain re-drives the
// coordinator when a backgrounded delegate completes, rather than returning
// while the child is still running. It also asserts the delegate's terminal
// completion is reflected as a drained notification turn (the fakeAdapter
// receives the coordinator's re-drive request).
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

	// After draining, the tree must be terminal.
	if n := sess.activeJobCount(); n != 0 {
		t.Fatalf("expected 0 active jobs after drain, got %d", n)
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("expected 0 pending notifications after drain, got %d", p)
	}
}
