package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// enqueueLeftoverNotification puts an in-memory notification for jobID on the
// session's own rail, mirroring what a job that finalized during the final
// round leaves behind when the model ends the turn anyway.
func enqueueLeftoverNotification(t *testing.T, sess *Session, jobID string) {
	t.Helper()
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load job store: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("job %s not in store", jobID)
	}
	sess.enqueueJobNotification(jobNotificationFromRecord(rec))
}

// TestTerminalDrainDiscardsLeftoverNotificationsWithoutAProviderCall is the
// #329 sanitize-git-repo regression. The model sent the terminal communicate
// (end_turn under TurnEndsProcess) with an undelivered job notification still
// pending. There is no model left to deliver to — the turn that ends the
// process is by definition the last turn — so the drain must discard the
// leftover (settling its durable record as consumed) and exit WITHOUT another
// provider call. The field failure made that call, got an empty response, and
// the retry path injected "please continue" instead of exiting.
func TestTerminalDrainDiscardsLeftoverNotificationsWithoutAProviderCall(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("the answer") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	sess.cfg.testOnly.beforeTerminalCommunicateAccept = func() {
		seedOwnedDurablePending(t, sess.jobManager, "job-leftover", jobstore.JobShell)
	}

	// TRIPWIRE: scripted in-process adapter plus in-memory job fixtures, no
	// real I/O; 30s only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.ProcessInput(ctx, "do the task", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if res != "the answer" {
		t.Fatalf("ProcessInput result = %q, want %q", res, "the answer")
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider calls before drain = %d, want 1", got)
	}
	// Reopen the durable ledger before drain entry, as a crash/restart in this
	// exact window would. Acceptance itself must have committed the pre-cut
	// disposition so restore cannot rematerialize it.
	reopened, err := jobstore.Open(filepath.Join(jobsDir(sess.cfg.StateDir, sess.ID()), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen durable job store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recs, err := reopened.Load()
	if err != nil {
		t.Fatalf("load reopened durable job store: %v", err)
	}
	if got := recs["job-leftover"].NotifyState; got != jobstore.NotifyConsumed {
		t.Fatalf("reopened pre-drain notification state = %s, want %s", got, jobstore.NotifyConsumed)
	}

	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != "" {
		t.Fatalf("drain result = %q; no drain turn may run after the terminal communicate for a leftover", drained)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider calls = %d, want 1: the terminal communicate must be the last provider call", got)
	}
	requireNotificationState(t, sess.jobManager, "job-leftover", jobstore.NotifyConsumed)
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("queued notifications after terminal drain = %d, want 0 (discarded)", p)
	}
}

// TestTerminalCutPreservesFreshCompletionBeforeDrainEntry is the temporal-cut
// regression: terminal communicate is accepted while no notification is
// pending, then a durable completion lands before DrainJobTree starts. That
// completion belongs after the cut and must still open one provider turn.
func TestTerminalCutPreservesFreshCompletionBeforeDrainEntry(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("the answer") },
		func(llm.Request) llm.Response { return finalResponse("fresh_result_sentinel_7c4e") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	if _, err := sess.ProcessInput(context.Background(), "do the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	seedOwnedDurablePending(t, sess.jobManager, "job-fresh", jobstore.JobShell)
	enqueueLeftoverNotification(t, sess, "job-fresh")

	drained, err := sess.DrainJobTree(context.Background())
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != "fresh_result_sentinel_7c4e" {
		t.Fatalf("drain result = %q, want fresh completion result", drained)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2: the post-cut completion must be delivered", got)
	}
	requireNotificationState(t, sess.jobManager, "job-fresh", jobstore.NotifyDelivered)
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("queued notifications after fresh completion delivery = %d, want 0", p)
	}
}

// TestTerminalCutDistinguishesSameJobNotificationGenerations pins the exact
// durable identity. A later queue entry that reuses a job ID but carries a new
// terminal generation is not part of the old generation's discard set.
func TestTerminalCutDistinguishesSameJobNotificationGenerations(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	const jobID = "job-same-id"
	seedOwnedDurablePending(t, sess.jobManager, jobID, jobstore.JobShell)
	enqueueLeftoverNotification(t, sess, jobID)

	cut, err := sess.captureTerminalNotificationCut()
	if err != nil {
		t.Fatalf("capture terminal cut: %v", err)
	}
	sess.enqueueJobNotification(jobNotification{
		JobID:       jobID,
		TerminalGen: "gen-after-cut",
		Status:      string(jobstore.StatusCompleted),
	})
	if err := sess.discardTerminalDrainLeftovers(cut); err != nil {
		t.Fatalf("discard terminal cut: %v", err)
	}
	requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyConsumed)

	sess.pendingJobNotifsMu.Lock()
	queued := append([]jobNotification(nil), sess.pendingJobNotifs...)
	sess.pendingJobNotifsMu.Unlock()
	if len(queued) != 1 || queued[0].JobID != jobID || queued[0].TerminalGen != "gen-after-cut" {
		t.Fatalf("post-cut same-job generation was discarded: queued = %+v", queued)
	}
}

// TestTerminalCutOrdersConcurrentFinalization forces both sides of the
// finalizer/acceptance ordering without sleeps. The durable pending and running
// map are the exact two production signals captureTerminalNotificationCut reads
// under jm.mu; the queue enqueue happens after the finalizer drops that lock,
// matching armFinalizedJob.
func TestTerminalCutOrdersConcurrentFinalization(t *testing.T) {
	t.Run("acceptance lock wins", func(t *testing.T) {
		sess := newSession(t)
		const jobID = "job-cut-first"
		seedOwnedDurablePending(t, sess.jobManager, jobID, jobstore.JobShell)
		sess.jobManager.mu.Lock()
		sess.jobManager.running[jobID] = &runningJob{}
		sess.jobManager.mu.Unlock()

		cutLocked := make(chan struct{})
		releaseCut := make(chan struct{})
		var cutHookOnce sync.Once
		sess.cfg.testOnly.terminalCutAfterManagerLock = func() {
			cutHookOnce.Do(func() {
				close(cutLocked)
				<-releaseCut
			})
		}
		cutDone := make(chan terminalNotificationCut, 1)
		cutErr := make(chan error, 1)
		go func() {
			cut, err := sess.captureTerminalNotificationCut()
			if err != nil {
				cutErr <- err
				return
			}
			cutDone <- cut
		}()
		<-cutLocked

		finalizerStarted := make(chan struct{})
		finalizerDone := make(chan struct{})
		go func() {
			close(finalizerStarted)
			sess.jobManager.mu.Lock()
			delete(sess.jobManager.running, jobID)
			sess.jobManager.mu.Unlock()
			sess.enqueueJobNotification(jobNotification{JobID: jobID, TerminalGen: "gen-" + jobID})
			close(finalizerDone)
		}()
		<-finalizerStarted
		close(releaseCut)

		var cut terminalNotificationCut
		select {
		case err := <-cutErr:
			t.Fatalf("capture terminal cut: %v", err)
		case cut = <-cutDone:
		}
		<-finalizerDone
		if _, discarded := cut.durable[terminalIdentity(jobID, "gen-"+jobID)]; discarded {
			t.Fatal("job still running at acceptance was included in the durable discard set")
		}
		if err := sess.discardTerminalDrainLeftovers(cut); err != nil {
			t.Fatalf("discard terminal cut: %v", err)
		}
		requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyPending)
		if got := sess.peekNotifications(); got != 1 {
			t.Fatalf("fresh finalizer queue after acceptance = %d, want 1", got)
		}
	})

	t.Run("finalizer lock wins", func(t *testing.T) {
		sess := newSession(t)
		const jobID = "job-finalizer-first"
		seedOwnedDurablePending(t, sess.jobManager, jobID, jobstore.JobShell)
		sess.jobManager.mu.Lock()
		sess.jobManager.running[jobID] = &runningJob{}
		sess.jobManager.mu.Unlock()

		finalizerLocked := make(chan struct{})
		releaseFinalizer := make(chan struct{})
		finalizerDone := make(chan struct{})
		go func() {
			sess.jobManager.mu.Lock()
			close(finalizerLocked)
			<-releaseFinalizer
			delete(sess.jobManager.running, jobID)
			sess.jobManager.mu.Unlock()
			sess.enqueueJobNotification(jobNotification{JobID: jobID, TerminalGen: "gen-" + jobID})
			close(finalizerDone)
		}()
		<-finalizerLocked

		captureStarted := make(chan struct{})
		cutDone := make(chan terminalNotificationCut, 1)
		cutErr := make(chan error, 1)
		go func() {
			close(captureStarted)
			cut, err := sess.captureTerminalNotificationCut()
			if err != nil {
				cutErr <- err
				return
			}
			cutDone <- cut
		}()
		<-captureStarted
		close(releaseFinalizer)
		<-finalizerDone

		var cut terminalNotificationCut
		select {
		case err := <-cutErr:
			t.Fatalf("capture terminal cut: %v", err)
		case cut = <-cutDone:
		}
		if _, discarded := cut.durable[terminalIdentity(jobID, "gen-"+jobID)]; !discarded {
			t.Fatal("job finalized before acceptance was absent from the durable discard set")
		}
		if err := sess.discardTerminalDrainLeftovers(cut); err != nil {
			t.Fatalf("discard terminal cut: %v", err)
		}
		requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyConsumed)
		if got := sess.peekNotifications(); got != 0 {
			t.Fatalf("pre-cut finalizer queue after discard = %d, want 0", got)
		}
	})
}

// seedWatchSendResidue leaves the job manager in the wedged-settlement state
// the #329 hang trials showed around delegate-lane (dlg_*) watches: a stable
// watch settlement claim that nothing in this process will ever finish. It
// keeps hasPendingWatchSends true, which counts as BOTH outstanding work
// (treeHasOutstandingWork) and a live/deliverable component
// (subtreeHasLiveComponent) — so the drain neither quiesces nor ever reaches
// the stall watchdog's verdict.
func seedWatchSendResidue(t *testing.T, sess *Session) {
	t.Helper()
	jm := sess.jobManager
	jm.mu.Lock()
	jm.stableWatchSettlementRetrying = true
	jm.mu.Unlock()

	outstanding, err := sess.treeHasOutstandingWork()
	if err != nil || !outstanding {
		t.Fatalf("precondition: residue must count as outstanding work, got %v err=%v", outstanding, err)
	}
	live, err := sess.subtreeHasLiveComponent()
	if err != nil || !live {
		t.Fatalf("precondition: residue must suppress the stall watchdog, got live=%v err=%v", live, err)
	}
	stalled, err := sess.drainSubtreeIsStalled()
	if err != nil || stalled {
		t.Fatalf("precondition: residue must NOT read as a genuine stall, got stalled=%v err=%v", stalled, err)
	}
}

// TestTerminalDrainExitsDespiteWatchSendResidue is the #329 dead-hang
// regression (train-fasttext, count-dataset-tokens, filter-js-from-html): the
// model ended the process-ending turn, no live work remains anywhere, but
// watch-send residue keeps the drain outstanding AND keeps the stall watchdog
// suppressed — a livelock that previously held the process open until the
// harness killed it. Once the terminal communicate is accepted, residue with
// no live work behind it must be abandoned so the process can exit.
func TestTerminalDrainExitsDespiteWatchSendResidue(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("all done") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	// TRIPWIRE: scripted adapter, in-memory residue, no real I/O. The fixed
	// drain returns on its first pass; 10s only fires on the #329 livelock.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	seedWatchSendResidue(t, sess)

	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree must exit cleanly past terminal residue, got: %v", err)
	}
	if drained != "" {
		t.Fatalf("drain result = %q, want empty (no drain turn ran)", drained)
	}
	if ctx.Err() != nil {
		t.Fatal("drain only returned because the context expired: this is the #329 hang")
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

// TestPostTerminalNotificationTurnEmptyResponseFinishesIdle is the other half
// of the #329 sanitize-git-repo regression. A notification turn that
// legitimately runs after the terminal communicate (a live job finished during
// the drain) can meet a model that has nothing left to say — an empty
// response. The empty-response retry path must not inject "please continue"
// and resurrect a run the model already declared over: the turn finishes idle
// on the first empty response, with no retry provider call.
func TestPostTerminalNotificationTurnEmptyResponseFinishesIdle(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("the answer") },
		// The post-terminal notification turn: truly empty (no text, no tool
		// calls, no phase metadata) — the model glitch shape the retry budget
		// exists for, which after a terminal communicate must finish idle.
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
		},
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	// TRIPWIRE: scripted in-process adapter, no real I/O; 30s only fires on a
	// genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// A fresh completion arriving AFTER the terminal communicate (live-work
	// shape): durable pending plus its queued notification.
	seedOwnedDurablePending(t, sess.jobManager, "job-late", jobstore.JobShell)
	enqueueLeftoverNotification(t, sess, "job-late")

	out, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if out != "" {
		t.Fatalf("notification turn result = %q, want empty", out)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2: an empty post-terminal response must finish idle, not retry", got)
	}
}

// TestNormalDrainStillWaitsOnWatchSendResidue pins the #329 design boundary:
// WITHOUT a terminal communicate (the turn ended some other way), residue is
// treated exactly as before — the drain keeps waiting for the machinery to
// convert it, and only the caller's context bounds the wait. The discard rule
// is gated on the model having explicitly ended the process-ending turn.
func TestNormalDrainStillWaitsOnWatchSendResidue(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	seedWatchSendResidue(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	recheck := make(chan time.Time)
	firstKick := make(chan struct{})
	secondKick := make(chan struct{})
	releaseKick := make(chan struct{})
	var kickCount atomic.Int32
	kick := func(context.Context) error {
		switch kickCount.Add(1) {
		case 1:
			close(firstKick)
		case 2:
			close(secondKick)
			<-releaseKick
		}
		return nil
	}
	type drainResult struct {
		out string
		err error
	}
	done := make(chan drainResult, 1)
	go func() {
		out, err := sess.drainJobTreeWith(ctx, recheck, kick, sess.ProcessInputKind)
		done <- drainResult{out: out, err: err}
	}()

	<-firstKick
	// An unbuffered recheck send completes only when waitDrainWake is actually
	// parked on the drain's production select. The second kick proves the drain
	// woke and began another pass without returning.
	recheck <- frozenTestTime
	<-secondKick
	select {
	case got := <-done:
		t.Fatalf("normal drain returned after a real park while residue remained: %+v", got)
	default:
	}

	// Release every test-owned waiter, then await deterministic cleanup.
	cancel()
	close(releaseKick)
	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("drain error after cancellation = %v, want context canceled", got.err)
	}
	if got.out != "" {
		t.Fatalf("drain result after cancellation = %q, want empty", got.out)
	}
}
