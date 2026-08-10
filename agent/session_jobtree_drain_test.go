package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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

// TestOutstandingDrainJobCountIncludesRunningManagedJobs verifies every owned
// managed job keeps the drain open while it remains in the running map. Shell
// execution mode is not part of that durable drain contract.
func TestOutstandingDrainJobCountIncludesRunningManagedJobs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  *jobstore.JobRecord
	}{
		{name: "delegate", rec: &jobstore.JobRecord{JobID: "del-live", Type: jobstore.JobDelegate, Status: jobstore.StatusRunning}},
		{name: "explicit background shell", rec: &jobstore.JobRecord{JobID: "sh-bg", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: true}},
		{name: "foreground-promoted shell", rec: &jobstore.JobRecord{JobID: "sh-promoted", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newSession(t)
			jm := sess.jobManager
			tt.rec.OwnerSessionID = sess.ID()
			jm.mu.Lock()
			jm.running[tt.rec.JobID] = &runningJob{rec: tt.rec}
			jm.mu.Unlock()
			defer func() {
				jm.mu.Lock()
				delete(jm.running, tt.rec.JobID)
				jm.mu.Unlock()
			}()

			if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
				t.Fatalf("owned running managed job count = %d (err %v), want 1", n, err)
			}
		})
	}
}

// TestDrainJobTreeReturnsOnContextCancel verifies the wait is bounded: a
// managed job that never completes (never signals, never leaves the running
// map) must not hang the drain forever — a cancelled context releases it.
func TestDrainJobTreeReturnsOnContextCancel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		typ  jobstore.JobType
	}{
		{name: "delegate", typ: jobstore.JobDelegate},
		{name: "shell", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sess := newSession(t)
			jm := sess.jobManager
			rec := &jobstore.JobRecord{JobID: "stuck-" + tt.name, Type: tt.typ, Status: jobstore.StatusRunning, OwnerSessionID: sess.ID()}
			jm.mu.Lock()
			jm.running[rec.JobID] = &runningJob{rec: rec}
			jm.mu.Unlock()
			defer func() {
				jm.mu.Lock()
				delete(jm.running, rec.JobID)
				jm.mu.Unlock()
			}()

			if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
				t.Fatalf("precondition: owned running managed job count = %d (err %v), want 1", n, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := sess.DrainJobTree(ctx)
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("DrainJobTree error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("DrainJobTree did not return after context cancellation; hang-safety backstop failed")
			}
		})
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
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("a NotifyPending delegate absent from the running map must still count as outstanding, got %d (err %v)", n, err)
	}
}

// TestTreeHasOutstandingWorkSkipsStopGatedChild is the roborev regression: the
// drive path (driveChildrenWithUndeliveredAttention) skips stop-gated children,
// so the quiescence walk must too. A deliberately stopped child's leftover
// attention is never delivered, so counting it would hang DrainJobTree until
// ctx cancellation.
func TestTreeHasOutstandingWorkSkipsStopGatedChild(t *testing.T) {
	t.Parallel()
	root := newSession(t)
	child := newSession(t)
	childID := child.ID()

	// Give the child leftover child-local attention.
	child.enqueueJobNotification(jobNotification{JobID: "leftover"})

	// Track it as a live direct subagent of root, and untrack before close.
	root.subagents.mu.Lock()
	root.subagents.subs[childID] = &subagent{id: childID, sess: child}
	root.subagents.mu.Unlock()
	defer func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, childID)
		root.subagents.mu.Unlock()
	}()

	// Before stop-gating, the child's pending attention is outstanding.
	if out, err := root.treeHasOutstandingWork(); err != nil || !out {
		t.Fatalf("precondition: expected outstanding work from child pending attention, got out=%v err=%v", out, err)
	}

	// Stop-gate the child: its latest delegate record is Cancelled/stopped_by_parent.
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_sc", Type: jobstore.JobDelegate, OwnerSessionID: root.ID(), VisibleToSession: root.ID(), TranscriptRef: encodeRef("", childID), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_sc", Status: jobstore.StatusCancelled, Reason: "stopped_by_parent", EndedAt: &ended, TerminalGen: "gen_sc"},
	} {
		if err := root.jobManager.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
	if !root.childStopGated(childID) {
		t.Fatal("precondition: child should be stop-gated after Cancelled/stopped_by_parent")
	}

	// The stop-gated child's leftover attention must no longer count as
	// outstanding — the drain would otherwise never deliver it and hang.
	if out, err := root.treeHasOutstandingWork(); err != nil || out {
		t.Fatalf("stop-gated child must not count as outstanding, got out=%v err=%v", out, err)
	}
}

// TestOutstandingDrainJobCountIgnoresForwardedDescendantPending verifies the
// count only holds the drain open for THIS session's own managed-job notifications.
// A forwarded descendant pending copy (a drive signal owned by a child session)
// must not count here — the descendant is covered by the recursive tree walk,
// and counting the forwarded copy would hang the drain when that child is
// stop-gated and nothing settles the copy.
func TestOutstandingDrainJobCountIgnoresForwardedDescendantPending(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		typ  jobstore.JobType
	}{
		{name: "delegate", typ: jobstore.JobDelegate},
		{name: "shell", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sess := newSession(t)
			jm := sess.jobManager
			started := frozenTestTime.Add(-time.Second)
			ended := frozenTestTime
			for _, ev := range []jobstore.Event{
				{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_fwd", Type: tt.typ, OwnerSessionID: "CHILD-SESSION", VisibleToSession: sess.ID(), TranscriptRef: encodeRef("", "grandchild"), StartedAt: &started},
				{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_fwd", Status: jobstore.StatusCompleted, Reason: "communicated", EndedAt: &ended, TerminalGen: "gen_fwd"},
				{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "job_fwd", TerminalGen: "gen_fwd"},
			} {
				if err := jm.appendEvent(ev); err != nil {
					t.Fatalf("append %s: %v", ev.Kind, err)
				}
			}
			if n, err := jm.outstandingDrainJobCount(); err != nil || n != 0 {
				t.Fatalf("a forwarded descendant pending copy must not count as this session's own outstanding managed job, got %d (err %v)", n, err)
			}
		})
	}
}

// TestDrainSettlesRootDurableOnlyPending is the kata h8mq regression: a
// delegate whose owner notification survives ONLY in the durable ledger
// (NotifyState==NotifyPending) with nothing queued in memory (peek==0) must not
// wedge the drain. outstandingDrainJobCount reads the durable ledger, so it
// counts the delegate as outstanding forever; but the loop's delivery gate is
// the in-memory queue, so without re-materializing the durable pending the drain
// never runs a notification turn and blocks until ctx cancellation. This state
// arises when a finalize's in-memory enqueue never lands or a revived delegate's
// deferred restore side effects are interrupted before arm_notifications.
func TestDrainSettlesRootDurableOnlyPending(t *testing.T) {
	t.Parallel()
	steps := make([]func(llm.Request) llm.Response, 6)
	for i := range steps {
		steps[i] = func(llm.Request) llm.Response { return finalResponse("ack") }
	}
	sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{NoProjectPrompts: true}))
	jm := sess.jobManager

	// Durable owned delegate whose notification is Pending, absent from memory.
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "del-r", Type: jobstore.JobDelegate, OwnerSessionID: sess.ID(), VisibleToSession: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "del-r", Status: jobstore.StatusCompleted, Reason: "communicated", EndedAt: &ended, TerminalGen: "rgen"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "del-r", TerminalGen: "rgen"},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
	}
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("precondition: expected 1 outstanding, got %d (err %v)", n, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.DrainJobTree(ctx); err != nil {
		t.Fatalf("DrainJobTree wedged on a durable-only pending: %v", err)
	}
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 0 {
		t.Fatalf("expected 0 outstanding after drain, got %d (err %v)", n, err)
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("expected 0 pending notifications after drain, got %d", p)
	}
}

// TestDrainSettlesAlreadyInjectedDurablePending is the adversarial-review
// regression for kata h8mq: an owned NotifyPending delegate whose
// <job-notification> block is ALREADY in history (a crash between
// appendSteeringTurnDurably and markJobNotificationsDelivered left it injected but
// unmarked) with an empty in-memory queue is still counted outstanding, so the
// drain must re-materialize it. The already-injected record settles through the
// injectedJobNotifs path, which marks it Delivered WITHOUT re-appending to
// history — so the drain settles and the block is not duplicated.
func TestDrainSettlesAlreadyInjectedDurablePending(t *testing.T) {
	t.Parallel()
	steps := make([]func(llm.Request) llm.Response, 6)
	for i := range steps {
		steps[i] = func(llm.Request) llm.Response { return finalResponse("ack") }
	}
	sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{NoProjectPrompts: true}))
	jm := sess.jobManager

	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "del-r", Type: jobstore.JobDelegate, OwnerSessionID: sess.ID(), VisibleToSession: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "del-r", Status: jobstore.StatusCompleted, Reason: "communicated", EndedAt: &ended, TerminalGen: "rgen"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "del-r", TerminalGen: "rgen"},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}

	// The notification block is already in history but was never marked Delivered.
	injected := `<job-notification job_id="del-r" status="completed">\ndelegate del-r completed\n</job-notification>`
	sess.appendTurn(schema.TurnSteering, llm.User(injected))
	if !sess.jobNotificationAlreadyInjected("del-r") {
		t.Fatal("precondition: del-r must be detected as already injected in history")
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
	}
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("precondition: expected 1 outstanding, got %d (err %v)", n, err)
	}

	blocksBefore := countHistoryNeedle(sess, `job_id="del-r"`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.DrainJobTree(ctx); err != nil {
		t.Fatalf("DrainJobTree wedged on an already-injected durable pending: %v", err)
	}
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 0 {
		t.Fatalf("expected 0 outstanding after drain, got %d (err %v)", n, err)
	}
	if blocksAfter := countHistoryNeedle(sess, `job_id="del-r"`); blocksAfter != blocksBefore {
		t.Fatalf("already-injected block must not be re-appended: had %d, now %d", blocksBefore, blocksAfter)
	}
}

func countHistoryNeedle(s *Session, needle string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, turn := range s.history {
		if strings.Contains(turn.Message.Text(), needle) {
			n++
		}
	}
	return n
}

// TestDrainSettlesChildDurableOnlyPending is the recursive (isolation-delegate)
// form of kata h8mq: a live retained child whose OWN subtree carries a durable-
// only pending (peek==0) must not wedge the root drain. treeHasOutstandingWork
// recurses into the child and counts its durable pending, but
// driveChildrenWithUndeliveredAttention only drives a child whose in-memory queue
// is non-empty — so the child is never driven to settle it. The drain kick must
// re-materialize the child's stranded pending so the existing drive-down path
// delivers it.
func TestDrainSettlesChildDurableOnlyPending(t *testing.T) {
	t.Parallel()
	rootSteps := make([]func(llm.Request) llm.Response, 8)
	for i := range rootSteps {
		rootSteps[i] = func(llm.Request) llm.Response { return finalResponse("root-ack") }
	}
	root := newSession(t, withSteps(rootSteps...), withConfig(SessionConfig{NoProjectPrompts: true}))

	childSteps := make([]func(llm.Request) llm.Response, 8)
	for i := range childSteps {
		childSteps[i] = func(llm.Request) llm.Response { return finalResponse("child-ack") }
	}
	child := newSession(t, withSteps(childSteps...), withConfig(SessionConfig{NoProjectPrompts: true}))
	childID := child.ID()

	root.subagents.mu.Lock()
	root.subagents.subs[childID] = &subagent{id: childID, sess: child}
	root.subagents.mu.Unlock()
	defer func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, childID)
		root.subagents.mu.Unlock()
	}()

	// The child owns a durable delegate whose notification is Pending, absent
	// from the child's in-memory queue.
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "gc-job", Type: jobstore.JobDelegate, OwnerSessionID: child.ID(), VisibleToSession: child.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "gc-job", Status: jobstore.StatusCompleted, Reason: "communicated", EndedAt: &ended, TerminalGen: "gcgen"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "gc-job", TerminalGen: "gcgen"},
	} {
		if err := child.jobManager.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
	if p := child.peekNotifications(); p != 0 {
		t.Fatalf("precondition: child in-memory queue must be empty, got %d", p)
	}
	if out, err := root.treeHasOutstandingWork(); err != nil || !out {
		t.Fatalf("precondition: expected outstanding work from child durable pending, got out=%v err=%v", out, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.DrainJobTree(ctx); err != nil {
		t.Fatalf("DrainJobTree wedged on a child's durable-only pending: %v", err)
	}
	if n, err := child.jobManager.outstandingDrainJobCount(); err != nil || n != 0 {
		t.Fatalf("expected child's pending settled after drain, got %d (err %v)", n, err)
	}
}

// TestDrainJobTreeWaitsForRunningDelegate verifies the drain re-drives the
// coordinator when a backgrounded delegate completes, rather than returning
// while the child is still running.
func TestDrainJobTreeWaitsForRunningDelegate(t *testing.T) {
	t.Parallel()
	// Every turn (the delegate child's, then this session's notification turns)
	// cleanly communicates end_turn, so the delegate completes via the real
	// result-tool path rather than a bare-text turn error.
	steps := make([]func(llm.Request) llm.Response, 12)
	for i := range steps {
		steps[i] = func(llm.Request) llm.Response { return finalResponse("done") }
	}
	sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{
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
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 0 {
		t.Fatalf("expected 0 in-flight delegates after drain, got %d (err %v)", n, err)
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("expected 0 pending notifications after drain, got %d", p)
	}
}
