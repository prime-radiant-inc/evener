package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// usageLimitError is the provider's usage-limit 429 as the openai adapter
// classifies it: a quota failure that says when the allowance returns.
func usageLimitError(t *testing.T, provider string, resetAt time.Time) error {
	t.Helper()
	body := map[string]any{"error": map[string]any{
		"type":      "usage_limit_reached",
		"message":   "The usage limit has been reached",
		"resets_at": json.Number(strconv.FormatInt(resetAt.Unix(), 10)),
	}}
	err := llm.ErrorFromHTTPStatus(provider, 429, "responses.create(stream) failed", body, nil)
	if got, ok := llm.UsageLimitResetAt(err); llm.Kind(err) != llm.KindQuotaExceeded || !ok || !got.Equal(resetAt) {
		t.Fatalf("fixture: kind=%v reset=%v ok=%t, want a quota failure resetting at %v", llm.Kind(err), got, ok, resetAt)
	}
	return err
}

// quotaResetAt is the reset every test's usage limit names: two hours after
// the fake clock's start.
var quotaResetAt = time.Unix(1000, 0).Add(2 * time.Hour)

// recoverProvider makes the adapter answer every later request with a
// communicate call, the provider's quota having come back.
func recoverProvider(adapter *streamingAdapter) {
	var calls atomic.Int64
	adapter.mu.Lock()
	adapter.streamErr = nil
	adapter.streamScript = func(st *llm.ChanStream) {
		call := communicateCall("recovered-"+strconv.FormatInt(calls.Add(1), 10), "back at work")
		sendCommunicatePreview(st, call)
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}
	adapter.mu.Unlock()
}

// limitAfterFirstAnswer makes the adapter answer the next request with a
// communicate call and meet limit on every request after it.
func limitAfterFirstAnswer(adapter *streamingAdapter, limit error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.streamScript = func(st *llm.ChanStream) {
		call := communicateCall("first-answer", "done with the work")
		sendCommunicatePreview(st, call)
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		adapter.mu.Lock()
		adapter.streamErr = limit
		adapter.mu.Unlock()
	}
}

func streamCalls(adapter *streamingAdapter) int {
	_, n := adapter.Counts()
	return n
}

func adapterRequests(adapter *streamingAdapter) []llm.Request {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]llm.Request(nil), adapter.requests...)
}

// waitUntil polls cond until it holds, failing once tripped is closed (the
// test's tripwire).
func waitUntil(t *testing.T, tripped <-chan struct{}, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-tripped:
			t.Fatalf("never reached: %s", what)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// inputRun is one ProcessInputKind call running in the background, the way
// the serve loop runs an input while the session's clients keep talking to it.
type inputRun struct {
	done chan struct{}
	out  string
	err  error
}

func startInput(ctx context.Context, s *Session, input string, kind EntryKind) *inputRun {
	run := &inputRun{done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.out, run.err = s.ProcessInputKind(ctx, input, nil, kind)
	}()
	return run
}

func (r *inputRun) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// awaitQuotaWait waits until the input's model call is waiting out the usage
// limit, failing if the input ends first.
func (r *inputRun) awaitQuotaWait(t *testing.T, s *Session, tripped <-chan struct{}) {
	t.Helper()
	waitUntil(t, tripped, "the model call waits for quota", func() bool {
		if r.finished() {
			t.Fatalf("the input ended (out %q, err %v) instead of waiting for the usage limit to reset", r.out, r.err)
		}
		return s.waitingForQuota()
	})
}

// awaitEnd waits for the input to return.
func (r *inputRun) awaitEnd(t *testing.T, tripped <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-r.done:
	case <-tripped:
		t.Fatalf("the input never ended: %s", what)
	}
}

// reachReset moves the fake clock to the usage limit's reset.
func reachReset(clk *agenttest.FakeClock) {
	clk.Advance(2 * time.Hour)
	clk.Drain()
}

// A usage-limit 429 that names its reset time means every request before
// then fails the same way. The model call waits inside itself instead of
// failing the turn: no further requests, the session stays busy (so it does
// not retire), notifications arriving meanwhile queue as they do behind any
// running turn, and an active goal stays active. At the reset the call
// retries and the turn finishes; the queued notification is then delivered
// once.
func TestUsageLimitWaitsInsideTheModelCall(t *testing.T) {
	s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSession(t, s)
	if _, err := s.SetGoal(context.Background(), "ship the release"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	s.SetKickFunc(func(string) {})
	trip := tripwireCtx(t).Done()
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitQuotaWait(t, s, trip)

	enqueueCompletedJobNotification(t, s, "job_quota")
	s.notify()
	armOneRootAttention(t, s, "dlg_quota", "delegate:dlg_quota/delivery/1")
	clk.Advance(time.Hour)
	clk.Drain()
	if got := streamCalls(adapter); got != 1 {
		t.Fatalf("model requests before the reset = %d, want only the one that met the limit", got)
	}
	if got := s.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState while waiting = %q, want the session busy in its turn", got)
	}
	if !slices.ContainsFunc(s.retirementInputBlockers(), func(b RetirementBlocker) bool { return b.Category == "turn" }) {
		t.Fatal("a session waiting for quota inside its turn is not blocked from retiring by that turn")
	}
	if status, _, _ := s.GoalStatus(); status != string(goal.StatusActive) {
		t.Fatalf("goal status while waiting = %q, want it still active", status)
	}

	recoverProvider(adapter)
	reachReset(clk)
	run.awaitEnd(t, trip, "the call resumes at the reset")
	if run.err != nil {
		t.Fatalf("the input after the reset: %v", run.err)
	}
	if s.waitingForQuota() {
		t.Fatal("the session still waits for quota after its call resumed")
	}
	// The scripted provider never makes progress, so the goal runs its
	// continuations until the no-progress breaker stops it: it was driven on
	// after the wait, not blocked by the usage limit.
	if snap, _ := s.getOrCreateGoalStore().Snapshot(); snap.Iterations == 0 {
		t.Fatalf("goal after the wait = %+v, want its continuations to have run", snap)
	}
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	if got := s.jobNotificationsDelivered(); got != 1 {
		t.Fatalf("job notifications delivered = %d, want the one queued during the wait delivered once", got)
	}
	if !requestsContain(adapterRequests(adapter)[1:], "job_quota") {
		t.Fatal("no request after the reset carried the job completion queued during the wait")
	}
}

// The wait is session state a client reads while the session is busy: the
// failure summary names the usage limit and when the call resumes, and it
// clears once the call resumes.
func TestWaitingForQuotaIsVisibleInSessionState(t *testing.T) {
	s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSession(t, s)
	trip := tripwireCtx(t).Done()
	if failure := s.RestingFailure(); failure != nil {
		t.Fatalf("precondition: failure before any turn = %+v", failure)
	}
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitQuotaWait(t, s, trip)

	failure := s.RestingFailure()
	if failure == nil || failure.ResumesAt != quotaResetAt.UnixMilli() {
		t.Fatalf("failure while waiting = %+v, want it to resume at %d", failure, quotaResetAt.UnixMilli())
	}
	if failure.Cause == nil || failure.Cause.Status != 429 || failure.Cause.Provider != "openai" {
		t.Fatalf("failure cause while waiting = %+v, want the provider's 429", failure.Cause)
	}

	recoverProvider(adapter)
	reachReset(clk)
	run.awaitEnd(t, trip, "the call resumes at the reset")
	if failure := s.RestingFailure(); failure != nil {
		t.Fatalf("failure after the call resumed = %+v, want none", failure)
	}
}

// A model switch while waiting is the user choosing a model the spent quota
// may not limit: the waiting call retries at once on it, without waiting for
// the reset, and the turn finishes there.
func TestModelSwitchWhileWaitingForQuotaRetriesOnTheNewModel(t *testing.T) {
	s, _, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSession(t, s)
	trip := tripwireCtx(t).Done()
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitQuotaWait(t, s, trip)

	recoverProvider(adapter)
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	run.awaitEnd(t, trip, "the switch resumes the call")
	if run.err != nil {
		t.Fatalf("the input after the switch: %v", run.err)
	}
	reqs := adapterRequests(adapter)
	if len(reqs) != 2 || reqs[1].Model != "gpt-5.4" {
		models := make([]string, 0, len(reqs))
		for _, req := range reqs {
			models = append(models, req.Model)
		}
		t.Fatalf("requests ran on %v, want the limited one then one retry on gpt-5.4", models)
	}
}

// A user message while waiting tries the provider once: the user may know
// the quota came back. A steer rides that try; if the limit still holds, the
// call keeps waiting with the steer delivered. A queued message wakes the
// same one try and runs as its own turn once the waiting one finishes.
func TestUserMessageWhileWaitingForQuotaRetriesOnce(t *testing.T) {
	type send func(t *testing.T, s *Session, text string)
	steer := func(t *testing.T, s *Session, text string) {
		t.Helper()
		if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "steer-" + text,
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
		}); err != nil {
			t.Fatalf("steer: %v", err)
		}
	}
	queue := func(t *testing.T, s *Session, text string) {
		t.Helper()
		if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: "queue-" + text,
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
		}); err != nil {
			t.Fatalf("queue: %v", err)
		}
	}
	for _, tc := range []struct {
		name string
		send send
		// carried says whether the retry's request carries the message.
		carried bool
	}{
		{"steer", steer, true},
		{"queued message", queue, false},
	} {
		t.Run(tc.name+" still limited", func(t *testing.T) {
			s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
			serveSessionWithMutationStore(t, s)
			trip := tripwireCtx(t).Done()
			run := startInput(context.Background(), s, "start the work", EntryUserInput)
			run.awaitQuotaWait(t, s, trip)

			tc.send(t, s, "is the quota back yet")
			waitUntil(t, trip, "the user's message retries the call", func() bool { return streamCalls(adapter) == 2 })
			run.awaitQuotaWait(t, s, trip)
			if tc.carried && !requestsContain(adapterRequests(adapter)[1:], "is the quota back yet") {
				t.Fatal("the retry the steer woke did not carry the steer")
			}
			clk.Advance(time.Hour)
			clk.Drain()
			if got := streamCalls(adapter); got != 2 {
				t.Fatalf("model requests after the user's one try = %d, want 2: the call kept waiting", got)
			}

			recoverProvider(adapter)
			reachReset(clk)
			run.awaitEnd(t, trip, "the call resumes at the reset")
			if run.err != nil {
				t.Fatalf("the input after the reset: %v", run.err)
			}
			if !requestsContain(adapterRequests(adapter)[2:], "is the quota back yet") {
				t.Fatal("no request after the reset carried the user's message")
			}
		})
	}
	t.Run("quota came back", func(t *testing.T) {
		s, _, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
		serveSessionWithMutationStore(t, s)
		trip := tripwireCtx(t).Done()
		run := startInput(context.Background(), s, "start the work", EntryUserInput)
		run.awaitQuotaWait(t, s, trip)

		recoverProvider(adapter)
		steer(t, s, "try again now")
		run.awaitEnd(t, trip, "the steer resumes the call")
		if run.err != nil {
			t.Fatalf("the input after the quota came back: %v", run.err)
		}
		if got := streamCalls(adapter); got != 2 || !requestsContain(adapterRequests(adapter)[1:], "try again now") {
			t.Fatalf("model requests = %d, want the limited one then one carrying the steer", got)
		}
	})
}

// Waiting is not working: the wait and the retries it ends in do not count
// as model rounds against the input's budget.
func TestQuotaWaitDoesNotSpendRounds(t *testing.T) {
	s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSessionWithMutationStore(t, s)
	trip := tripwireCtx(t).Done()
	s.mu.Lock()
	roundsBefore := s.totalRounds
	s.mu.Unlock()
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitQuotaWait(t, s, trip)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-rounds",
		Input:            []appwire.InputItem{{Type: "text", Text: "still there?"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	waitUntil(t, trip, "the steer retries the call", func() bool { return streamCalls(adapter) == 2 })
	run.awaitQuotaWait(t, s, trip)
	recoverProvider(adapter)
	reachReset(clk)
	run.awaitEnd(t, trip, "the call resumes at the reset")
	s.mu.Lock()
	rounds := s.totalRounds - roundsBefore
	s.mu.Unlock()
	if rounds != 1 {
		t.Fatalf("model rounds spent = %d over three requests, want 1: the waits counted as rounds", rounds)
	}
}

// A Stop while waiting is an ordinary interrupt: the call stops waiting at
// once, the turn ends as cancelled rather than failed, and the goal stays
// active as it does after any Stop.
func TestStopWhileWaitingForQuotaIsAnOrdinaryInterrupt(t *testing.T) {
	s, _, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSession(t, s)
	if _, err := s.SetGoal(context.Background(), "ship the release"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	s.SetKickFunc(func(string) {})
	trip := tripwireCtx(t).Done()
	// The turn context a user's Stop cancels, marked as production marks it.
	turnCtx, stop := context.WithCancel(context.Background())
	defer stop()
	run := startInput(WithQueuedInputDrainOnInterrupt(turnCtx, context.Background()), s, "start the work", EntryUserInput)
	run.awaitQuotaWait(t, s, trip)

	stop()
	run.awaitEnd(t, trip, "the Stop ends the wait")
	if !errors.Is(run.err, context.Canceled) {
		t.Fatalf("input error after the Stop = %v, want the ordinary cancellation", run.err)
	}
	if s.waitingForQuota() {
		t.Fatal("the session still waits for quota after the Stop")
	}
	if got := s.WireState(); got == appwire.ThreadStatusSystemError {
		t.Fatal("a stopped wait reads Failed")
	}
	if status, _, _ := s.GoalStatus(); status != string(goal.StatusActive) {
		t.Fatalf("goal status after the Stop = %q, want it active as after any Stop", status)
	}
	if got := streamCalls(adapter); got != 1 {
		t.Fatalf("model requests = %d, want only the one that met the limit", got)
	}
}

// A one-shot run (`evener run`, and every delegate under it) has no human to
// switch models and exits when its work drains, so a wait until the reset
// could hold the process for days. Its turn fails fast with the usage limit,
// as any provider failure fails it.
func TestOneShotRunFailsFastOnUsageLimit(t *testing.T) {
	dir := t.TempDir()
	adapter := &streamingAdapter{name: "openai", streamErr: usageLimitError(t, "openai", quotaResetAt)}
	c := llm.NewClient()
	c.Register(adapter)
	policy := llm.RetryPolicy{MaxRetries: 0}
	s := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		clock:           agenttest.NewFakeClock(),
		TurnEndsProcess: true,
	}))
	trip := tripwireCtx(t).Done()
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitEnd(t, trip, "a one-shot turn fails on the usage limit")
	if llm.Kind(run.err) != llm.KindQuotaExceeded {
		t.Fatalf("one-shot input error = %v, want the usage-limit failure", run.err)
	}
	if got := streamCalls(adapter); got != 1 {
		t.Fatalf("model requests = %d, want one", got)
	}
}

// A usage limit that names no reset time keeps today's handling: the call
// does not wait, the turn fails, and the goal blocks as on any provider
// failure.
func TestUsageLimitWithoutResetTimeDoesNotWait(t *testing.T) {
	body := map[string]any{"error": map[string]any{"type": "usage_limit_reached", "message": "The usage limit has been reached"}}
	err := llm.ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", body, nil)
	if _, ok := llm.UsageLimitResetAt(err); ok || llm.Kind(err) != llm.KindQuotaExceeded {
		t.Fatalf("fixture: want a quota failure with no reset time, got kind %v", llm.Kind(err))
	}
	s, _, _, _ := newAttentionLivelockSession(t, err)
	serveSession(t, s)
	if _, setErr := s.SetGoal(context.Background(), "ship the release"); setErr != nil {
		t.Fatalf("SetGoal: %v", setErr)
	}
	trip := tripwireCtx(t).Done()
	run := startInput(context.Background(), s, "start the work", EntryUserInput)
	run.awaitEnd(t, trip, "a usage limit without a reset time fails the turn")
	if llm.Kind(run.err) != llm.KindQuotaExceeded {
		t.Fatalf("input error = %v, want the usage-limit failure", run.err)
	}
	if status, _, _ := s.GoalStatus(); status != string(goal.StatusBlocked) {
		t.Fatalf("goal status = %q, want blocked as on any provider failure", status)
	}
}

// newQuotaDelegateRoot is a root session that can run real delegates, every
// one of them on adapter and the fake clock.
func newQuotaDelegateRoot(t *testing.T, adapter *streamingAdapter) (*Session, *agenttest.FakeClock) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(adapter)
	policy := llm.RetryPolicy{MaxRetries: 0}
	clk := agenttest.NewFakeClock()
	s := newSession(t, withClient(c), withDir(dir), withoutGitSnapshot(), withConfig(SessionConfig{
		StateDir:         dir,
		LLMRetryPolicy:   &policy,
		MaxSubagentDepth: 2,
		clock:            clk,
	}))
	return s, clk
}

// startQuotaDelegate creates a delegate under root and returns its runtime.
func startQuotaDelegate(t *testing.T, root *Session) (*subagent, string) {
	t.Helper()
	created := root.createDelegate(context.Background(), delegateArgs{Task: "do the delegated work"})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}
	sub := root.subagents.get(created.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %q has no runtime", created.ChildSessionID)
	}
	return sub, created.DelegateID
}

func subagentDone(sub *subagent) bool {
	select {
	case <-sub.done:
		return true
	default:
		return false
	}
}

// A delegate's generation turn meeting the limit waits in its call like any
// other: the run stays open and running (the parent sees it working, not
// failed), and it resumes at the reset and finishes its work.
func TestDelegateGenerationWaitsForQuota(t *testing.T) {
	adapter := &streamingAdapter{name: "openai", streamErr: usageLimitError(t, "openai", quotaResetAt)}
	root, clk := newQuotaDelegateRoot(t, adapter)
	trip := tripwireCtx(t).Done()
	sub, _ := startQuotaDelegate(t, root)
	waitUntil(t, trip, "the delegate's call waits for quota", func() bool {
		if subagentDone(sub) {
			t.Fatalf("the delegate's run ended on the usage limit: status=%q err=%v", sub.status, sub.err)
		}
		return sub.sess.waitingForQuota()
	})
	sub.mu.Lock()
	status := sub.status
	sub.mu.Unlock()
	if status != SubagentRunning {
		t.Fatalf("delegate status while waiting = %q, want running", status)
	}

	recoverProvider(adapter)
	reachReset(clk)
	waitUntil(t, trip, "the delegate's run finishes after the reset", func() bool { return subagentDone(sub) })
	sub.mu.Lock()
	status, runErr, fatal := sub.status, sub.err, sub.fatalRunGated
	sub.mu.Unlock()
	if status == SubagentFailed || runErr != nil || fatal {
		t.Fatalf("delegate after the reset: status=%q err=%v fatal-gated=%t, want it finished", status, runErr, fatal)
	}
	if got := streamCalls(adapter); got != 2 {
		t.Fatalf("model requests = %d, want the limited one and one after the reset", got)
	}
}

// Stopping a delegate that waits for quota is an ordinary Stop: the run
// ends cancelled rather than on the usage limit, and the fatal gate that would stop the work
// its subtree owns stays shut, so a job the delegate started survives.
func TestStoppingAQuotaWaitingDelegateIsAnOrdinaryStop(t *testing.T) {
	adapter := &streamingAdapter{name: "openai", streamErr: usageLimitError(t, "openai", quotaResetAt)}
	root, _ := newQuotaDelegateRoot(t, adapter)
	trip := tripwireCtx(t).Done()
	sub, delegateID := startQuotaDelegate(t, root)
	waitUntil(t, trip, "the delegate's call waits for quota", func() bool {
		if subagentDone(sub) {
			t.Fatalf("the delegate's run ended on the usage limit: status=%q err=%v", sub.status, sub.err)
		}
		return sub.sess.waitingForQuota()
	})
	jm := sub.sess.jobManager
	running := &jobstore.JobRecord{JobID: "job_descendant", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: true, OwnerSessionID: sub.sess.ID()}
	jm.mu.Lock()
	jm.running[running.JobID] = &runningJob{rec: running}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, running.JobID)
		jm.mu.Unlock()
	})

	if _, err := jobStopTool(context.Background(), root, map[string]any{"target": delegateID}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatalf("job_stop: %v", err)
	}
	waitUntil(t, trip, "the stopped delegate's run ends", func() bool { return subagentDone(sub) })
	sub.mu.Lock()
	runErr, fatal := sub.err, sub.fatalRunGated
	sub.mu.Unlock()
	if !errors.Is(runErr, context.Canceled) || fatal || delegateFatalRun(runErr) {
		t.Fatalf("stopped quota-waiting delegate: fatal-gated=%t err=%v, want a nonfatal cancellation", fatal, runErr)
	}
	jm.mu.Lock()
	_, survived := jm.running[running.JobID]
	jm.mu.Unlock()
	if !survived {
		t.Fatal("stopping the waiting delegate stopped a job it owned, as a fatal run would")
	}
	if got := streamCalls(adapter); got != 1 {
		t.Fatalf("model requests = %d, want only the one that met the limit", got)
	}
}

// A delegate can first meet the limit after its own turn succeeded: in a
// finalization drain's notification turn, which runs while a job it owns
// still runs. That turn waits in its call like any other, and the run then
// finishes without failing, with the completion delivered once.
func TestDelegateFinalizationTurnWaitsForQuota(t *testing.T) {
	limit := usageLimitError(t, "openai", quotaResetAt)
	s, clk, _, adapter := newAttentionLivelockSession(t, nil)
	limitAfterFirstAnswer(adapter, limit)
	// A job the delegate owns runs past its turn, holding the drain open.
	jm := s.jobManager
	running := &jobstore.JobRecord{JobID: "job_owned", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: true, OwnerSessionID: s.ID()}
	jm.mu.Lock()
	jm.running[running.JobID] = &runningJob{rec: running}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, running.JobID)
		jm.mu.Unlock()
	})

	// The sub drives sub.run directly on its own session, as the cancel tests
	// in subagents_test.go do.
	sub := &subagent{id: s.id, sess: s, running: true, status: SubagentRunning, done: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		sub.run(context.Background(), "do the delegated work", nil)
		close(finished)
	}()
	trip := tripwireCtx(t).Done()
	waitUntil(t, trip, "the delegate's finalization drain", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.finalizing
	})

	// The job finishes; its completion's notification turn meets the limit.
	jm.mu.Lock()
	delete(jm.running, running.JobID)
	jm.mu.Unlock()
	enqueueCompletedJobNotification(t, s, "job_owned_done")
	s.notify()
	waitUntil(t, trip, "the finalization turn waits for quota", func() bool {
		select {
		case <-finished:
			t.Fatalf("the delegate's run ended (status=%q err=%v) when its finalization turn met the usage limit", sub.status, sub.err)
		default:
		}
		return s.waitingForQuota()
	})

	limited := streamCalls(adapter)
	recoverProvider(adapter)
	reachReset(clk)
	select {
	case <-finished:
	case <-trip:
		t.Fatal("the delegate's run did not finish after the reset")
	}
	sub.mu.Lock()
	status, fatal := sub.status, sub.fatalRunGated
	sub.mu.Unlock()
	if status == SubagentFailed || fatal {
		t.Fatalf("delegate after the reset: status=%q fatal-gated=%t, want it finished without failing", status, fatal)
	}
	if got := s.jobNotificationsDelivered(); got != 1 {
		t.Fatalf("job notifications delivered = %d, want the completion delivered once", got)
	}
	if !requestsContain(adapterRequests(adapter)[limited:], "job_owned_done") {
		t.Fatal("no request after the reset carried the completion")
	}
}

// An idle delegate's notification turn, driven by its parent, waits in its
// call too. The drive's hung-turn bound does not cut a waiting turn loose:
// that would fail the delegate fatally on a limit that clears by itself. At
// the reset the turn delivers the notification and the drive ends.
func TestIdleDelegateNotificationDriveWaitsForQuota(t *testing.T) {
	// NOT parallel: this test shrinks a package-level timing var; parallel
	// siblings must never observe the mutated value.
	old := driveTurnTimeout
	driveTurnTimeout = 200 * time.Millisecond
	t.Cleanup(func() { driveTurnTimeout = old })

	adapter := &streamingAdapter{name: "openai"}
	limitAfterFirstAnswer(adapter, usageLimitError(t, "openai", quotaResetAt))
	root, clk := newQuotaDelegateRoot(t, adapter)
	trip := tripwireCtx(t).Done()
	sub, _ := startQuotaDelegate(t, root)
	waitUntil(t, trip, "the delegate's first run finishes", func() bool { return subagentDone(sub) })
	enqueueCompletedJobNotification(t, sub.sess, "job_idle_child")

	if !root.driveSubagentNotificationTurn(sub) {
		t.Fatal("the notification drive did not launch")
	}
	waitUntil(t, trip, "the drive turn waits for quota", sub.sess.waitingForQuota)
	// Several hung-turn bounds pass while the turn waits.
	time.Sleep(5 * driveTurnTimeout)
	if !sub.sess.waitingForQuota() || root.driveCounter.n.Load() != 1 {
		t.Fatalf("after the drive's bound: waiting=%t drives=%d, want the drive turn still waiting", sub.sess.waitingForQuota(), root.driveCounter.n.Load())
	}

	recoverProvider(adapter)
	reachReset(clk)
	waitUntil(t, trip, "the drive ends after the reset", func() bool { return root.driveCounter.n.Load() == 0 })
	sub.mu.Lock()
	fatal := sub.fatalRunGated
	sub.mu.Unlock()
	if fatal {
		t.Fatal("the drive turn that waited for quota gated the delegate as fatally failed")
	}
	if got := sub.sess.jobNotificationsDelivered(); got != 1 {
		t.Fatalf("job notifications delivered = %d, want the one the drive carried", got)
	}
}

// The root's own notification turn — here carrying a delegate's report —
// waits in its call the same way, and the report is delivered once at the
// reset.
func TestRootNotificationTurnWaitsForQuota(t *testing.T) {
	s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", quotaResetAt))
	serveSession(t, s)
	trip := tripwireCtx(t).Done()
	armOneRootAttention(t, s, "dlg_report", "delegate:dlg_report/delivery/1")
	run := startInput(context.Background(), s, "", EntryNotification)
	run.awaitQuotaWait(t, s, trip)
	if !slices.ContainsFunc(s.retirementInputBlockers(), func(b RetirementBlocker) bool { return b.Category == "turn" }) {
		t.Fatal("a notification turn waiting for quota does not hold the session from retiring")
	}

	recoverProvider(adapter)
	reachReset(clk)
	run.awaitEnd(t, trip, "the notification turn resumes at the reset")
	if run.err != nil {
		t.Fatalf("the notification turn after the reset: %v", run.err)
	}
	reqs := adapterRequests(adapter)
	if len(reqs) != 2 || !requestsContain(reqs[1:], "dlg_report") {
		t.Fatalf("requests = %d, want the limited one and one retry carrying the report", len(reqs))
	}
	if s.pendingRootDelegateAttention() {
		t.Fatal("the delegate report is still pending after the turn that carried it")
	}
}
