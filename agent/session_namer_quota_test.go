package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// namerQuotaHarness is a session whose namer always fails, with a tally of how
// many times the provider was actually reached.
type namerQuotaHarness struct {
	sess *Session
	dir  string

	mu    sync.Mutex
	calls int
}

func (h *namerQuotaHarness) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// newNamerQuotaHarness builds a session whose cheap-model namer client always
// returns namerErr. LLMSleep is a no-op so a retryable failure does not burn
// GenerateObject's real 1+2+4+8s backoff as wall time.
func newNamerQuotaHarness(t *testing.T, namerErr error) *namerQuotaHarness {
	t.Helper()
	dir := t.TempDir()

	h := &namerQuotaHarness{dir: dir}
	namerClient := llm.NewClient()
	namerClient.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		FaultResponder: func(llm.Request) error {
			h.mu.Lock()
			h.calls++
			h.mu.Unlock()
			return namerErr
		},
		Responder: func(llm.Request) llm.Response {
			t.Error("namer Responder reached; FaultResponder should have failed the call")
			return llm.Response{}
		},
	})

	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("ok")}
	}})

	cfg := SessionConfig{StateDir: dir, LLMSleep: func(context.Context, time.Duration) error { return nil }}
	cfg.testOnly.namerClient = namerClient
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	h.sess = sess
	return h
}

// namerFailureAdvisories counts the session log's session_namer failure entries.
func (h *namerQuotaHarness) namerFailureAdvisories(t *testing.T) int {
	t.Helper()
	log := mustNewSessionLog(t, filepath.Join(h.dir, sessionsSubdir, h.sess.ID()+".log.jsonl"))
	count := 0
	for _, entry := range log.Entries() {
		if entry.Action == "session_namer" && entry.Outcome == "failure" {
			count++
		}
	}
	return count
}

// awaitFailedAttempts blocks until want failure advisories have been written.
//
// The advisory is appended AFTER the suppression decision is recorded (see
// nameSessionFromText), so observing it means the session has finished
// reacting to the failure. That ordering is what lets these tests launch the
// next attempt without racing the previous one -- and it matters: without the
// wait, a second launch would be suppressed by the in-flight promptPending
// guard instead of by the quota state, and the test would pass green today
// for entirely the wrong reason.
func (h *namerQuotaHarness) awaitFailedAttempts(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.namerFailureAdvisories(t) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d namer failure advisories; got %d after %d provider calls",
		want, h.namerFailureAdvisories(t), h.callCount())
}

// quotaExhausted429 is the error a provider returns when the plan allowance is
// spent -- the condition kata bmgz observed, whose reset was days away.
func quotaExhausted429() error {
	raw := map[string]any{
		"error": map[string]any{
			"code":    "usage_limit_reached",
			"message": "The usage limit has been reached",
		},
	}
	return llm.ErrorFromHTTPStatus("openai", http.StatusTooManyRequests, "", raw, nil)
}

// transientRateLimit429 is an ordinary "slow down" 429: same status code, and
// it clears in seconds rather than days.
func transientRateLimit429() error {
	raw := map[string]any{
		"error": map[string]any{"code": "rate_limit_exceeded", "message": "Rate limit reached"},
	}
	return llm.ErrorFromHTTPStatus("openai", http.StatusTooManyRequests, "", raw, nil)
}

// TestSessionNamerStopsAfterQuotaExhaustionOnPromptPath is the kata's core stop
// condition. The namer's guard resets after every failure, so before this fix a
// spent allowance was re-dispatched on every user turn for the life of the
// session -- each one a wasted round trip plus a failure advisory.
func TestSessionNamerStopsAfterQuotaExhaustionOnPromptPath(t *testing.T) {
	t.Parallel()
	h := newNamerQuotaHarness(t, quotaExhausted429())

	h.sess.launchInitialPromptNamer(context.Background(), "first task")
	h.awaitFailedAttempts(t, 1)

	// Two more turns, each launched only after the previous attempt settled.
	h.sess.launchInitialPromptNamer(context.Background(), "second task")
	h.sess.launchInitialPromptNamer(context.Background(), "third task")
	h.sess.Close()

	if got := h.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1: a spent allowance must not be re-dispatched every turn", got)
	}
	if got := h.namerFailureAdvisories(t); got != 1 {
		t.Errorf("failure advisories = %d, want 1: suppressed turns must not add session-log noise", got)
	}
}

// TestSessionNamerStopsAfterQuotaExhaustionOnCompactionPath covers the second
// launch path. Both paths gate on the same availability predicate, and this
// test is what keeps that true for the compaction one.
func TestSessionNamerStopsAfterQuotaExhaustionOnCompactionPath(t *testing.T) {
	t.Parallel()
	h := newNamerQuotaHarness(t, quotaExhausted429())

	turn := schema.Turn{Kind: schema.TurnSummary, Message: llm.Assistant("a compaction summary")}
	h.sess.launchCompactionNamer(context.Background(), turn)
	h.awaitFailedAttempts(t, 1)

	h.sess.launchCompactionNamer(context.Background(), turn)
	h.sess.launchCompactionNamer(context.Background(), turn)
	h.sess.Close()

	if got := h.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1: compaction naming must stop on a spent allowance too", got)
	}
}

// TestSessionNamerKeepsRetryingAfterTransientRateLimit is the other half of the
// distinction the kata asks for. An ordinary rate limit clears in seconds, so
// disabling the namer for the whole session on one would throw away every later
// chance to name the session.
//
// Its mutation is suppress-on-any-error rather than deletion of the guard --
// that is the plausible wrong implementation, and it is what this test kills.
func TestSessionNamerKeepsRetryingAfterTransientRateLimit(t *testing.T) {
	t.Parallel()
	h := newNamerQuotaHarness(t, transientRateLimit429())

	h.sess.launchInitialPromptNamer(context.Background(), "first task")
	h.awaitFailedAttempts(t, 1)
	afterFirst := h.callCount()

	h.sess.launchInitialPromptNamer(context.Background(), "second task")
	h.awaitFailedAttempts(t, 2)
	h.sess.Close()

	if got := h.callCount(); got <= afterFirst {
		t.Errorf("provider calls = %d, want more than %d: a transient rate limit must not disable the namer",
			got, afterFirst)
	}
}
