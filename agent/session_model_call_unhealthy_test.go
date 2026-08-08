package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// scriptedStreamAdapter answers Stream per model: either a rejection at open or
// a scripted event sequence. It records every model it was asked for, in order,
// so a fallback-chain test can assert which groups ran and how many attempts
// each burned.
type scriptedStreamAdapter struct {
	provider string
	openErr  map[string]error
	script   map[string]func(*llm.ChanStream)

	mu     sync.Mutex
	models []string
}

func (a *scriptedStreamAdapter) Name() string { return a.provider }

func (a *scriptedStreamAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_, _ = ctx, req
	return llm.Response{}, fmt.Errorf("scriptedStreamAdapter: Complete called for %q, want the streaming path", req.Model)
}

func (a *scriptedStreamAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	openErr := a.openErr[req.Model]
	script := a.script[req.Model]
	a.mu.Unlock()
	if openErr != nil {
		return nil, openErr
	}
	if script == nil {
		return nil, fmt.Errorf("scriptedStreamAdapter: no script for model %q", req.Model)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	_ = streamCtx
	st := llm.NewChanStream(cancel)
	go func() {
		defer st.CloseSend()
		script(st)
	}()
	return st, nil
}

// Models returns the models streamed, in call order.
func (a *scriptedStreamAdapter) Models() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.models...)
}

// streamThenFail scripts one attempt that emits text and then dies mid-stream —
// the consume-phase shape the early-stop rules count.
func streamThenFail(text string, err error) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: text})
		st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: err})
	}
}

// unhealthyChainSession builds a session whose fallback chain is
// primary → fallback-b → fallback-c against the given scripted adapter, with a
// retry budget large enough that the early-stop rules, not the budget, decide
// when a group gives up.
func unhealthyChainSession(t *testing.T, a *scriptedStreamAdapter) *Session {
	t.Helper()
	policy := llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	sess := newSession(t,
		withAdapter(a),
		withProfile(NewOpenAIProfile("primary")),
		withConfig(SessionConfig{
			LLMRetryPolicy: &policy,
			LLMSleep:       func(context.Context, time.Duration) error { return nil },
			ModelFallbacks: []string{"fallback-b", "fallback-c"},
		}),
	)
	drainSessionEvents(sess)
	return sess
}

func unhealthyChainRequest() llm.Request {
	return llm.Request{
		Provider: "openai",
		Model:    "primary",
		Messages: []llm.Message{llm.User("hi")},
	}
}

// TestFallbackChain_MidChainProviderUnhealthyAbortsWalk: the primary rejects
// permanently so the chain walks, the first fallback burns its consume-phase
// streak and returns llm.ProviderUnhealthyError, and that verdict ends the
// round — the second fallback is never tried and last-error-wins does not
// replace the verdict with a later group's failure. An unhealthy verdict
// indicts the provider's transport, which every same-provider fallback shares.
func TestFallbackChain_MidChainProviderUnhealthyAbortsWalk(t *testing.T) {
	permErr := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)
	midStreamErr := llm.ErrorFromHTTPStatus("openai", 503, "upstream stream died", nil, nil)
	a := &scriptedStreamAdapter{
		provider: "openai",
		openErr: map[string]error{
			"primary":    permErr,
			"fallback-c": llm.ErrorFromHTTPStatus("openai", 403, "fallback-c denied", nil, nil),
		},
		script: map[string]func(*llm.ChanStream){
			"fallback-b": streamThenFail("partial draft", midStreamErr),
		},
	}
	sess := unhealthyChainSession(t, a)

	_, _, _, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), unhealthyChainRequest(), "", 1)

	var pu *llm.ProviderUnhealthyError
	if !errors.As(err, &pu) {
		t.Fatalf("terminal error = %v (%T), want *llm.ProviderUnhealthyError from the fallback-b group", err, err)
	}
	got := a.Models()
	// The primary's permanent rejection is not retried; fallback-b burns the
	// four-attempt consume-phase streak; fallback-c is never reached.
	want := []string{"primary", "fallback-b", "fallback-b", "fallback-b", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("streamed models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("streamed models = %v, want %v", got, want)
		}
	}
}

// TestModelFallbackEligible_ProviderUnhealthy pins the dedicated
// non-eligibility arm. The verdict wraps its last attempt error, so deriving
// the class through llm.Classify would walk into that wrapped error: a
// permanent-class one reports the chain eligible (the case that fails without
// the arm), and a retryable-class one only happens to report non-eligible.
func TestModelFallbackEligible_ProviderUnhealthy(t *testing.T) {
	cases := []struct {
		name    string
		lastErr error
	}{
		{"WrappedPermanent", llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)},
		{"WrappedRetryable", llm.ErrorFromHTTPStatus("openai", 503, "upstream stream died", nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 2 * time.Minute, LastErr: tc.lastErr}
			if modelFallbackEligible(err, llm.DefaultRetryPolicy()) {
				t.Fatalf("modelFallbackEligible(%v) = true, want false", err)
			}
		})
	}
}
