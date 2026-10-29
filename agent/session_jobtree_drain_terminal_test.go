package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
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

// Terminal communication cannot turn unobserved custody into quiescence.
func TestTerminalDrainPreservesWatchSendResidue(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{TurnEndsProcess: true}))
	sess.acceptTerminalCommunicate()
	seedWatchSendResidue(t, sess)
	ctx, cancel := context.WithCancel(context.Background())
	wake, hint := newDrainWake()
	sess.SetNotifyFunc(hint)
	_, err := sess.drainJobTreeWith(ctx, wake, nil, func(context.Context) error { cancel(); return nil }, sess.ProcessInputKind)
	if !errors.Is(err, context.Canceled) || !sess.jobManager.hasPendingWatchSends() {
		t.Fatalf("custody lost: %v", err)
	}
}

// TestTerminalDrainKeepsFinalizingDelegateLive pins the finalize window the
// #3531 drain flakes fall through. A delegate's run clears its own
// running/finalizing flags (subagents.go, before announceFinishedGeneration)
// while the controller still holds the generation finalizing until its tail
// has announced the result. In that window the child is live work — its
// completion is about to be delivered — so the terminal-residue verdict must
// not read it as undeliverable residue and let the drain exit before the
// result is announced. holdDelegateTail pins exactly that window with no
// timing; the old inline predicate (running||finalizing||driving) reported the
// child dead and the drain abandoned the still-owed completion, printing the
// pre-drain "waiting" turn instead of the delegate's result.
func TestTerminalDrainKeepsFinalizingDelegateLive(t *testing.T) {
	held := holdDelegateTail(t, holdBeforeAnnouncement)
	held.s.acceptTerminalCommunicate()

	live, err := held.s.treeHasOutstandingWork()
	if err != nil {
		t.Fatalf("treeHasOutstandingWork: %v", err)
	}
	if !live {
		t.Fatal("a delegate whose run ended but whose generation is still finalizing must keep the terminal drain live: its result has not been announced yet, so reading it as residue abandons the completion and exits before it is drained (issue #3531)")
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
		StateDir:        t.TempDir(),
		TurnEndsProcess: true,
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
	requireOwnedDrainAttentionObservedOnce(t, sess, stableShellAttentionID("job-late", "gen-job-late"))
}

// TestNormalDrainStillWaitsOnWatchSendResidue pins the #329 design boundary:
// WITHOUT a terminal communicate (the turn ended some other way), residue is
// treated exactly as before — the drain keeps waiting for the machinery to
// convert it, and only the caller's context bounds the wait. The discard rule
// is gated on the model having explicitly ended the process-ending turn.
func TestNormalDrainStillWaitsOnWatchSendResidue(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		TurnEndsProcess: true,
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
		out, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), recheck, kick, sess.ProcessInputKind)
		done <- drainResult{out: out, err: err}
	}()

	<-firstKick
	// An unbuffered recheck send completes only when waitDrainWake is actually
	// parked on the drain's production select. The second kick proves the drain
	// woke and began another pass without returning.
	sess.notify()
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

// finalizeShell releases a controlled shell and blocks until its finalization
// has closed the job's done channel, which armFinalizedJob does only after the
// owner notification is enqueued: on return the completion is queued and the
// job has left the running map. The channel is taken before the release, so a
// finalization that outruns the lookup cannot pass for one that has not
// started. It may run on the goroutine a scripted provider step is called
// from, so a miss is reported with t.Error and false, never t.Fatal.
func finalizeShell(t *testing.T, jm *jobManager, jobID string, release func()) bool {
	t.Helper()
	done, live := shellDoneChannel(jm, jobID)
	if !live {
		t.Errorf("job %s was not running before its release", jobID)
		return false
	}
	release()
	select {
	case <-done:
		return true
	// TRIPWIRE: hang guard only; finalization is one goroutine hop and a few
	// store appends past the release.
	case <-time.After(30 * time.Second):
		t.Errorf("job %s never finalized", jobID)
		return false
	}
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

// lastMessageText returns the text of the request's final message: for a
// notification turn, the injected reminder carrying the <job-notification>
// blocks delivered by that turn.
func lastMessageText(req llm.Request) string {
	return req.Messages[len(req.Messages)-1].Text()
}

func requireTerminalShellCompletionInRequest(t *testing.T, req llm.Request, jobID, status, reason, exitCode string) {
	t.Helper()
	blocks := regexp.MustCompile(`(?s)<job-notification\b[^>]*>.*?</job-notification>`)
	for _, message := range req.Messages {
		for _, block := range blocks.FindAllString(message.Text(), -1) {
			if !strings.Contains(block, `job_id="`+jobID+`"`) || !strings.Contains(block, `job_type="shell"`) {
				continue
			}
			header, _, _ := strings.Cut(block, ">")
			for _, fact := range []string{`event="` + status + `"`, `job_type="shell"`, `status="` + status + `"`, `reason="` + reason + `"`, `exit_code="` + exitCode + `"`} {
				if !strings.Contains(header, fact) {
					t.Fatalf("completion %s lacks %s: %s", jobID, fact, block)
				}
			}
			return
		}
	}
	t.Fatalf("designated provider request lacks completion %s", jobID)
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
	var jobID string
	var releaseShell func()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The final-answer request is built; finish the job before the reply
			// so the completion lands inside the answer's generation window.
			finalizeShell(t, sess.jobManager, jobID, releaseShell)
			return finalResponse(firstAnswer)
		},
		func(llm.Request) llm.Response { return finalResponse(finalAnswer) },
	}}
	sess = newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		StateDir:        t.TempDir(),
		TurnEndsProcess: true,
	}))
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
	drained, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
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
	requireTerminalShellCompletionInRequest(t, reqs[1], jobID, "command_exited_nonzero", "exit_nonzero", "143")
	requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyDelivered)
	requireNeverConsumed(t, sess.jobManager, jobID)
	requireOwnedDrainAttentionObservedOnce(t, sess, stableShellAttentionID(jobID, loadShellRecord(t, sess.jobManager, jobID).TerminalGen))
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
	testTerminalFollowUpCompletion(t, false)
}

func TestOneShotDrainReturnsWhenFollowUpCompletionRidesToolResult(t *testing.T) {
	t.Parallel()
	testTerminalFollowUpCompletion(t, true)
}

type terminalFollowUpShellEnv struct {
	execenv.ExecutionEnvironment
	*delayedSuccessStreamingExecutor
}

func testTerminalFollowUpCompletion(t *testing.T, early bool) {
	t.Helper()
	const firstAnswer = "first answer, written before the build finished"
	const finalAnswer = "FINAL-ANSWER: both jobs done"
	var sess *Session
	var firstJobID, secondJobID string
	var releaseShell func()
	executor := newDelayedSuccessStreamingExecutor()
	releaseSecond := func() {
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			finalizeShell(t, sess.jobManager, firstJobID, releaseShell)
			return finalResponse(firstAnswer)
		},
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{
				ID: "second-job", Name: "shell", Type: "function",
				Arguments: json.RawMessage(`{"command":"printf second-job-output","mode":"background"}`),
			})
		},
		func(req llm.Request) llm.Response {
			if early {
				requireTerminalShellCompletionInRequest(t, req, secondJobID, "completed", "exit_zero", "0")
				return finalResponse(finalAnswer)
			}
			if requestsContain([]llm.Request{req}, `job_id="`+secondJobID+`"`) {
				t.Error("late follow-up completion was already present in the tool-result request")
			}
			// Request3 is built without the completion. Release it only now, so
			// the drain must present its outcome in a separate fourth request.
			finalizeShell(t, sess.jobManager, secondJobID, releaseSecond)
			return finalResponse("started a follow-up job; waiting")
		},
		func(llm.Request) llm.Response { return finalResponse(finalAnswer) },
	}}
	sess = newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		StateDir:        t.TempDir(),
		TurnEndsProcess: true,
	}))
	firstJobID, releaseShell = startControlledBackgroundShell(t, sess, "controlled build")
	// Reuse the existing successful controlled executor at the external shell
	// boundary. The real shell tool, job lifecycle and receiver remain intact.
	registered := sess.reg.Get("shell")
	original := registered.Exec
	registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		result, err := original(ctx, terminalFollowUpShellEnv{env, executor}, args)
		if err != nil {
			return result, err
		}
		records, err := sess.jobManager.store.Load()
		if err != nil {
			return nil, err
		}
		for id, record := range records {
			if id != firstJobID && record.Type == jobstore.JobShell && record.OwnerSessionID == sess.ID() {
				secondJobID = id
			}
		}
		if secondJobID == "" {
			return nil, errors.New("controlled follow-up shell has no source record")
		}
		if early {
			finalizeShell(t, sess.jobManager, secondJobID, releaseSecond)
		}
		return result, nil
	}
	if err := sess.reg.Register(*registered); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseSecond()
		if secondJobID != "" {
			waitForShellDone(t, sess.jobManager, secondJobID)
		}
	})

	// TRIPWIRE: scripted adapter and controlled shell executors; 30s
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
	drained, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), recheck, sess.kickDriveTree, sess.ProcessInputKind)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != finalAnswer {
		t.Fatalf("drain result = %q, want %q: the drain must finish with the follow-up job's outcome", drained, finalAnswer)
	}
	reqs := adapter.Requests()
	wantRequests := 4
	if early {
		wantRequests = 3
	}
	if len(reqs) != wantRequests {
		t.Fatalf("model calls = %d, want exactly %d for the pinned completion schedule", len(reqs), wantRequests)
	}
	if requestsContain(reqs[:1], "<job-notification") {
		t.Fatal("the initial request already carried a completion")
	}
	requireTerminalShellCompletionInRequest(t, reqs[1], firstJobID, "command_exited_nonzero", "exit_nonzero", "143")
	records, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load follow-up shell source: %v", err)
	}
	var followUpIDs []string
	for id, record := range records {
		if id != firstJobID && record.Type == jobstore.JobShell && record.OwnerSessionID == sess.ID() {
			followUpIDs = append(followUpIDs, id)
		}
	}
	if len(followUpIDs) != 1 {
		t.Fatalf("follow-up owned shell source IDs = %v, want exactly one", followUpIDs)
	}
	if followUpIDs[0] != secondJobID {
		t.Fatalf("follow-up source = %s, want controlled shell %s", followUpIDs[0], secondJobID)
	}
	requireTerminalShellCompletionInRequest(t, reqs[wantRequests-1], secondJobID, "completed", "exit_zero", "0")
	for _, id := range []string{firstJobID, secondJobID} {
		requireNotificationState(t, sess.jobManager, id, jobstore.NotifyDelivered)
		requireNeverConsumed(t, sess.jobManager, id)
	}
	// The first accepted turn remains in history through every later
	// request. Each successful request truthfully covers it, without another
	// acceptance or consumption. Each pinned schedule has its own exact oracle.
	firstCoverage, secondCoverage := []bool{false, true, true, true}, []bool{false, false, false, true}
	if early {
		firstCoverage, secondCoverage = []bool{false, true, true}, []bool{false, false, true}
	}
	requireTerminalAttentionRequestCoverage(t, sess, stableShellAttentionID(firstJobID, loadShellRecord(t, sess.jobManager, firstJobID).TerminalGen), firstCoverage)
	requireTerminalAttentionRequestCoverage(t, sess, stableShellAttentionID(secondJobID, loadShellRecord(t, sess.jobManager, secondJobID).TerminalGen), secondCoverage)
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
		StateDir:        t.TempDir(),
		TurnEndsProcess: true,
	}))
	jobID, releaseShell := startControlledBackgroundShell(t, sess, "controlled build")
	if !finalizeShell(t, sess.jobManager, jobID, releaseShell) {
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
	drained, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
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
	requireOwnedDrainAttentionObservedOnce(t, sess, stableShellAttentionID(jobID, loadShellRecord(t, sess.jobManager, jobID).TerminalGen))
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("want no warnings, got %+v", warnings)
	}
}

// expiredDrainTimerClock observes real timer creation. Cancel only after two
// expired timers so the broken drain's hot loop fails without hanging cleanup.
type expiredDrainTimerClock struct {
	*agenttest.FakeClock
	expired atomic.Int32
	cancel  context.CancelFunc
}

func (c *expiredDrainTimerClock) NewTimer(d time.Duration) clock.Timer {
	if d <= 0 && c.expired.Add(1) == 2 {
		c.cancel()
	}
	return c.FakeClock.NewTimer(d)
}

func TestDrainColdYieldedDelegateDoesNotArmUnownedGraceDeadline(t *testing.T) {
	const prompt = "COLD-DRAIN-DEADLINE-731a"
	adapter := newMessagingAdapter(prompt)
	returns := newActivationReturns()
	yieldGate := make(chan struct{})
	releaseYield := sync.OnceFunc(func() { close(yieldGate) })
	defer releaseYield()
	fifo, releaseOriginal := waitProducerFIFO(t)
	defer releaseOriginal()
	adapter.script("root",
		func(llm.Request) llm.Response { return delegateToolResponse("cold-drain-child", prompt) },
		func(llm.Request) llm.Response { return finalResponse("root-created-cold-child") },
		func(llm.Request) llm.Response { return finalResponse("root-observed-yield-update") })
	adapter.script(prompt, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "cold-drain-shell", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		awaitSignal(t, yieldGate, "cold source yield release")
		return waitResponseFor("cold-drain-wait", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	}, func(llm.Request) llm.Response { return finalResponse("original-runtime-only") })
	original := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.TurnEndsProcess = true
		returns.configure(cfg)
	})
	t.Cleanup(func() { returns.close(t, original) })
	if _, err := original.ProcessInput(context.Background(), "create yielded child", nil); err != nil {
		t.Fatal(err)
	}
	child := original.subagents.sessions()[0]
	sub := original.getSub(child.ID())
	requests := adapter.waitForRequests(t, prompt, 2)
	activation := returns.take(t, child.ID())
	target := regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(requests[1].Messages))
	releaseYield()
	waitForTestSignal(t, activation.returned, "cold source yielded activation")
	checkpoint, err := readWaitCheckpoint(child.TranscriptPath(), child.ID())
	if err != nil || checkpoint == nil || checkpoint.ExecutionTurnID == "" || checkpoint.CommunicateCallID != "cold-drain-wait" || checkpoint.WaitingOn != target || checkpoint.ResumedByTurnID != "" || checkpoint.Generation != 1 || activation.lease != (delegateLease{delegateID: child.owningDelegateID, generation: 1}) {
		t.Fatalf("cold source checkpoint=%+v activation=%+v err=%v", checkpoint, activation, err)
	}
	sub.mu.Lock()
	running, done := sub.running, sub.done
	sub.mu.Unlock()
	if running || done != activation.generationDone {
		t.Fatal("cold source changed generation or retained executor")
	}
	activationStillOpen(t, activation.generationDone)
	if _, err := original.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, original, []string{})
	copyDir := copyWaitingState(t, original.stateDir)
	meta, err := schema.LoadSessionMeta(copyDir, original.ID())
	if err != nil {
		t.Fatal(err)
	}
	// Isolate the copied open generation from every original producer callback.
	returns.close(t, original)
	releaseOriginal()
	beforeRequests := len(adapter.requestsFor(prompt))
	clockStart := time.Now()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		clk := &expiredDrainTimerClock{FakeClock: agenttest.NewFakeClockAt(clockStart), cancel: cancel}
		var denied atomic.Int32
		root, err := RestoreSessionFromMetaWithConfig(original.client, original.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
			StateDir: copyDir, ForceRealIO: true, TurnEndsProcess: true, clock: clk,
			AcquireSessionOwnership: func(id string) error {
				if id == child.ID() {
					denied.Add(1)
					return errors.New("fixture child ownership unavailable")
				}
				return nil
			},
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(root.Close)
		wake, hint := newDrainWake()
		root.SetNotifyFunc(hint)
		before := delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID)
		if !before.CurrentRunOpen || before.Generation != 1 || before.LatestOutcome != nil || root.subagentForChild(child.ID()) != nil || denied.Load() == 0 {
			t.Fatalf("not a cold ownership-refused open generation: %+v, denied=%d", before, denied.Load())
		}
		journalPath := delegateResourceStorePath(copyDir, root.ID())
		journalBefore, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		jobID, releaseShell := startControlledBackgroundShell(t, root, "independent watched shell")
		watch := root.reg.ExecuteCall(ctx, root.env, llm.ToolCallData{
			ID: "independent-shell-watch", Name: "job_watch", Type: "function",
			Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"never-printed-731a"}`, jobID)),
		})
		if watch.IsError {
			t.Fatalf("registered shell watch: %s", watch.Output)
		}
		adapter.script("root", func(req llm.Request) llm.Response {
			requireTerminalShellCompletionInRequest(t, req, jobID, "command_exited_nonzero", "exit_nonzero", "143")
			return finalResponse("independent-shell-observed")
		})
		rootRequests := len(adapter.requestsFor("root"))
		done := make(chan struct{})
		var result string
		var drainErr error
		go func() {
			result, drainErr = root.DrainJobTree(ctx, wake)
			close(done)
		}()
		synctest.Wait()
		clk.Advance(2*DrainStallTimeout + time.Nanosecond)
		synctest.Wait()
		if got := clk.expired.Load(); got != 0 {
			t.Fatalf("cold child armed repeated expired drain timers: %d, want 0", got)
		}
		select {
		case <-done:
			t.Fatalf("drain returned before independent shell completion: %q, %v", result, drainErr)
		default:
		}
		if len(adapter.requestsFor("root")) != rootRequests {
			t.Fatal("clock advancement created a phantom provider request")
		}
		releaseShell()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("shell completion did not wake and finish drain")
		}
		if drainErr != nil || result != "independent-shell-observed" || len(adapter.requestsFor("root")) != rootRequests+1 {
			t.Fatalf("completion drain=%q, %v, requests=%d, want one useful observation", result, drainErr, len(adapter.requestsFor("root"))-rootRequests)
		}
		after := delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID)
		if !after.CurrentRunOpen || after.Generation != before.Generation || after.LatestOutcome != nil || root.subagentForChild(child.ID()) != nil || root.childDrainAbandoned(child.ID()) || root.childDrainGracePending(child.ID()) || len(adapter.requestsFor(prompt)) != beforeRequests {
			t.Fatalf("cold generation was driven or abandoned: before=%+v after=%+v", before, after)
		}
		journalAfter, err := os.ReadFile(journalPath)
		if err != nil || !bytes.Equal(journalBefore, journalAfter) {
			t.Fatalf("cold generation controller journal changed during drain: %v", err)
		}
		record := loadShellRecord(t, root.jobManager, jobID)
		requireNotificationState(t, root.jobManager, jobID, jobstore.NotifyDelivered)
		requireOwnedDrainAttentionObservedOnce(t, root, stableShellAttentionID(jobID, record.TerminalGen))
		if clk.expired.Load() != 0 {
			t.Fatal("completion rearmed an expired cold-child deadline")
		}
	})
}

func TestEphemeralNotificationDrainShell(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("ephemeral-initial-731a") },
			func(llm.Request) llm.Response { return finalResponse("ephemeral-observed-731a") },
		}}
		// Intentionally no StateDir. This supported mode must not create one.
		s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{TurnEndsProcess: true}), withoutGitSnapshot())
		wake, hint := newDrainWake()
		s.SetNotifyFunc(hint)
		if _, err := s.ProcessInput(context.Background(), "start ephemeral run", nil); err != nil {
			t.Fatal(err)
		}
		jobID, release := startControlledBackgroundShell(t, s, "ephemeral controlled shell")
		done, _ := shellDoneChannel(s.jobManager, jobID)
		// On RED, the production finalizer retries a missing-custody error.
		// Release only failed-test cleanup waiters, never the successful path.
		t.Cleanup(func() {
			if t.Failed() {
				_ = s.jobManager.store.Close()
				s.jobManager.mu.Lock()
				if run := s.jobManager.running[jobID]; run != nil {
					run.closeDone()
				}
				delete(s.jobManager.running, jobID)
				s.jobManager.mu.Unlock()
				// Teardown only: let an already sleeping failed finalizer
				// observe the closed source before the bubble exits.
				time.Sleep(shellFinalizeMaxRetryDelay)
			}
		})
		before := time.Now()
		release()
		synctest.Wait()
		select {
		case <-done:
		default:
			code := 143
			err := s.jobManager.finalize(jobID, jobstore.StatusCommandExitedNonzero, "exit_nonzero", &code)
			t.Fatalf("ephemeral shell did not finalize before clock advancement: %v", err)
		}
		result, err := s.DrainJobTree(context.Background(), wake)
		if err != nil || result != "ephemeral-observed-731a" {
			t.Fatalf("ephemeral drain = %q, %v", result, err)
		}
		requests := adapter.Requests()
		if len(requests) != 2 {
			t.Fatalf("provider requests = %d, want initial and one observation", len(requests))
		}
		requireTerminalShellCompletionInRequest(t, requests[1], jobID, "command_exited_nonzero", "exit_nonzero", "143")
		record := loadShellRecord(t, s.jobManager, jobID)
		id := stableShellAttentionID(jobID, record.TerminalGen)
		s.attentionMu.Lock()
		fold, foldErr := s.delegateAttentionFoldLocked()
		s.attentionMu.Unlock()
		if foldErr != nil || len(fold.order) != 1 || fold.order[0] != id || len(fold.observed) != 1 || fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("ephemeral custody/observation = %#v, %v", fold, foldErr)
		}
		s.mu.Lock()
		acceptances, receipts := 0, 0
		for _, turn := range s.history {
			if turn.AttentionID == id {
				acceptances++
			}
			for _, observed := range turn.ObservedNotificationIDs {
				if observed == id {
					receipts++
				}
			}
		}
		s.mu.Unlock()
		if acceptances != 1 || receipts != 1 {
			t.Fatalf("raw lifetime acceptance/receipt counts = %d/%d, want 1/1", acceptances, receipts)
		}
		requireNotificationState(t, s.jobManager, jobID, jobstore.NotifyDelivered)
		requireNeverConsumed(t, s.jobManager, jobID)
		hint()
		if _, err := s.DrainJobTree(context.Background(), wake); err != nil {
			t.Fatal(err)
		}
		if len(adapter.Requests()) != 2 || !time.Now().Equal(before) || s.stateDir != "" || s.attachedTranscript() != nil {
			t.Fatal("phantom request, clock pumping or invented persistence")
		}
	})
}

func TestEphemeralNotificationFailureCompactionAndDuplicate(t *testing.T) {
	requireEphemeralNotificationFailureRecovery(t, nil)
}

func TestProcessingDelegateDeliveryCleanupIsolatesAuthenticationLog(t *testing.T) {
	clock := agenttest.NewFakeClock()
	var producer *Session
	t.Run("producer", func(owner *testing.T) {
		producer = newProcessingDelegateDeliveryFixture(owner, clock)
	})
	if producer == nil {
		t.Fatal("producer did not reach acceptance")
	}
	_, statErr := os.Stat(producer.TranscriptPath())
	producer.delegateController.mu.Lock()
	closing := producer.delegateController.closing
	producer.delegateController.mu.Unlock()
	if !closing || clock.PendingCallbacks() != 0 || !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("producer cleanup: closing=%t callbacks=%d transcript stat=%v, want fenced controller, no callbacks and removed transcript", closing, clock.PendingCallbacks(), statErr)
	}
	t.Run("authentication_receiver", func(receiver *testing.T) {
		requireEphemeralNotificationFailureRecovery(receiver, func() {
			// A leaked producer retry would now write into the actual strict
			// authentication capture, without a sleep or fabricated log record.
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
		})
	})
}

func requireEphemeralNotificationFailureRecovery(t *testing.T, afterCapture func()) {
	t.Helper()
	// This test is not parallel: capture the deliberately failed provider's
	// daemon warning as well as its session event, rather than leaking stderr.
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if afterCapture != nil {
		afterCapture()
	}
	base := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("initial") },
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "ephemeral-pending-731a") {
				t.Error("successful retry lost pending content during compaction")
			}
			return finalResponse("observed")
		},
	}}
	adapter := &failingRequestAdapter{fakeAdapter: base, failAt: 1, err: llm.ErrorFromHTTPStatus("openai", 401, "fixture failure", nil, nil)}
	s := newSession(t, withAdapter(adapter), withoutGitSnapshot())
	wake, hint := newDrainWake()
	s.SetNotifyFunc(hint)
	if _, err := s.ProcessInput(context.Background(), "initialize", nil); err != nil {
		t.Fatal(err)
	}
	n := notification{ID: "ephemeral-id-731a", Message: llm.User("ephemeral-pending-731a")}
	for range 2 {
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
	}
	_, failed := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	var providerErr llm.Error
	if !errors.As(failed, &providerErr) || providerErr.StatusCode() != 401 || llm.Kind(failed) != llm.KindAuthentication {
		t.Fatalf("expected typed authentication failure, got %v", failed)
	}
	warnings := collectStallWarnings(s)
	if len(warnings) != 1 {
		t.Fatalf("expected one paused warning event, got %+v", warnings)
	}
	warning, ok := warnings[0].Data.(events.WarningData)
	if !ok || warning.Code != events.WarningCodeAttentionPaused || warning.Source != "evener" || warning.Hint != "HTTP 401 (authentication)" {
		t.Fatalf("expected structured authentication pause warning, got %#v", warnings[0].Data)
	}
	var logRecord struct {
		Level   string `json:"level"`
		Session string `json:"session"`
		Failure string `json:"failure"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(logged.Bytes()), &logRecord); err != nil || logRecord.Level != "WARN" || logRecord.Session != s.ID() || logRecord.Failure != "HTTP 401 (authentication)" {
		t.Fatalf("expected one structured authentication log, got %q, %v", logged.String(), err)
	}
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil || len(fold.order) != 1 || len(fold.pendingIDs()) != 1 || len(fold.observed) != 0 || len(fold.resolutions) != 0 {
		t.Fatalf("failed response credited or duplicated custody: %#v, %v", fold, err)
	}
	// Use the real compaction publication transaction with a summary that omits
	// the accepted turn. Its pending-attention restoration must consult the
	// canonical lifetime fold, not reconstruct from this replacement history.
	s.mu.Lock()
	snapLen, revision := len(s.history), s.historyRevision
	appends := s.persistedAppendLogBase + len(s.persistedAppendLog)
	s.mu.Unlock()
	folded := []schema.Turn{{Kind: schema.TurnSteering, Message: llm.User("compacted summary")}}
	_, _, commit, _ := s.stageCompactionEffects(context.Background(), &folded)
	published, ok, refusal := s.publishFoldTransaction(snapLen, revision, appends, folded, commit, nil)
	if !ok || refusal != nil {
		t.Fatalf("compaction publish = %v, %v", ok, refusal)
	}
	retained := 0
	for _, turn := range published {
		if turn.AttentionID == n.ID && requestContainsText(llm.Request{Messages: []llm.Message{turn.Message}}, "ephemeral-pending-731a") {
			retained++
		}
	}
	if retained != 1 {
		t.Fatalf("pending turns retained across compaction = %d, want 1", retained)
	}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueNotification(n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainJobTree(context.Background(), wake); err != nil {
		t.Fatal(err)
	}
	adapter.callsMu.Lock()
	calls := adapter.calls
	adapter.callsMu.Unlock()
	s.attentionMu.Lock()
	fold, err = s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if calls != 3 || len(base.Requests()) != 2 || err != nil || len(fold.order) != 1 || len(fold.observed) != 1 || fold.resolutions[n.ID] != delegateAttentionConsumed || len(fold.pendingIDs()) != 0 {
		t.Fatalf("retry/duplicate observation: calls=%d successful=%d fold=%#v err=%v", calls, len(base.Requests()), fold, err)
	}
	if more := collectStallWarnings(s); len(more) != 0 {
		t.Fatalf("unexpected warning after recovery: %+v", more)
	}
}

func TestConfiguredNotificationWriterMissingRemainsRetryable(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withoutGitSnapshot(), withSteps(
		func(llm.Request) llm.Response { return finalResponse("initial") },
		func(llm.Request) llm.Response { return finalResponse("observed after writer repair") },
	))
	if _, err := s.ProcessInput(context.Background(), "initialize", nil); err != nil {
		t.Fatal(err)
	}
	writer := s.attachedTranscript()
	s.attentionMu.Lock()
	s.mu.Lock()
	s.setTranscriptLocked(nil)
	s.mu.Unlock()
	s.attentionMu.Unlock()
	n := notification{ID: "configured-writer-missing-731a", Message: llm.User("must not fall back")}
	err := s.enqueueNotification(n)
	s.attentionMu.Lock()
	credited := len(s.attentionFoldCursor.fold.order)
	s.mu.Lock()
	s.setTranscriptLocked(writer)
	s.mu.Unlock()
	s.attentionMu.Unlock()
	if err == nil || credited != 0 {
		t.Fatalf("configured missing writer fell back: credited=%d err=%v", credited, err)
	}
	if err := s.enqueueNotification(n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireOwnedDrainAttentionObservedOnce(t, s, n.ID)
}

func requireTerminalAttentionRequestCoverage(t *testing.T, s *Session, id string, want []bool) {
	t.Helper()
	accepted, consumed := 0, 0
	var coverage []bool
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		turn := entry.Turn
		if turn.AttentionID == id {
			accepted++
		}
		if turn.Kind == schema.TurnAssistant && turn.Message.Role == llm.RoleAssistant {
			count := 0
			for _, observed := range turn.ObservedNotificationIDs {
				if observed == id {
					count++
				}
			}
			if count > 1 {
				t.Fatalf("duplicate ID within one successful receipt: %s", id)
			}
			coverage = append(coverage, count == 1)
		}
		if resolution := turn.AttentionResolution; resolution != nil && resolution.AttentionID == id {
			if resolution.Disposition != string(delegateAttentionConsumed) {
				t.Fatalf("attention %s was discarded", id)
			}
			consumed++
		}
	}
	if accepted != 1 || consumed != 1 || len(coverage) != len(want) {
		t.Fatalf("attention %s acceptance=%d consumption=%d coverage=%v, want 1/1/%v", id, accepted, consumed, coverage, want)
	}
	for i := range want {
		if coverage[i] != want[i] {
			t.Fatalf("attention %s exact successful request coverage=%v, want %v", id, coverage, want)
		}
	}
}

// TestOneShotDrainPreservesWatchFramesQueuedBeforeTheFinalAnswer: a watch frame
// (an output match the model asked for) and the job's completion both land
// during the final answer's generation. Neither was presented in that answer's
// request, so the drain preserves and delivers both on one extra turn.
func TestOneShotDrainPreservesWatchFramesQueuedBeforeTheFinalAnswer(t *testing.T) {
	t.Parallel()
	const firstAnswer = "first answer, written before the build finished"
	const finalAnswer = "FINAL-ANSWER: the build passed"
	var sess *Session
	var jobID string
	var releaseShell func()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// The final-answer request is built. Fire the watch, then finish the
			// job, so both a watch frame and the completion are queued before
			// the reply.
			feedJob(sess.jobManager, jobID, []byte("READY\n"))
			finalizeShell(t, sess.jobManager, jobID, releaseShell)
			if n := sess.peekNotifications(); n < 1 {
				t.Errorf("queued shell completions before the reply = %d, want 1", n)
			}
			if ids, err := sess.pendingNotificationIDs(); err != nil || len(ids) == 0 {
				t.Errorf("produced watch lacks canonical custody: ids=%v err=%v", ids, err)
			}
			return finalResponse(firstAnswer)
		},
		func(llm.Request) llm.Response { return finalResponse(finalAnswer) },
	}}
	sess = newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		StateDir:        t.TempDir(),
		TurnEndsProcess: true,
	}))
	jobID, releaseShell = startControlledBackgroundShell(t, sess, "controlled build")
	watchRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "watch-it", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"` + jobID + `","output_match":"READY"}`),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch create: %s", watchRes.Output)
	}

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
	if early := collectStallWarnings(sess); len(early) != 0 {
		t.Fatalf("want no warnings before the drain, got %+v", early)
	}

	drained, err := sess.DrainJobTree(ctx, drainWakeForTest(sess))
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != finalAnswer {
		t.Fatalf("drain result = %q, want %q", drained, finalAnswer)
	}
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("unexpected discard warnings: %+v", warnings)
	}
	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want exactly 2: the answer, then one turn carrying the completion and watch frame", len(reqs))
	}
	last := lastMessageText(reqs[1])
	if !strings.Contains(last, "<job-notification") || !strings.Contains(last, jobID) {
		t.Fatalf("the notification turn did not carry the completion: %q", last)
	}
	if !strings.Contains(last, `event="watch"`) {
		t.Fatalf("the notification turn lost the produced watch frame: %q", last)
	}
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("queued notifications after the drain = %d, want 0", p)
	}
	requireNotificationState(t, sess.jobManager, jobID, jobstore.NotifyDelivered)
	requireNeverConsumed(t, sess.jobManager, jobID)
}
