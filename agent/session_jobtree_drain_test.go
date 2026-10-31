package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// outstandingWorkReasons names every signal treeHasOutstandingWork consults that
// is currently true, in the same order that function checks them.
//
// treeHasOutstandingWork answers a bare yes/no over eight independent signals,
// so a drain that returns clean and a tree that is outstanding a moment later
// produce a failure message naming none of them. That is precisely the case
// worth diagnosing, and it reproduces only under heavy parallel load, where a
// re-run to add prints is expensive and may not fail again.
func outstandingWorkReasons(s *Session) []string {
	var reasons []string
	if s.jobManager != nil {
		if n, err := s.jobManager.outstandingDrainJobCount(); err != nil {
			reasons = append(reasons, "outstandingDrainJobCount error: "+err.Error())
		} else if n > 0 {
			reasons = append(reasons, fmt.Sprintf("outstandingDrainJobCount=%d", n))
		}
		if s.jobManager.hasPendingWatchSends() {
			reasons = append(reasons, "hasPendingWatchSends")
		}
	}
	if n := s.peekNotifications(); n > 0 {
		reasons = append(reasons, fmt.Sprintf("peekNotifications=%d", n))
	}
	if s.hasPendingNotifications() {
		reasons = append(reasons, "hasPendingNotifications")
	}
	if s.notificationWorkPending() {
		reasons = append(reasons, "hasPendingDelegateAttentionArmRetry")
	}
	if s.notificationWorkPending() {
		reasons = append(reasons, "hasPendingStableDelegateAttention")
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		running, finalizing, driving := sub.running, sub.finalizing, sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if running || finalizing || driving {
			reasons = append(reasons, fmt.Sprintf("subagent active(running=%v finalizing=%v driving=%v)", running, finalizing, driving))
		}
		// A delegate whose finish append failed holds drain work the run flags do
		// not show; name it so a failure here says why the tree was outstanding.
		if s.childDelegateRecoveryHeld(child) {
			reasons = append(reasons, "child "+child.id+": delegate finalization recovery held")
		}
		if child != nil {
			for _, r := range outstandingWorkReasons(child) {
				reasons = append(reasons, "child "+child.id+": "+r)
			}
		}
	}
	return reasons
}

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
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobType, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, ParentDelegateID: jm.parentDelegateID, StartedAt: &started},
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
		res, err = sess.DrainJobTree(context.Background(), drainWakeForTest(sess))
		close(done)
	}()

	select {
	case <-done:
	// TRIPWIRE: awaits done, DrainJobTree's own return signal; with no jobs in
	// flight it should return at once. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("DrainJobTree blocked with no jobs in flight; expected immediate return")
	}
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if res != "" {
		t.Fatalf("expected empty result when no drain turn ran, got %q", res)
	}
}

// TestDrainJobTreeRescansWhenAWakeLandsMidPass pins the drain's quiescence
// verdict against the completion hand-off a single pass can straddle.
//
// treeHasOutstandingWork reads eight independent signals in sequence, so it is
// not a snapshot. A delegate completion hands its work UP the tree in two steps
// — deliverDelegatePacket arms the coordinator's root attention (raising this
// wake), then runSubagent clears the subagent's finalizing flag — while a pass
// reads the attention EARLY and the flag LATE. A pass that straddles the two
// steps sees both false even though at no instant were they both false, and the
// drain then returns "" and lets Close() SIGKILL a delegate notification that
// was already armed and waiting. That is the PRI-2441 B1 flake: a one-shot
// `evener run` printing "waiting on delegate" instead of the coordinator's real
// final answer.
//
// The wake is the one signal that survives the straddle, because every producer
// of drain-relevant work raises it. So the pass consumes the wake edge BEFORE it
// reads any state and re-checks it before concluding quiescence: a wake raised
// in between means the tree moved under the scan and the verdict is stale. Here
// the injected kick plays the concurrent completion, raising the wake mid-pass
// while leaving every signal the pass reads false — exactly the state a
// straddling pass observes.
func TestDrainJobTreeRescansWhenAWakeLandsMidPass(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	passes := 0
	kick := func(context.Context) error {
		passes++
		if passes == 1 {
			sess.notify()
		}
		return nil
	}
	process := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Error("drain ran a notification turn with nothing queued")
		return "", nil
	}

	// recheck never fires: only the mid-pass wake may cause the second pass, so a
	// lost wake cannot be masked by the periodic re-check.
	res, err := sess.drainJobTreeWith(context.Background(), drainWakeForTest(sess), make(chan time.Time), kick, process)
	if err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if res != "" {
		t.Fatalf("drain result = %q, want empty (no drain turn ran)", res)
	}
	if passes != 2 {
		t.Fatalf("drain passes = %d, want 2: a wake raised while a pass was deciding quiescence must invalidate that pass's verdict and force a re-scan", passes)
	}
}

// A producer obligation keeps drain open before receiver custody.
func TestTreeHasOutstandingWorkCountsPendingDelegateDeliveries(t *testing.T) {
	// Serial: seedPendingDeliverySource captures process-wide diagnostics.
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withClock(clk))
	sess.stopLaneResidueSweepTimer()

	seedPendingDeliverySource(t, sess, clk)

	outstanding, err := sess.treeHasOutstandingWork()
	if err != nil {
		t.Fatalf("treeHasOutstandingWork: %v", err)
	}
	if !outstanding {
		t.Fatal("treeHasOutstandingWork = false with a delegate delivery still pending; a deferred completion must keep the drain open")
	}

	live, err := sess.subtreeHasLiveComponent()
	if err != nil {
		t.Fatalf("subtreeHasLiveComponent: %v", err)
	}
	if !live {
		t.Fatal("subtreeHasLiveComponent = false with a delegate delivery still pending; the stall watchdog must not treat this as a genuine wedge, since kickDriveTree flushes it every pass")
	}
}

// TestDrainJobTreeWaitsWhileSourceDeliveryPending proves the drain
// loop itself, not just the read function, respects a pending delegate
// delivery: with a kick that deliberately never flushes anything (isolating
// treeHasOutstandingWork's verdict from kickDriveTree's own behavior, which
// TestKickDriveTreeReplaysPendingDelegateDeliveries covers separately),
// drainJobTreeWith must block rather than ever declaring quiescence, exactly
// as TestDrainJobTreeReturnsOnContextCancel already pins for a stuck managed
// job. recheck is left permanently unfired, so the only way this run ends is
// wrongly quiescing (an immediate nil-error return, independent of ctx) or
// correctly blocking in waitDrainWake until cancel() reaches it.
func TestDrainJobTreeWaitsWhileSourceDeliveryPending(t *testing.T) {
	// Serial: seedPendingDeliverySource captures process-wide diagnostics.
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withClock(clk))
	sess.stopLaneResidueSweepTimer()

	seedPendingDeliverySource(t, sess, clk)

	kick := func(context.Context) error { return nil } // deliberately never flushes
	process := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Error("drain ran a notification turn with nothing queued")
		return "", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), make(chan time.Time), kick, process)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("drainJobTreeWith error = %v, want context.Canceled: a pending delegate delivery must keep the drain open, not let it quiesce", err)
		}
	// TRIPWIRE: awaits done, drainJobTreeWith's own return signal, after
	// cancel(); a correctly blocked drain observes ctx.Err() and returns as
	// soon as it reaches waitDrainWake. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drainJobTreeWith did not return after context cancellation")
	}
}

// The drain replays source obligations rather than popping receiver placeholders.
func TestKickDriveTreeReplaysPendingDelegateDeliveries(t *testing.T) {
	// Serial: seedPendingDeliverySource captures process-wide diagnostics.
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withClock(clk))
	sess.stopLaneResidueSweepTimer()

	seedPendingDeliverySource(t, sess, clk)

	err := sess.kickDriveTree(context.Background())
	if err != nil && !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("kickDriveTree: %v", err)
	}
	if sess.hasPendingDelegateDeliveries() {
		t.Fatal("kickDriveTree did not flush the pending delegate delivery")
	}
	// Exercise the real missing-transcript retry before testing its teardown.
	if got := clk.PendingCallbacks(); got != 1 {
		t.Fatalf("armed wake retries=%d, want 1", got)
	}
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	if got := clk.PendingCallbacks(); got != 1 {
		t.Fatalf("rearmed wake retries=%d, want 1", got)
	}
	t.Log("real missing-transcript wake retry fired and rearmed before cleanup")
}

// Busy receivers may take custody, but only their own turn constructs a request.
func TestKickDriveTreeAcceptsBusyChildDeliveryWithoutDrivingIt(t *testing.T) {
	// Serial: seedPendingDeliverySource captures process-wide diagnostics.
	clk := agenttest.NewFakeClock()
	root := newSession(t, withClock(clk))
	root.stopLaneResidueSweepTimer()
	child := newSession(t, withClock(clk))
	child.stopLaneResidueSweepTimer()
	sub := &subagent{id: child.ID(), sess: child, running: true}
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = sub
	root.subagents.mu.Unlock()
	id := seedPendingDeliverySource(t, child, clk)
	if err := root.kickDriveTree(context.Background()); err != nil {
		t.Fatal(err)
	}
	if child.hasPendingDelegateDeliveries() {
		t.Fatal("busy child did not take custody")
	}
	ids, err := child.pendingNotificationIDs()
	if err != nil || len(ids) != 1 || ids[0] != delegateAttentionID(id) {
		t.Fatalf("busy child custody=%v err=%v", ids, err)
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if !running {
		t.Fatal("ancestor changed the busy child's execution state")
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
				_, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("DrainJobTree error = %v, want context.Canceled", err)
				}
			// TRIPWIRE: awaits done, DrainJobTree's own return signal, after
			// cancel(); it should observe ctx.Err() and return immediately.
			// 30s only fires on a genuine hang.
			case <-time.After(30 * time.Second):
				t.Fatal("DrainJobTree did not return after context cancellation; hang-safety backstop failed")
			}
		})
	}
}

func TestCancelledManagedShellDrainStopsOnSessionClose(t *testing.T) {
	t.Parallel()
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
		_, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), recheck, func(ctx context.Context) error {
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
	// TRIPWIRE: awaits kickEntered, the drain loop's own per-iteration signal;
	// the goroutine above sends on it synchronously as soon as it enters the
	// kick. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
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
	// TRIPWIRE: awaits tickConsumed, closed once the drain loop reads the
	// controlled recheck tick above. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("managed-job drain did not consume the controlled recheck")
	}
	// Resource rechecks do not pump notifications. A producer hint, not the
	// tick above, owns a second kick.
	sess.notify()
	select {
	case kick := <-kickEntered:
		if kick != 2 {
			t.Fatalf("second drain kick = %d, want 2", kick)
		}
	// TRIPWIRE: awaits kickEntered again for the second iteration. 30s only
	// fires on a genuine hang.
	case <-time.After(30 * time.Second):
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
	// TRIPWIRE: awaits drainDone, the drain goroutine's own exit signal, after
	// cancel(); it should observe ctx.Err() and return immediately. 30s only
	// fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("DrainJobTree did not return after caller cancellation")
	}
	if signals := executor.signals.Load(); signals != 0 {
		t.Fatalf("caller cancellation stopped the managed shell: signals=%d", signals)
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("managed shell after drain cancellation = %d, %v, want still live", n, err)
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

// TestOutstandingJobCountCoversPendingNotifyWindow verifies a terminal shell
// remains outstanding between its durable pending marker and in-memory enqueue.
func TestOutstandingJobCountCoversPendingNotifyWindow(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	jm := sess.jobManager

	// Reproduce the post-delete/pre-enqueue state durably: the shell finished
	// and its notification-pending event is recorded, but it is not in the running
	// map and not yet in the in-memory notification queue.
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	code := 0
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "shell-window", Type: jobstore.JobShell, OwnerSessionID: sess.ID(), VisibleToSession: sess.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "shell-window", Status: jobstore.StatusCompleted, Reason: "exit_zero", ExitCode: &code, EndedAt: &ended, TerminalGen: "gen-window"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "shell-window", TerminalGen: "gen-window"},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}

	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
	}
	if n, err := jm.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("a pending shell absent from the running map must still count as outstanding, got %d (err %v)", n, err)
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
		{name: "shell", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sess := newSession(t)
			jm := sess.jobManager
			started := frozenTestTime.Add(-time.Second)
			ended := frozenTestTime
			for _, ev := range []jobstore.Event{
				{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_fwd", Type: tt.typ, OwnerSessionID: "CHILD-SESSION", VisibleToSession: sess.ID(), StartedAt: &started},
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

// TestRematerializeOwnedDrainJobPendings verifies custody recovery uses
// the same ownership and managed-job eligibility as durable drain accounting.
// Reverting rematerialization to its former delegate-only filter would strand
// the owned shell and fail this test.
func TestRematerializeOwnedDrainJobPendings(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	jm := sess.jobManager
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
	assertNotificationPending(t, sess, []string{"shell:shell-owned:gen-shell-owned"})
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("rematerialization queued %d legacy notifications", got)
	}
	requireNotificationState(t, jm, "shell-owned", jobstore.NotifyDelivered)
	requireNotificationState(t, jm, "shell-forwarded", jobstore.NotifyPending)
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
	t.Parallel()
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "shell", jobID: "shell-root", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]func(llm.Request) llm.Response, 6)
			for i := range steps {
				steps[i] = func(llm.Request) llm.Response { return finalResponse("ack") }
			}
			sess := newSession(t, withSteps(steps...), withConfig(SessionConfig{}))
			jm := sess.jobManager
			seedOwnedDurablePending(t, jm, tt.jobID, tt.typ)
			if p := sess.peekNotifications(); p != 0 {
				t.Fatalf("precondition: expected empty in-memory queue, got %d", p)
			}

			// TRIPWIRE: scripted in-process adapter steps plus fully in-memory
			// job-manager fixtures, no real I/O; only fires on a genuine hang
			// (a wedged drain, the regression this test guards against).
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.DrainJobTree(ctx, drainWakeForTest(sess)); err != nil {
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
// regression for an owned NotifyPending shell whose canonical receiver record
// survived a crash before source acknowledgement. Drain settles the source
// without duplicate custody or presentation. Notification-like text and a watch
// identity for the same job do not prove shell custody.
func TestDrainSettlesAlreadyInjectedDurablePending(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "shell", jobID: "shell-injected", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adapter := newMessagingAdapter()
			sess := newMessagingRoot(t, adapter, nil)
			jm := sess.jobManager
			seedOwnedDurablePending(t, jm, tt.jobID, tt.typ)

			injected := `<job-notification job_id="` + tt.jobID + `" status="completed"></job-notification>`
			sess.appendTurn(schema.TurnSteering, llm.User(injected))
			recs, err := jm.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			id := "shell:" + tt.jobID + ":" + recs[tt.jobID].TerminalGen
			// Seed the exact source payload at its custody-before-ack boundary.
			// The shortened notification-like text above is not that payload and
			// must not become a conflicting message under the real shell ID.
			canonical := sess.formatJobNotificationReminder([]deliverableJobNotification{{
				notification: jobNotificationFromRecord(recs[tt.jobID]),
				terminalGen:  recs[tt.jobID].TerminalGen,
			}})
			if accepted, err := sess.notificationSourceAccepted(id); err != nil || accepted {
				t.Fatalf("notification-like text established custody: %t, %v", accepted, err)
			}
			if err := sess.enqueueNotification(notification{ID: "watch:" + tt.jobID, Message: llm.UserMachinery(injected)}); err != nil {
				t.Fatal(err)
			}
			if accepted, err := sess.notificationSourceAccepted(id); err != nil || accepted {
				t.Fatalf("watch identity established shell custody: %t, %v", accepted, err)
			}
			if err := sess.enqueueNotification(notification{ID: id, Message: llm.UserMachinery(canonical)}); err != nil {
				t.Fatal(err)
			}
			if accepted, err := sess.notificationSourceAccepted(id); err != nil || !accepted {
				t.Fatalf("canonical source not accepted: %t, %v", accepted, err)
			}

			// TRIPWIRE: scripted in-process provider with temporary journals.
			// This bounds a wedged drain, not a production delivery deadline.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.DrainJobTree(ctx, drainWakeForTest(sess)); err != nil {
				t.Fatalf("DrainJobTree wedged on an already-injected durable pending: %v", err)
			}
			requireNotificationState(t, jm, tt.jobID, jobstore.NotifyDelivered)
			count := 0
			for _, entry := range notificationRawEntries(t, transcriptPath(sess.stateDir, sess.ID())) {
				if entry.Turn.AttentionID == id {
					count++
				}
			}
			if count != 1 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("receiver records=%d provider requests=%d, want 1 each", count, len(adapter.requestsFor("root")))
			}
			drainRequestNotice(t, adapter.requestsFor("root")[0], map[string]string{"job_id": tt.jobID, "job_type": "shell", "status": "completed"})
			requireDrainShellObserved(t, sess, tt.jobID)
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
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "shell", jobID: "shell-child", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const marker = "durable-shell-child-731a"
			adapter := newMessagingAdapter(marker)
			adapter.script("root",
				func(llm.Request) llm.Response { return delegateToolResponse("create-child", marker) },
				func(llm.Request) llm.Response { return finalResponse("root-ready") },
			)
			adapter.script(marker, func(llm.Request) llm.Response { return finalResponse("child-ready") })
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.MaxSubagentDepth = 2 })
			if _, err := root.ProcessInput(context.Background(), "create child", nil); err != nil {
				t.Fatal(err)
			}
			child := waitForSoleChildIdle(t, root)
			if child.owningDelegateID == "" || child.delegateController != root.delegateController {
				t.Fatal("fixture lacks stable delegate ownership")
			}
			if _, err := root.DrainJobTree(context.Background(), drainWakeForTest(root)); err != nil {
				t.Fatal(err)
			}
			seedOwnedDurablePending(t, child.jobManager, tt.jobID, tt.typ)
			id := "shell:" + tt.jobID + ":gen-" + tt.jobID
			assertNotificationPending(t, child, []string{})
			before := len(adapter.requestsFor(marker))

			// TRIPWIRE: scripted in-process provider and temporary journals.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := root.DrainJobTree(ctx, drainWakeForTest(root)); err != nil {
				t.Fatalf("DrainJobTree wedged on a child's durable-only pending: %v", err)
			}
			requireNotificationState(t, child.jobManager, tt.jobID, jobstore.NotifyDelivered)
			requireOwnedDrainAttentionObservedOnce(t, child, id)
			if got := len(adapter.requestsFor(marker)); got != before+1 {
				t.Fatalf("child requests = %d, want %d", got, before+1)
			}
			drainRequestNotice(t, adapter.requestsFor(marker)[before], map[string]string{"job_id": tt.jobID, "job_type": "shell", "status": "completed"})
			if requestsContain(adapter.requestsFor("root"), `job_id="`+tt.jobID+`"`) {
				t.Fatal("root provider request presented child-owned shell notification")
			}
			for _, entry := range notificationRawEntries(t, root.TranscriptPath()) {
				if entry.Turn.AttentionID == id {
					t.Fatal("root accepted child-owned shell notification")
				}
				for _, observed := range entry.Turn.ObservedNotificationIDs {
					if observed == id {
						t.Fatal("root observed child-owned shell notification")
					}
				}
			}
			if err := child.rematerializeDurablePendings(); err != nil {
				t.Fatal(err)
			}
			if _, err := root.DrainJobTree(ctx, drainWakeForTest(root)); err != nil {
				t.Fatal(err)
			}
			requireOwnedDrainAttentionObservedOnce(t, child, id)
			if got := len(adapter.requestsFor(marker)); got != before+1 {
				t.Fatalf("replay issued a duplicate child request: %d", got)
			}
		})
	}
}

func TestDrainJobTreeBatchesQueuedShellNotifications(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("batch handled") },
		},
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{}))
	jm := sess.jobManager
	seedOwnedDurablePending(t, jm, "shell-batch-one", jobstore.JobShell)
	seedOwnedDurablePending(t, jm, "shell-batch-two", jobstore.JobShell)

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a
	// genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
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
	for _, id := range []string{"shell-batch-one", "shell-batch-two"} {
		drainRequestNotice(t, requests[0], map[string]string{"job_id": id, "job_type": "shell", "status": "completed"})
		requireDrainShellObserved(t, sess, id)
	}
	requireNotificationState(t, jm, "shell-batch-one", jobstore.NotifyDelivered)
	requireNotificationState(t, jm, "shell-batch-two", jobstore.NotifyDelivered)
}

func TestDrainJobTreeConsumesRootDelegateAttentionBeforeCompletion(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_drain/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_drain">drain me</delegate-notification>`
	)
	requestSawAttention := false
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(func(req llm.Request) llm.Response {
			requestSawAttention = requestContainsText(req, content)
			return toolCallResponse(communicateCall("root-attention-drain", "root attention drained"))
		}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm root attention after source settlement: %v", err)
	}

	result, err := root.drainJobTreeWith(context.Background(), drainWakeForTest(root), make(chan time.Time), root.kickDriveTree, root.ProcessInputKind)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if result != "root attention drained" {
		t.Fatalf("DrainJobTree result = %q, want root attention drained", result)
	}
	if !requestSawAttention {
		t.Fatal("DrainJobTree completed without delivering durable root attention")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("DrainJobTree left root attention pending: %#v", pending)
	}
	if outstanding, err := root.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("root work after DrainJobTree = %v, err %v; want quiescent", outstanding, err)
	}
	if root.sessionWorkPending() {
		t.Fatal("root work-pending remained set after DrainJobTree consumed attention")
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
	stateDir := t.TempDir()
	sess := newSession(t, withDir(stateDir), withSteps(steps...), withConfig(SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
	}))

	// Background a delegate; it runs to completion in its own goroutine and
	// enqueues a completion notification on this session.
	res := sess.createDelegate(context.Background(), delegateArgs{
		Task: "do a small unit of work and communicate done",
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	// TRIPWIRE: scripted in-process adapter steps for both the root and the
	// delegate child, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.DrainJobTree(ctx, drainWakeForTest(sess)); err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}

	// After draining, the stable delegate run is settled and nothing is pending.
	aggregate := delegateAggregateSnapshot(t, sess.delegateController, res.DelegateID)
	if aggregate.CurrentRunOpen {
		t.Fatalf("delegate %s still has an open run after drain: %#v", res.DelegateID, aggregate)
	}
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("tree remains outstanding after delegate drain: outstanding=%v err=%v reasons=%v",
			outstanding, err, outstandingWorkReasons(sess))
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("expected 0 pending notifications after drain, got %d", p)
	}
}

func TestDrainJobTreeWaitsForForegroundPromotedShell(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("foreground shell handled") },
		},
	}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		clock: clk,
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
	// TRIPWIRE: awaits resultCh, runShell's own return signal, after the fake
	// clock above has already been advanced past the block timeout; the call
	// should return immediately. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
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
	if !live.rec.Background {
		t.Fatal("foreground-promoted shell live record has Background=false; promotion must record that the command now runs in the background")
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("outstanding drain jobs = %d (err %v), want 1", n, err)
	}

	releaseShell()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a
	// genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
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

// TestDrainDoesNotExcuseFiredReadinessWatch verifies that an output-match watch
// that already fired is not a live excuse for the still-running background job.
// The watch remains armed for later output, but only an unfired condition can
// tell a one-shot drain that the model explicitly awaits this job.
func TestDrainDoesNotExcuseFiredReadinessWatch(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("readiness acknowledged") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{TurnEndsProcess: true}))
	shell := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"printf READY\\n; sleep 5","mode":"background"}`),
	})
	if shell.IsError {
		t.Fatalf("shell: %s", shell.Output)
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shell), &started); err != nil || started.JobID == "" {
		t.Fatalf("shell result: %v %s", err, shell.Output)
	}
	t.Cleanup(func() {
		_, _ = sess.jobManager.stop(started.JobID)
		waitForShellDone(t, sess.jobManager, started.JobID)
	})
	// TRIPWIRE: the shell emits READY immediately; 5s only fires if the real
	// background process/output path wedges.
	waitForCondition(t, 5*time.Second, "READY output", func() bool {
		sess.jobManager.mu.Lock()
		defer sess.jobManager.mu.Unlock()
		run := sess.jobManager.running[started.JobID]
		if run == nil || run.output == nil {
			return false
		}
		data, _, _, err := run.output.Tail(maxJobOutputRetentionBytes)
		return err == nil && strings.Contains(string(data), "READY")
	})
	watch := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "watch", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","source":"` + started.JobID + `","output_match":"READY"}`),
	})
	if watch.IsError {
		t.Fatalf("watch: %s", watch.Output)
	}
	sess.jobManager.mu.Lock()
	var fired, live bool
	for key, cfg := range sess.jobManager.watches {
		if key.Target == started.JobID {
			live = true
			fired = cfg.deliveries > 0 && cfg.conditionFires > 0
		}
	}
	sess.jobManager.mu.Unlock()
	if !fired || !live {
		t.Fatalf("attach lifecycle did not fire and retain watch: fired=%v live=%v", fired, live)
	}
	// TRIPWIRE: notification delivery is synchronous; 5s only fires if the
	// real session turn wedges.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("deliver fired readiness notification: %v", err)
	}
	ids, sole, err := sess.undisposedBackgroundDrainJobs()
	if err != nil {
		t.Fatal(err)
	}
	if !sole || len(ids) != 1 || ids[0] != started.JobID {
		t.Fatalf("fired readiness watch still excuses job: ids=%v sole=%v", ids, sole)
	}
}
