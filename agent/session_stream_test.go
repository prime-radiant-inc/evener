package agent

import (
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// newRetryEmitTestSession builds a session with no registered adapter — these
// tests call emitModelRetry directly and never reach the network, so nothing
// here needs a working client.
func newRetryEmitTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// nextModelRetryEvent reads until the next EventModelRetry, skipping the
// SessionStart envelope NewSession emits on every fresh session.
func nextModelRetryEvent(t *testing.T, sess *Session) events.ModelRetryData {
	t.Helper()
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind != events.EventModelRetry {
				continue
			}
			data, ok := ev.Data.(events.ModelRetryData)
			if !ok {
				t.Fatalf("event data = %T, want events.ModelRetryData", ev.Data)
			}
			return data
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for EventModelRetry")
			return events.ModelRetryData{}
		}
	}
}

// kata (task 10): the chip's denominator must reflect the early-stop rule that
// will actually govern this group. A group that has only failed at stream
// open (no consume-phase failure yet) can still ride the whole policy budget.
func TestEmitModelRetry_AttemptCapIsPolicyBudgetBeforeConsumePhaseFailure(t *testing.T) {
	t.Parallel()
	sess := newRetryEmitTestSession(t)
	policy := llm.RetryPolicy{MaxRetries: 10}
	req := llm.Request{Model: "gpt-5.2"}
	group := &groupRecord{}
	group.observe(attemptRecord{Phase: llm.PhaseOpen, Err: errors.New("429")}, nil)
	group.observe(attemptRecord{Phase: llm.PhaseOpen, Err: errors.New("429")}, nil)

	onRetry := sess.emitModelRetry(policy, req, group)
	onRetry(errors.New("429"), 2, time.Second)

	got := nextModelRetryEvent(t, sess)
	if want := policy.MaxRetries + 1; got.AttemptCap != want {
		t.Errorf("AttemptCap = %d, want %d (policy.MaxRetries+1, no consume-phase failure yet)", got.AttemptCap, want)
	}
}

// A single consume-phase failure (the stream opened, then died mid-flight)
// puts the group under the streak rule's early-stop bound: the chip must stop
// promising a budget the streak rule will cut short.
func TestEmitModelRetry_AttemptCapDropsToFailFastAfterOnConsumePhaseFailure(t *testing.T) {
	t.Parallel()
	sess := newRetryEmitTestSession(t)
	policy := llm.RetryPolicy{MaxRetries: 10}
	req := llm.Request{Model: "gpt-5.2"}
	group := &groupRecord{}
	group.observe(attemptRecord{Phase: llm.PhaseConsume, Err: errors.New("truncated")}, nil)

	onRetry := sess.emitModelRetry(policy, req, group)
	onRetry(errors.New("truncated"), 1, time.Second)

	got := nextModelRetryEvent(t, sess)
	if got.AttemptCap != modelRetryFailFastAfter {
		t.Errorf("AttemptCap = %d, want %d (FailFastAfter) once a consume-phase failure is recorded", got.AttemptCap, modelRetryFailFastAfter)
	}
}

// PhaseSilentStall counts toward the streak rule exactly like PhaseConsume —
// the spec's streak rule counts both phases toward the 4-streak, so a group
// that has only stalled silently is capped at 4 in reality and the chip must
// not promise more just because no byte was ever delivered.
func TestEmitModelRetry_AttemptCapDropsToFailFastAfterOnSilentStall(t *testing.T) {
	t.Parallel()
	sess := newRetryEmitTestSession(t)
	policy := llm.RetryPolicy{MaxRetries: 10}
	req := llm.Request{Model: "gpt-5.2"}
	group := &groupRecord{}
	group.observe(attemptRecord{Phase: llm.PhaseSilentStall, Err: errors.New("stalled")}, nil)

	onRetry := sess.emitModelRetry(policy, req, group)
	onRetry(errors.New("stalled"), 1, time.Second)

	got := nextModelRetryEvent(t, sess)
	if got.AttemptCap != modelRetryFailFastAfter {
		t.Errorf("AttemptCap = %d, want %d (FailFastAfter) once a silent-stall consume-phase failure is recorded", got.AttemptCap, modelRetryFailFastAfter)
	}
}

// GroupElapsedMS is wall-clock time since the group's first attempt, not a
// fixed value — a chip stuck at "0s" across successive retries would be as
// dishonest as the wrong denominator.
func TestEmitModelRetry_GroupElapsedMSIsMonotonic(t *testing.T) {
	t.Parallel()
	sess := newRetryEmitTestSession(t)
	policy := llm.RetryPolicy{MaxRetries: 10}
	req := llm.Request{Model: "gpt-5.2"}
	group := &groupRecord{}

	onRetry := sess.emitModelRetry(policy, req, group)
	onRetry(errors.New("429"), 1, time.Second)
	first := nextModelRetryEvent(t, sess)

	time.Sleep(5 * time.Millisecond)
	onRetry(errors.New("429"), 2, time.Second)
	second := nextModelRetryEvent(t, sess)

	if second.GroupElapsedMS <= first.GroupElapsedMS {
		t.Errorf("GroupElapsedMS not monotonic: first=%d second=%d", first.GroupElapsedMS, second.GroupElapsedMS)
	}
}
