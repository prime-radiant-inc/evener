package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
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

func streamCalls(adapter *streamingAdapter) int {
	_, n := adapter.Counts()
	return n
}

func adapterRequests(adapter *streamingAdapter) []llm.Request {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]llm.Request(nil), adapter.requests...)
}

// A usage-limit 429 that names its reset time means every request before
// then fails the same way. Notifications that arrive in the meantime — a job
// completion, a delegate report, a watch frame — must not start turns
// against it: each one ran a doomed turn in the observed loop. They wait,
// and the session asks for one wake at the reset that carries all of them.
func TestUsageLimitHoldsNotificationsUntilRecovery(t *testing.T) {
	resetAt := time.Unix(1000, 0).Add(2 * time.Hour)
	s, clk, notifies, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", resetAt))
	serveSession(t, s)
	ctx := context.Background()

	if _, err := s.ProcessInput(ctx, "start the work", nil); err == nil {
		t.Fatal("the usage-limited turn unexpectedly succeeded")
	}
	failed := streamCalls(adapter)

	// Each arrival is followed by the wake the serve loop runs for it.
	arrivals := []struct {
		kind   string
		arrive func()
	}{
		{"job completion", func() { enqueueCompletedJobNotification(t, s, "job_quota") }},
		{"delegate report", func() { armOneRootAttention(t, s, "dlg_quota", "delegate:dlg_quota/delivery/1") }},
		{"watch frame", func() { s.enqueueJobNotificationAndNotify(watchNotification("job_watch_quota", "watched-frame-quota")) }},
	}
	for _, a := range arrivals {
		before := streamCalls(adapter)
		a.arrive()
		_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
		if got := streamCalls(adapter); got != before {
			t.Errorf("a %s while waiting for quota made %d model request(s), want none", a.kind, got-before)
		}
	}
	clk.Advance(time.Hour)
	clk.Drain()
	if got := streamCalls(adapter); got != failed {
		t.Fatalf("model requests before the reset = %d, want %d (only the failed turn)", got, failed)
	}

	recoverProvider(adapter)
	wakes := notifies.Load()
	clk.Advance(time.Hour)
	clk.Drain()
	if notifies.Load() == wakes {
		t.Fatal("the reset time passed without the session asking for a wake")
	}
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("the wake at the reset: %v", err)
	}
	if got := streamCalls(adapter) - failed; got != 1 {
		t.Fatalf("model requests after the reset = %d, want one turn carrying every held notification", got)
	}
	reqs := adapterRequests(adapter)
	last := reqs[len(reqs)-1:]
	for _, want := range []string{"job_quota", "dlg_quota", "watched-frame-quota"} {
		if !requestsContain(last, want) {
			t.Errorf("the turn at the reset did not carry the held %q notification", want)
		}
	}

	// Delivered once: another wake has nothing left to carry.
	_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
	if got := streamCalls(adapter) - failed; got != 1 {
		t.Fatalf("model requests after a second wake = %d, want the held notifications delivered once", got)
	}
}

// kickRecorder stands in for the serve loop's goal kick: it records each
// continuation prompt the session asks to run.
type kickRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (k *kickRecorder) kick(prompt string) {
	k.mu.Lock()
	k.prompts = append(k.prompts, prompt)
	k.mu.Unlock()
}

// take returns and forgets the prompts recorded so far.
func (k *kickRecorder) take() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	taken := k.prompts
	k.prompts = nil
	return taken
}

// runKicks runs each recorded continuation the way the serve loop does.
func runKicks(t *testing.T, s *Session, k *kickRecorder) {
	t.Helper()
	for _, prompt := range k.take() {
		_, _ = s.ProcessInputKind(context.Background(), prompt, nil, EntryContinuation)
	}
}

// waitingForQuotaWithGoal is a served session with an active goal whose
// first turn met a usage limit resetting two hours on.
func waitingForQuotaWithGoal(t *testing.T) (*Session, *agenttest.FakeClock, *atomic.Int64, *streamingAdapter, *kickRecorder) {
	t.Helper()
	resetAt := time.Unix(1000, 0).Add(2 * time.Hour)
	s, clk, notifies, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", resetAt))
	serveSession(t, s)
	if _, err := s.SetGoal(context.Background(), "ship the release"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	kicks := &kickRecorder{}
	s.SetKickFunc(kicks.kick)
	if _, err := s.ProcessInput(context.Background(), "start the work", nil); err == nil {
		t.Fatal("the usage-limited turn unexpectedly succeeded")
	}
	return s, clk, notifies, adapter, kicks
}

// R05 for the quota case: a usage limit with a known reset is not a reason
// to give up on the goal. It stays active through the wait, takes no turns
// against the limit, and continues once the reset passes. A goal the user
// cleared meanwhile does not come back.
func TestUsageLimitKeepsGoalWaiting(t *testing.T) {
	t.Run("continues after the reset", func(t *testing.T) {
		s, clk, _, adapter, kicks := waitingForQuotaWithGoal(t)
		if status, _, _ := s.GoalStatus(); status != string(goal.StatusActive) {
			t.Fatalf("goal status after the usage limit = %q, want it still active", status)
		}
		failed := streamCalls(adapter)
		runKicks(t, s, kicks)
		if got := streamCalls(adapter); got != failed {
			t.Fatalf("a goal continuation while waiting for quota made %d model request(s), want none", got-failed)
		}

		recoverProvider(adapter)
		clk.Advance(2 * time.Hour)
		clk.Drain()
		prompts := kicks.take()
		if len(prompts) != 1 {
			t.Fatalf("goal kicks at the reset = %d, want one", len(prompts))
		}
		if _, err := s.ProcessInputKind(context.Background(), prompts[0], nil, EntryContinuation); err != nil {
			t.Fatalf("the goal continuation at the reset: %v", err)
		}
		if got := streamCalls(adapter) - failed; got < 1 {
			t.Fatal("the goal continuation at the reset made no model request")
		}
		// The scripted provider never makes progress, so the goal runs its
		// continuations until the no-progress breaker stops it: it was driven
		// again, not blocked by the usage limit.
		if snap, _ := s.getOrCreateGoalStore().Snapshot(); snap.Iterations == 0 {
			t.Fatalf("goal after the reset = %+v, want its continuations to have run", snap)
		}
	})
	t.Run("a cleared goal does not resume", func(t *testing.T) {
		s, clk, notifies, adapter, kicks := waitingForQuotaWithGoal(t)
		kicks.take()
		if err := s.ClearGoal(); err != nil {
			t.Fatalf("ClearGoal: %v", err)
		}
		recoverProvider(adapter)
		wakes := notifies.Load()
		clk.Advance(2 * time.Hour)
		clk.Drain()
		if got := kicks.take(); len(got) != 0 {
			t.Fatalf("goal kicks at the reset after the goal was cleared = %d, want none", len(got))
		}
		if notifies.Load() != wakes {
			t.Fatal("the reset asked for a wake with nothing held")
		}
	})
}

// A model switch while waiting is the user choosing a model the spent quota
// may not limit: the session resumes at once on it, delivering the held
// notifications in one turn and then continuing the goal, and the reset
// timer no longer matters.
func TestModelSwitchWhileWaitingForQuotaResumesImmediately(t *testing.T) {
	s, clk, notifies, adapter, kicks := waitingForQuotaWithGoal(t)
	ctx := context.Background()
	kicks.take()
	enqueueCompletedJobNotification(t, s, "job_switch")
	_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
	failed := streamCalls(adapter)

	recoverProvider(adapter)
	wakes := notifies.Load()
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if notifies.Load() == wakes {
		t.Fatal("the model switch did not ask for a wake for the held notification")
	}
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("the wake after the switch: %v", err)
	}
	if got := streamCalls(adapter) - failed; got != 1 {
		t.Fatalf("model requests after the switch = %d, want one notification turn", got)
	}
	reqs := adapterRequests(adapter)
	if last := reqs[len(reqs)-1]; last.Model != "gpt-5.4" || !requestsContain(reqs[len(reqs)-1:], "job_switch") {
		t.Fatalf("the turn after the switch ran on %q, carrying the held notification: %t; want gpt-5.4 carrying it", last.Model, requestsContain(reqs[len(reqs)-1:], "job_switch"))
	}
	if got := s.jobNotificationsDelivered(); got != 1 {
		t.Fatalf("job notifications delivered = %d, want the held one exactly once", got)
	}
	prompts := kicks.take()
	if len(prompts) != 1 {
		t.Fatalf("goal kicks after the notification turn = %d, want the goal continued once", len(prompts))
	}

	// The reset timer was the old model's; reaching it later asks for nothing.
	wakes = notifies.Load()
	clk.Advance(3 * time.Hour)
	clk.Drain()
	if notifies.Load() != wakes || len(kicks.take()) != 0 {
		t.Fatal("the stale reset timer fired after the model switch ended the wait")
	}
}

// A user message while waiting tries the provider once: the user may know
// the quota came back. If it has not, the session keeps waiting and keeps
// the message, which the turn at the reset then carries. If it has, the
// wait ends and that same input delivers what was held.
func TestUserMessageWhileWaitingForQuotaRetriesOnce(t *testing.T) {
	ctx := context.Background()
	t.Run("still limited", func(t *testing.T) {
		resetAt := time.Unix(1000, 0).Add(2 * time.Hour)
		s, clk, notifies, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", resetAt))
		serveSession(t, s)
		_, _ = s.ProcessInput(ctx, "start the work", nil)
		enqueueCompletedJobNotification(t, s, "job_user_retry")
		_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
		before := streamCalls(adapter)

		if _, err := s.ProcessInput(ctx, "is the quota back yet", nil); err == nil {
			t.Fatal("the user's turn unexpectedly succeeded against the usage limit")
		}
		if got := streamCalls(adapter) - before; got != 1 {
			t.Fatalf("model requests for the user's message = %d, want exactly one try", got)
		}
		_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
		if got := streamCalls(adapter) - before; got != 1 {
			t.Fatal("a notification wake after the user's failed try reached the provider: the session stopped waiting")
		}

		recoverProvider(adapter)
		wakes := notifies.Load()
		clk.Advance(2 * time.Hour)
		clk.Drain()
		if notifies.Load() == wakes {
			t.Fatal("the reset passed without the session asking for a wake")
		}
		_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
		reqs := adapterRequests(adapter)
		last := reqs[len(reqs)-1:]
		if !requestsContain(last, "is the quota back yet") || !requestsContain(last, "job_user_retry") {
			t.Fatal("the turn at the reset did not carry both the user's kept message and the held notification")
		}
	})
	t.Run("quota came back", func(t *testing.T) {
		resetAt := time.Unix(1000, 0).Add(2 * time.Hour)
		s, _, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", resetAt))
		serveSession(t, s)
		_, _ = s.ProcessInput(ctx, "start the work", nil)
		enqueueCompletedJobNotification(t, s, "job_user_back")
		_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)

		recoverProvider(adapter)
		if _, err := s.ProcessInput(ctx, "try again now", nil); err != nil {
			t.Fatalf("the user's turn after the quota came back: %v", err)
		}
		if got := s.jobNotificationsDelivered(); got != 1 {
			t.Fatalf("job notifications delivered = %d, want the held one delivered by the user's input", got)
		}
		if s.RestingFailure() != nil {
			t.Fatal("the session still rests on the usage-limit failure after a successful turn")
		}
	})
}

// The wait is session state a client reads: the session rests Failed on the
// usage-limit failure, which says when it resumes, even after a held
// delegate report lands in the transcript. Held work does not make it read
// busy. Resuming clears it.
func TestWaitingForQuotaIsVisibleInSessionState(t *testing.T) {
	resetAt := time.Unix(1000, 0).Add(2 * time.Hour)
	s, clk, _, adapter := newAttentionLivelockSession(t, usageLimitError(t, "openai", resetAt))
	serveSession(t, s)
	ctx := context.Background()
	_, _ = s.ProcessInput(ctx, "start the work", nil)

	assertWaiting := func(when string) {
		t.Helper()
		if got := s.WireState(); got != appwire.ThreadStatusSystemError {
			t.Fatalf("WireState %s = %q, want %q", when, got, appwire.ThreadStatusSystemError)
		}
		failure := s.RestingFailure()
		if failure == nil || failure.ResumesAt != resetAt.UnixMilli() {
			t.Fatalf("RestingFailure %s = %+v, want it to resume at %d", when, failure, resetAt.UnixMilli())
		}
	}
	assertWaiting("after the usage limit")
	armOneRootAttention(t, s, "dlg_visible", "delegate:dlg_visible/delivery/1")
	_, _ = s.ProcessInputKind(ctx, "", nil, EntryNotification)
	assertWaiting("after a held delegate report")

	recoverProvider(adapter)
	clk.Advance(2 * time.Hour)
	clk.Drain()
	if failure := s.RestingFailure(); failure != nil && failure.ResumesAt != 0 {
		t.Fatalf("RestingFailure after the reset = %+v, want no resume time once the wait ended", failure)
	}
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("the wake at the reset: %v", err)
	}
	if s.RestingFailure() != nil || s.WireState() == appwire.ThreadStatusSystemError {
		t.Fatalf("after resuming: failure=%+v state=%q, want the failure cleared", s.RestingFailure(), s.WireState())
	}
}

// A usage limit that names no reset time keeps today's permanent-failure
// handling: no wait, no timer, and the goal blocks as on any provider
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
	_, _ = s.ProcessInput(context.Background(), "start the work", nil)
	if s.waitingForQuota() {
		t.Fatal("a usage limit with no reset time began a quota wait")
	}
	if status, _, _ := s.GoalStatus(); status != string(goal.StatusBlocked) {
		t.Fatalf("goal status = %q, want blocked as on any provider failure", status)
	}
}
