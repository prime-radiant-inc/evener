package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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

// awaitQueuedJobNotification spins until a job notification is queued on the
// session's own rail, which armFinalizedJob does only after the job has left
// the running map with its NotifyPending record durable. It may run on the
// goroutine a scripted provider step is called from, so a miss is reported
// with t.Error and false, never t.Fatal.
func awaitQueuedJobNotification(t *testing.T, sess *Session) bool {
	t.Helper()
	// TRIPWIRE: the enqueue is one goroutine hop and a few store appends past
	// the shell's release; 30s only fires if finalization never lands.
	deadline := time.Now().Add(30 * time.Second)
	for sess.peekNotifications() == 0 {
		if time.Now().After(deadline) {
			t.Error("completion never queued on the rail")
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

// requireNeverConsumed fails if the durable ledger holds a consumed
// disposition for jobID. The folded record cannot show this on its own: a
// delivered mark wins over consumed in the fold, so only the raw events prove
// the completion was never written off.
func requireNeverConsumed(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	evs, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load job events: %v", err)
	}
	for _, ev := range evs {
		if ev.JobID == jobID && ev.Kind == jobstore.EventJobNotificationConsumed {
			t.Fatalf("job %s notification was recorded consumed: %+v", jobID, ev)
		}
	}
}

var notificationJobIDPattern = regexp.MustCompile(`job_id="([^"]+)"`)

// lastMessageText returns the text of the request's final message: for a
// notification turn, the injected reminder carrying the <job-notification>
// blocks delivered by that turn.
func lastMessageText(req llm.Request) string {
	return req.Messages[len(req.Messages)-1].Text()
}

// TestOneShotDrainDeliversCompletionThatLandsDuringTheFinalAnswer is the #865
// regression. The model's final-answer request is built while a background
// job is still running; the job finalizes before the reply arrives, so the
// answer was written without it. That completion must reach the model on one
// more notification turn, whose reply is the run's answer, and the ledger must
// show it delivered, never consumed.
func TestOneShotDrainDeliversCompletionThatLandsDuringTheFinalAnswer(t *testing.T) {
	t.Parallel()
	const firstAnswer = "first answer, written before the build finished"
	const finalAnswer = "FINAL-ANSWER: the build passed"
	var sess *Session
	var releaseShell func()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The final-answer request is built; finish the job before the reply
			// so the completion lands inside the answer's generation window.
			releaseShell()
			awaitQueuedJobNotification(t, sess)
			return finalResponse(firstAnswer)
		},
		func(llm.Request) llm.Response { return finalResponse(finalAnswer) },
	}}
	sess = newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	var jobID string
	jobID, releaseShell = startControlledBackgroundShell(t, sess, "controlled build")

	// TRIPWIRE: scripted adapter and a controlled in-process shell; 30s only
	// fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.ProcessInput(ctx, "build it and report", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if res != firstAnswer {
		t.Fatalf("ProcessInput result = %q, want %q", res, firstAnswer)
	}
	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != finalAnswer {
		t.Fatalf("drain result = %q, want %q: the reply to the turn that delivered the completion is the run's answer", drained, finalAnswer)
	}
	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want exactly 2: the answer, then one notification turn", len(reqs))
	}
	if requestsContain(reqs[:1], "<job-notification") {
		t.Fatal("the final-answer request already carried the completion; the window was not opened")
	}
	if last := lastMessageText(reqs[1]); !strings.Contains(last, "<job-notification") || !strings.Contains(last, jobID) {
		t.Fatalf("the notification turn did not carry the completion: %q", last)
	}
	requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyDelivered)
	requireNeverConsumed(t, sess.jobManager, jobID)
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("want no warnings, got %+v", warnings)
	}
}

// TestOneShotDrainKeepsDrainingWhenTheDeliveredCompletionStartsMoreWork: the
// notification turn that delivers the completion the final answer missed
// starts another background job instead of answering. The drain continues as
// for any notification turn and finishes with that job's outcome as the run's
// answer.
func TestOneShotDrainKeepsDrainingWhenTheDeliveredCompletionStartsMoreWork(t *testing.T) {
	t.Parallel()
	const firstAnswer = "first answer, written before the build finished"
	const finalAnswer = "FINAL-ANSWER: both jobs done"
	var sess *Session
	var releaseShell func()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			releaseShell()
			awaitQueuedJobNotification(t, sess)
			return finalResponse(firstAnswer)
		},
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "second-job", Name: "shell", Type: "function",
				Arguments: json.RawMessage(`{"command":"printf second-job-output","mode":"background"}`),
			})
		},
		func(llm.Request) llm.Response { return finalResponse("started a follow-up job; waiting") },
		func(llm.Request) llm.Response { return finalResponse(finalAnswer) },
	}}
	sess = newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	var firstJobID string
	firstJobID, releaseShell = startControlledBackgroundShell(t, sess, "controlled build")

	// TRIPWIRE: scripted adapter, a controlled shell and one real printf; 30s
	// only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.ProcessInput(ctx, "build it and report", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if res != firstAnswer {
		t.Fatalf("ProcessInput result = %q, want %q", res, firstAnswer)
	}
	// No recheck ticks: only completion wakes drive passes, so the follow-up
	// job can never meet the undisposed-background-job announcement while it
	// is still finalizing; its wake delivers it.
	recheck := make(chan time.Time)
	drained, err := sess.drainJobTreeWith(ctx, recheck, sess.kickDriveTree, sess.ProcessInputKind)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != finalAnswer {
		t.Fatalf("drain result = %q, want %q: the drain must finish with the follow-up job's outcome", drained, finalAnswer)
	}
	reqs := adapter.Requests()
	if len(reqs) != 4 {
		t.Fatalf("model calls = %d, want 4: answer, first completion, tool result, second completion", len(reqs))
	}
	if last := lastMessageText(reqs[1]); !strings.Contains(last, "<job-notification") || !strings.Contains(last, firstJobID) {
		t.Fatalf("request 2 did not carry the first completion: %q", last)
	}
	last := lastMessageText(reqs[3])
	match := notificationJobIDPattern.FindStringSubmatch(last)
	if !strings.Contains(last, "<job-notification") || match == nil {
		t.Fatalf("request 4 did not carry the follow-up job's completion: %q", last)
	}
	secondJobID := match[1]
	if secondJobID == firstJobID {
		t.Fatalf("request 4 re-delivered the first job %s instead of the follow-up job", firstJobID)
	}
	for _, id := range []string{firstJobID, secondJobID} {
		requireNotificationState(t, sess.jobManager, id, jobstore.NotifyDelivered)
		requireNeverConsumed(t, sess.jobManager, id)
	}
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("want no warnings, got %+v", warnings)
	}
}

// TestOneShotDrainReturnsAfterAFinalAnswerThatSawEveryCompletion pins the
// loop's exit: the turn that produced the final answer already carried the
// completion, so nothing is unseen and the drain returns with no extra
// provider call.
func TestOneShotDrainReturnsAfterAFinalAnswerThatSawEveryCompletion(t *testing.T) {
	t.Parallel()
	const answer = "FINAL-ANSWER: the build passed"
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse(answer) },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	jobID, releaseShell := startControlledBackgroundShell(t, sess, "controlled build")
	releaseShell()
	if !awaitQueuedJobNotification(t, sess) {
		t.FailNow()
	}

	// TRIPWIRE: scripted adapter and a controlled in-process shell; 30s only
	// fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if res != answer {
		t.Fatalf("notification turn result = %q, want %q", res, answer)
	}
	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != "" {
		t.Fatalf("drain result = %q, want empty: no completion was unseen, so no drain turn may run", drained)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want exactly 1: the answer's turn carried the completion", len(reqs))
	}
	if last := lastMessageText(reqs[0]); !strings.Contains(last, "<job-notification") || !strings.Contains(last, jobID) {
		t.Fatalf("the answer's turn did not carry the completion: %q", last)
	}
	requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyDelivered)
	requireNeverConsumed(t, sess.jobManager, jobID)
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("want no warnings, got %+v", warnings)
	}
}
