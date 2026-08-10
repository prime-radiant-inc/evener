package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// seedOwnedDurablePending writes an owned terminal job whose notification
// survives only in the durable ledger, with nothing queued in memory.
func seedOwnedDurablePending(t *testing.T, jm *jobManager, jobID string, jobType jobstore.JobType) {
	t.Helper()
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	reason := "communicated"
	if jobType == jobstore.JobShell {
		reason = "exit_zero"
	}
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobType, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, Reason: reason, EndedAt: &ended, TerminalGen: "gen-" + jobID},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: jobID, TerminalGen: "gen-" + jobID},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
}

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

func TestCancelledManagedShellDrainStopsOnSessionClose(t *testing.T) {
	sess := newSession(t)
	executor := newSignalCompletesStreamingExecutor()
	result := runShell(context.Background(), sess.jobManager, executor, shellArgs{
		Command:    "held managed shell",
		Background: true,
	})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background shell result = %+v, want a managed running job", result)
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("outstanding managed jobs = %d (err %v), want 1", n, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	recheck := make(chan time.Time)
	kickEntered := make(chan int)
	releaseSecondKick := make(chan struct{})
	secondKickReleased := false
	releaseSecond := func() {
		if !secondKickReleased {
			close(releaseSecondKick)
			secondKickReleased = true
		}
	}
	t.Cleanup(releaseSecond)
	kickCount := 0
	drainDone := make(chan error, 1)
	go func() {
		_, err := sess.drainJobTreeWith(ctx, recheck, func(ctx context.Context) error {
			kickCount++
			select {
			case kickEntered <- kickCount:
			case <-ctx.Done():
				return ctx.Err()
			}
			if kickCount == 2 {
				<-releaseSecondKick
			}
			return sess.kickDriveTree(ctx)
		}, sess.ProcessInputKind)
		drainDone <- err
	}()
	select {
	case kick := <-kickEntered:
		if kick != 1 {
			t.Fatalf("first drain kick = %d, want 1", kick)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed-job drain did not enter its first cycle")
	}
	tickConsumed := make(chan struct{})
	go func() {
		select {
		case recheck <- time.Time{}:
			close(tickConsumed)
		case <-ctx.Done():
		}
	}()
	select {
	case <-tickConsumed:
	case <-time.After(5 * time.Second):
		t.Fatal("managed-job drain did not consume the controlled recheck")
	}
	select {
	case kick := <-kickEntered:
		if kick != 2 {
			t.Fatalf("second drain kick = %d, want 2", kick)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed-job drain did not re-enter after consuming its wait")
	}
	select {
	case err := <-drainDone:
		t.Fatalf("managed-job drain returned before caller cancellation: %v", err)
	default:
	}
	cancel()
	releaseSecond()
	select {
	case err := <-drainDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DrainJobTree error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DrainJobTree did not return after caller cancellation")
	}

	sess.Close()
	if signals := executor.signals.Load(); signals != 1 {
		t.Fatalf("shutdown signals = %d, want 1", signals)
	}
	select {
	case <-executor.done:
	default:
		t.Fatal("managed shell remained running after session shutdown")
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
	childAdapter := &fakeAdapter{name: "openai"}
	child := newSession(t, withAdapter(childAdapter))
	childID := child.ID()

	// Give the child an owned shell completion that exists only in its durable
	// ledger. A drain kick must not materialize or deliver it after the parent
	// deliberately stop-gates the child.
	seedOwnedDurablePending(t, child.jobManager, "shell-leftover", jobstore.JobShell)

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

	requestsBefore := len(childAdapter.Requests())
	queuedBefore := child.peekNotifications()
	if err := root.kickDriveTree(context.Background()); err != nil {
		t.Fatalf("kickDriveTree: %v", err)
	}

	// The stop-gated child's leftover durable attention must stay outside the
	// live drain tree: it is neither re-materialized nor delivered, and it no
	// longer counts as outstanding.
	if out, err := root.treeHasOutstandingWork(); err != nil || out {
		t.Fatalf("stop-gated child must not count as outstanding, got out=%v err=%v", out, err)
	}
	if requestsAfter := len(childAdapter.Requests()); requestsAfter != requestsBefore {
		t.Fatalf("child provider requests changed across stop-gated drain kick: before=%d after=%d", requestsBefore, requestsAfter)
	}
	if queuedAfter := child.peekNotifications(); queuedAfter != queuedBefore {
		t.Fatalf("child notification queue changed across stop-gated drain kick: before=%d after=%d", queuedBefore, queuedAfter)
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

// TestRematerializeOwnedDrainJobPendings verifies the queue reconstruction uses
// the same ownership and managed-job eligibility as durable drain accounting.
// Reverting rematerialization to its former delegate-only filter would strand
// the owned shell and fail this test.
func TestRematerializeOwnedDrainJobPendings(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	jm := sess.jobManager
	seedOwnedDurablePending(t, jm, "del-owned", jobstore.JobDelegate)
	seedOwnedDurablePending(t, jm, "shell-owned", jobstore.JobShell)

	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "shell-forwarded", Type: jobstore.JobShell, OwnerSessionID: "child-session", VisibleToSession: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "shell-forwarded", Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &ended, TerminalGen: "gen-shell-forwarded"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "shell-forwarded", TerminalGen: "gen-shell-forwarded"},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}

	if err := sess.rematerializeDurablePendings(); err != nil {
		t.Fatalf("rematerializeDurablePendings: %v", err)
	}
	sess.pendingJobNotifsMu.Lock()
	got := append([]jobNotification(nil), sess.pendingJobNotifs...)
	sess.pendingJobNotifsMu.Unlock()
	if len(got) != 2 {
		t.Fatalf("rematerialized notifications = %+v, want two owned jobs", got)
	}
	gotTypes := make(map[string]string, len(got))
	for _, notification := range got {
		gotTypes[notification.JobID] = notification.JobType
	}
	if gotTypes["del-owned"] != "delegate" || gotTypes["shell-owned"] != "shell" {
		t.Fatalf("rematerialized job ids and types = %v, want del-owned delegate and shell-owned shell", gotTypes)
	}
}

// TestDrainSettlesRootDurableOnlyPending is the kata h8mq regression: a
// managed job whose owner notification survives ONLY in the durable ledger
// (NotifyState==NotifyPending) with nothing queued in memory (peek==0) must not
// wedge the drain. outstandingDrainJobCount reads the durable ledger, so it
// counts the delegate as outstanding forever; but the loop's delivery gate is
// the in-memory queue, so without re-materializing the durable pending the drain
// never runs a notification turn and blocks until ctx cancellation. This state
// arises when a finalize's in-memory enqueue never lands or a revived job's
// deferred restore side effects are interrupted before arm_notifications.
func TestDrainSettlesRootDurableOnlyPending(t *testing.T) {
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "delegate", jobID: "del-root", typ: jobstore.JobDelegate},
		{name: "shell", jobID: "shell-root", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]func(llm.Request) llm.Response, 6)
			for i := range steps {
				steps[i] = func(llm.Request) llm.Response { return finalResponse("ack") }
			}
			sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{NoProjectPrompts: true}))
			jm := sess.jobManager
			seedOwnedDurablePending(t, jm, tt.jobID, tt.typ)
			if p := sess.peekNotifications(); p != 0 {
				t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.DrainJobTree(ctx); err != nil {
				t.Fatalf("DrainJobTree wedged on a durable-only pending: %v", err)
			}
			requireNotificationState(t, jm, tt.jobID, jobstore.NotifyDelivered)
			if p := sess.peekNotifications(); p != 0 {
				t.Fatalf("expected 0 pending notifications after drain, got %d", p)
			}
		})
	}
}

// TestDrainSettlesAlreadyInjectedDurablePending is the adversarial-review
// regression for kata h8mq: an owned NotifyPending managed job whose
// <job-notification> block is ALREADY in history (a crash between
// appendSteeringTurnDurably and markJobNotificationsDelivered left it injected but
// unmarked) with an empty in-memory queue is still counted outstanding, so the
// drain must re-materialize it. The already-injected record settles through the
// injectedJobNotifs path, which marks it Delivered WITHOUT re-appending to
// history — so the drain settles and the block is not duplicated.
func TestDrainSettlesAlreadyInjectedDurablePending(t *testing.T) {
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "delegate", jobID: "del-injected", typ: jobstore.JobDelegate},
		{name: "shell", jobID: "shell-injected", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]func(llm.Request) llm.Response, 6)
			for i := range steps {
				steps[i] = func(llm.Request) llm.Response { return finalResponse("ack") }
			}
			sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{NoProjectPrompts: true}))
			jm := sess.jobManager
			seedOwnedDurablePending(t, jm, tt.jobID, tt.typ)

			injected := `<job-notification job_id="` + tt.jobID + `" status="completed"></job-notification>`
			sess.appendTurn(schema.TurnSteering, llm.User(injected))
			if !sess.jobNotificationAlreadyInjected(tt.jobID) {
				t.Fatalf("precondition: %s must be detected as already injected in history", tt.jobID)
			}
			blocksBefore := countHistoryNeedle(sess, `job_id="`+tt.jobID+`"`)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.DrainJobTree(ctx); err != nil {
				t.Fatalf("DrainJobTree wedged on an already-injected durable pending: %v", err)
			}
			requireNotificationState(t, jm, tt.jobID, jobstore.NotifyDelivered)
			if blocksAfter := countHistoryNeedle(sess, `job_id="`+tt.jobID+`"`); blocksAfter != blocksBefore {
				t.Fatalf("already-injected block must not be re-appended: had %d, now %d", blocksBefore, blocksAfter)
			}
		})
	}
}

func requireNotificationState(t *testing.T, jm *jobManager, jobID string, want jobstore.NotifyState) {
	t.Helper()
	records, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	record := records[jobID]
	if record == nil || record.NotifyState != want {
		t.Fatalf("record %s notification state = %v, want %v", jobID, record, want)
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
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "delegate", jobID: "del-child", typ: jobstore.JobDelegate},
		{name: "shell", jobID: "shell-child", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
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

			seedOwnedDurablePending(t, child.jobManager, tt.jobID, tt.typ)
			if p := child.peekNotifications(); p != 0 {
				t.Fatalf("precondition: child in-memory queue must be empty, got %d", p)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := root.DrainJobTree(ctx); err != nil {
				t.Fatalf("DrainJobTree wedged on a child's durable-only pending: %v", err)
			}
			requireNotificationState(t, child.jobManager, tt.jobID, jobstore.NotifyDelivered)
		})
	}
}

func TestDrainJobTreeBatchesQueuedShellNotifications(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("batch handled") },
		},
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{NoProjectPrompts: true}))
	jm := sess.jobManager
	seedOwnedDurablePending(t, jm, "shell-batch-one", jobstore.JobShell)
	seedOwnedDurablePending(t, jm, "shell-batch-two", jobstore.JobShell)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if result != "batch handled" {
		t.Fatalf("DrainJobTree result = %q, want batch handled", result)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want one batched notification turn", len(requests))
	}
	if !requestsContain(requests, `job_id="shell-batch-one"`, `job_id="shell-batch-two"`) {
		t.Fatalf("batched request did not contain both shell notifications: %+v", requests)
	}
	requireNotificationState(t, jm, "shell-batch-one", jobstore.NotifyDelivered)
	requireNotificationState(t, jm, "shell-batch-two", jobstore.NotifyDelivered)
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

func TestDrainJobTreeWaitsForForegroundPromotedShell(t *testing.T) {
	clk := agenttest.NewFakeClock()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("foreground shell handled") },
		},
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		clock:            clk,
	}))
	sess.stopLaneResidueSweepTimer()
	se := newDelayedSuccessStreamingExecutor()
	releaseShell := func() {
		select {
		case <-se.release:
		default:
			close(se.release)
		}
	}
	t.Cleanup(releaseShell)
	resultCh := make(chan shellResult, 1)
	go func() {
		resultCh <- runShell(context.Background(), sess.jobManager, se, shellArgs{
			Command:        "delayed foreground success",
			BlockTimeoutMS: 1,
		})
	}()
	clk.BlockUntil(1)
	clk.Advance(time.Duration(clampShellBlockTimeoutMS(1)+1) * time.Millisecond)

	var shell shellResult
	select {
	case shell = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runShell did not return after the foreground wait bound")
	}
	if shell.JobID == "" || shell.Status != string(jobstore.StatusRunning) || shell.Reason != "foreground_timeout" {
		t.Fatalf("runShell result = %+v, want durable running foreground_timeout job", shell)
	}
	sess.jobManager.mu.Lock()
	live := sess.jobManager.running[shell.JobID]
	sess.jobManager.mu.Unlock()
	if live == nil {
		t.Fatalf("promoted shell %s missing from live jobs", shell.JobID)
	}
	if live.rec.Background {
		t.Fatal("foreground-promoted shell live record has Background=true, want false")
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("outstanding drain jobs = %d (err %v), want 1", n, err)
	}

	releaseShell()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if result != "foreground shell handled" {
		t.Fatalf("DrainJobTree result = %q, want foreground shell handled", result)
	}
	requests := adapter.Requests()
	if len(requests) != 1 || !requestsContain(requests, "<job-notification", `job_id="`+shell.JobID+`"`, `status="completed"`) {
		t.Fatalf("notification-turn requests = %+v, want one completed notification for %s", requests, shell.JobID)
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 0 {
		t.Fatalf("outstanding drain jobs after drain = %d (err %v), want 0", n, err)
	}
	if pending := sess.peekNotifications(); pending != 0 {
		t.Fatalf("queued notifications after drain = %d, want 0", pending)
	}
}
