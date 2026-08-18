package agent

import (
	"context"
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
	if s.hasPendingRootDelegateAttention() {
		reasons = append(reasons, "hasPendingRootDelegateAttention")
	}
	if s.hasPendingDelegateAttentionArmRetry() {
		reasons = append(reasons, "hasPendingDelegateAttentionArmRetry")
	}
	if s.hasPendingStableDelegateAttention() {
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
// `serf run` printing "waiting on delegate" instead of the coordinator's real
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
	res, err := sess.drainJobTreeWith(context.Background(), make(chan time.Time), kick, process)
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

// TestTreeHasOutstandingWorkCountsPendingDelegateDeliveries pins a sibling gap
// to the straddle above, through a different channel entirely:
// pendingDelegateDeliveries.
//
// acceptDelegateDeliveryPlan defers a delivery into that queue instead of
// delivering it immediately whenever the receiving session is
// SessionProcessing at that instant -- e.g. a second, unrelated delegate's
// completion targeting this session while the drain loop's own notification
// turn is already running one delivery. The deferral calls notify() exactly
// once, but treeHasOutstandingWork never reads pendingDelegateDeliveries at
// all (unlike outstandingDrainJobCount, peekNotifications, and every attention
// signal it does read). So once that one-shot wake is consumed, nothing about
// this session's state says a completion is still owed, and the drain
// concludes quiescence with a real delivery sitting undelivered -- letting
// Close() SIGKILL it. Same PRI-2441 symptom, different signal the original fix
// didn't touch.
//
// This test seeds the queue exactly the way acceptDelegateDeliveryPlan does
// (append under delegateDeliveryMu) and asks the read function the drain loop
// actually calls, so it is independent of how the entry got there.
func TestTreeHasOutstandingWorkCountsPendingDelegateDeliveries(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	sess.delegateDeliveryMu.Lock()
	sess.pendingDelegateDeliveries = append(sess.pendingDelegateDeliveries, delegateDeliveryPlan{})
	sess.delegateDeliveryMu.Unlock()

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

// TestDrainJobTreeWaitsWhilePendingDelegateDeliveryUnflushed proves the drain
// loop itself, not just the read function, respects a pending delegate
// delivery: with a kick that deliberately never flushes anything (isolating
// treeHasOutstandingWork's verdict from kickDriveTree's own behavior, which
// TestKickDriveTreeFlushesPendingDelegateDeliveries covers separately),
// drainJobTreeWith must block rather than ever declaring quiescence, exactly
// as TestDrainJobTreeReturnsOnContextCancel already pins for a stuck managed
// job. recheck is left permanently unfired, so the only way this run ends is
// wrongly quiescing (an immediate nil-error return, independent of ctx) or
// correctly blocking in waitDrainWake until cancel() reaches it.
func TestDrainJobTreeWaitsWhilePendingDelegateDeliveryUnflushed(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	sess.delegateDeliveryMu.Lock()
	sess.pendingDelegateDeliveries = append(sess.pendingDelegateDeliveries, delegateDeliveryPlan{})
	sess.delegateDeliveryMu.Unlock()

	kick := func(context.Context) error { return nil } // deliberately never flushes
	process := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Error("drain ran a notification turn with nothing queued")
		return "", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.drainJobTreeWith(ctx, make(chan time.Time), kick, process)
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

// TestKickDriveTreeFlushesPendingDelegateDeliveries pins the other half of the
// fix: counting a pending delivery as outstanding work only stops the drain
// from a FALSE quiescence. Something still has to resolve it, or the drain
// just waits out its stall timeout instead of making progress. kickDriveTree
// runs at the top of every pass (before the outstanding check), alongside
// drivePendingStableDelegateAttention and rematerializeDurablePendings -- the
// same "drive whatever is waiting" role -- so it is where the flush belongs.
//
// The seeded entry is a placeholder with no live controller, not a delegate
// produced by the real accept/defer path, so its OWN delivery attempt reports
// errDelegateStaleLease (the same "something else already resolved this"
// outcome a genuinely superseded delivery would produce) -- deliverDelegatePacket
// checks plan.controller before anything else. flushPendingDelegateDeliveries
// pops the queue entry before attempting delivery, so that pop -- not the
// placeholder's delivery outcome -- is the fact this test pins: did
// kickDriveTree reach into the queue at all.
func TestKickDriveTreeFlushesPendingDelegateDeliveries(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	sess.delegateDeliveryMu.Lock()
	sess.pendingDelegateDeliveries = append(sess.pendingDelegateDeliveries, delegateDeliveryPlan{})
	sess.delegateDeliveryMu.Unlock()

	err := sess.kickDriveTree(context.Background())
	if err != nil && !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("kickDriveTree: %v", err)
	}
	if sess.hasPendingDelegateDeliveries() {
		t.Fatal("kickDriveTree did not flush the pending delegate delivery")
	}
}

// TestKickDriveTreeDoesNotFlushBusyChildsPendingDelegateDeliveries pins a
// hazard in TestKickDriveTreeFlushesPendingDelegateDeliveries's own fix:
// kickDriveTreeWith recurses into every live child (liveDirectSubagents
// filters only on sub.closed) and, before this test, called
// flushPendingDelegateDeliveries there unconditionally. flushPendingDelegateDeliveries
// has no s.state check -- it was safe at its four pre-existing call sites only
// because every one of them runs ON the owning session's own turn goroutine.
// The drain's recursive call runs on the ANCESTOR's drain goroutine instead, so
// flushing a child that is actively running/driving/finalizing can splice a
// delivered notification into that child's history at a point its own round
// loop did not choose -- e.g. between an in-flight assistant tool call and its
// tool result, breaking message alternation. -race cannot catch this: every
// lock is held correctly, the hazard is purely about WHEN the mutation lands,
// not whether it races.
//
// driveSubagentNotificationTurn (subagents.go) already refuses to touch a
// child where sub.running || sub.driving || sub.finalizing; this test holds
// kickDriveTree to the same gate for the flush specifically. The child's own
// delivery queue must still count as outstanding work (so the ancestor's drain
// correctly keeps waiting on it) even though the ancestor must not be the one
// to flush it — the child's own turn machinery flushes at its next natural
// boundary instead.
func TestKickDriveTreeDoesNotFlushBusyChildsPendingDelegateDeliveries(t *testing.T) {
	t.Parallel()
	root := newSession(t)
	child := newSession(t)
	sub := &subagent{id: child.ID(), sess: child, running: true}
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = sub
	root.subagents.mu.Unlock()

	child.delegateDeliveryMu.Lock()
	child.pendingDelegateDeliveries = append(child.pendingDelegateDeliveries, delegateDeliveryPlan{})
	child.delegateDeliveryMu.Unlock()

	if err := root.kickDriveTree(context.Background()); err != nil {
		t.Fatalf("kickDriveTree: %v", err)
	}
	if !child.hasPendingDelegateDeliveries() {
		t.Fatal("kickDriveTree flushed a running child's pending delegate delivery; a busy child's own turn owns that mutation, not the ancestor's drain")
	}

	outstanding, err := root.treeHasOutstandingWork()
	if err != nil {
		t.Fatalf("treeHasOutstandingWork: %v", err)
	}
	if !outstanding {
		t.Fatal("treeHasOutstandingWork = false with the running child's delegate delivery still pending; skipping the flush must not also drop it from the outstanding count")
	}

	// Once the child is no longer busy, its own turn boundary has passed and
	// the ancestor's kick may flush it like any other idle child.
	sub.mu.Lock()
	sub.running = false
	sub.mu.Unlock()
	if err := root.kickDriveTree(context.Background()); err != nil && !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("kickDriveTree: %v", err)
	}
	if child.hasPendingDelegateDeliveries() {
		t.Fatal("kickDriveTree did not flush the child's pending delegate delivery once it stopped running")
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
				_, err := sess.DrainJobTree(ctx)
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

// TestRematerializeOwnedDrainJobPendings verifies the queue reconstruction uses
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
	sess.pendingJobNotifsMu.Lock()
	got := append([]jobNotification(nil), sess.pendingJobNotifs...)
	sess.pendingJobNotifsMu.Unlock()
	if len(got) != 1 {
		t.Fatalf("rematerialized notifications = %+v, want one owned shell", got)
	}
	gotTypes := make(map[string]string, len(got))
	for _, notification := range got {
		gotTypes[notification.JobID] = notification.JobType
	}
	if gotTypes["shell-owned"] != "shell" {
		t.Fatalf("rematerialized job ids and types = %v, want shell-owned shell", gotTypes)
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

			// TRIPWIRE: scripted in-process adapter steps plus fully in-memory
			// job-manager fixtures, no real I/O; only fires on a genuine hang
			// (a wedged drain, the regression this test guards against).
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

			// TRIPWIRE: scripted in-process adapter steps plus fully in-memory
			// job-manager fixtures, no real I/O; only fires on a genuine hang
			// (a wedged drain, the regression this test guards against).
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

			// TRIPWIRE: scripted in-process adapter steps for both sessions,
			// no real I/O; only fires on a genuine hang (a wedged drain, the
			// regression this test guards against).
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

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a
	// genuine hang.
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

func TestDrainJobTreeConsumesRootDelegateAttentionBeforeCompletion(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_drain/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_drain">drain me</delegate-notification>`
	)
	requestSawAttention := false
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true}),
		withSteps(func(req llm.Request) llm.Response {
			requestSawAttention = requestContainsText(req, content)
			return toolCallResponse(communicateCall("root-attention-drain", "root attention drained"))
		}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm root attention after source settlement: %v", err)
	}

	result, err := root.drainJobTreeWith(context.Background(), make(chan time.Time), root.kickDriveTree, root.ProcessInputKind)
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
		NoProjectPrompts: true,
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
	if _, err := sess.DrainJobTree(ctx); err != nil {
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
	if live.rec.Background {
		t.Fatal("foreground-promoted shell live record has Background=true, want false")
	}
	if n, err := sess.jobManager.outstandingDrainJobCount(); err != nil || n != 1 {
		t.Fatalf("outstanding drain jobs = %d (err %v), want 1", n, err)
	}

	releaseShell()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a
	// genuine hang.
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
