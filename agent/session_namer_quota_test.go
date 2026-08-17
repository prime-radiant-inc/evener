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

// namerIdle reports whether no naming call is still in flight.
func (h *namerQuotaHarness) namerIdle() bool {
	h.sess.mu.Lock()
	defer h.sess.mu.Unlock()
	return !h.sess.naming.promptPending
}

// awaitSettledFailures blocks until want failure advisories have been written
// AND no naming call is still in flight.
//
// Both conditions are load-bearing, and waiting on the advisory alone is a race.
// nameSessionFromText writes the advisory and returns; only then does the
// launcher goroutine call clearPromptNamePendingAfterAttempt
// (session_namer.go:232-233). Between those two steps the advisory is already
// visible on disk while promptPending is still set, so an advisory-only wait can
// resume inside that window -- and the next launch would then be suppressed by
// the in-flight pending guard rather than by the quota state.
//
// That is a different mechanism, and it makes these tests measure the wrong
// thing in both directions: the quota tests would pass even without the
// suppression fix, and the transient test would fail even with it. Observed
// once as exactly that failure under -tags serffuzz. It is load-dependent, so
// the proof of record is a mutation: inject a delay between those two lines and
// an advisory-only wait fails while this one holds.
//
// The suppression decision itself is recorded before the advisory is written, so
// once both conditions hold the session has fully settled.
func (h *namerQuotaHarness) awaitSettledFailures(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h.namerFailureAdvisories(t) >= want && h.namerIdle() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d settled namer failures; advisories=%d idle=%v provider calls=%d",
		want, h.namerFailureAdvisories(t), h.namerIdle(), h.callCount())
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
	h.awaitSettledFailures(t, 1)

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
	h.awaitSettledFailures(t, 1)

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
	h.awaitSettledFailures(t, 1)
	afterFirst := h.callCount()

	h.sess.launchInitialPromptNamer(context.Background(), "second task")
	h.awaitSettledFailures(t, 2)
	h.sess.Close()

	if got := h.callCount(); got <= afterFirst {
		t.Errorf("provider calls = %d, want more than %d: a transient rate limit must not disable the namer",
			got, afterFirst)
	}
}
