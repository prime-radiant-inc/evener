package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

// kata cxw8: when the primary model returns a Permanent class error
// (403/404/...) and ModelFallbacks is configured, the session must try each
// fallback in order before giving up.

// TestFallbackChain_PermanentErrorTriesNextModel: primary returns 403, the
// first fallback succeeds, the second fallback is never called.
func TestFallbackChain_PermanentErrorTriesNextModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	permErr := llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, permErr
			case "fallback-b":
				return agenttest.FinalResponse("fallback B answered"), nil
			case "fallback-c":
				t.Errorf("fallback-c must not be invoked once fallback-b succeeds")
				return agenttest.FinalResponse("c"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b", "fallback-c"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: got error %v, want nil (fallback should succeed)", err)
	}
	if !strings.Contains(out, "fallback B answered") {
		t.Errorf("output: got %q, want substring 'fallback B answered'", out)
	}

	got := f.Models()
	want := []string{"primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("attempted models: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attempt %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestFallbackChain_EndpointFallbackErrorTriesNextModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	endpointFallbackErr := llm.ErrorFromHTTPStatus("openai", 404, "responses.create(stream) failed: model not found", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, endpointFallbackErr
			case "fallback-b":
				return agenttest.FinalResponse("fallback B answered"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: got error %v, want nil (fallback should succeed)", err)
	}
	if !strings.Contains(out, "fallback B answered") {
		t.Errorf("output: got %q, want substring 'fallback B answered'", out)
	}

	got := f.Models()
	want := []string{"primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("attempted models: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attempt %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestFallbackChain_ExhaustionReturnsLastError: primary + all fallbacks return
// 403; the error returned to the caller is the LAST attempt's error.
func TestFallbackChain_ExhaustionReturnsLastError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	errPrimary := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)
	errB := llm.ErrorFromHTTPStatus("openai", 403, "fallback-b denied", nil, nil)
	errC := llm.ErrorFromHTTPStatus("openai", 403, "fallback-c denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, errPrimary
			case "fallback-b":
				return llm.Response{}, errB
			case "fallback-c":
				return llm.Response{}, errC
			}
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b", "fallback-c"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatal("ProcessInput: got nil error, want last-attempt error")
	}
	if !strings.Contains(err.Error(), "fallback-c denied") {
		t.Errorf("error should carry the LAST attempt (fallback-c) message; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "primary denied") {
		t.Errorf("error should not be from the primary attempt; got %q", err.Error())
	}

	got := f.Models()
	want := []string{"primary", "fallback-b", "fallback-c"}
	if len(got) != len(want) {
		t.Fatalf("attempted models: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attempt %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestFallbackChain_RetryableSkipsFallback: primary returns 429 (Retryable).
// The retry budget is burned on the primary; fallbacks are never touched.
func TestFallbackChain_RetryableSkipsFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	rateLimit := llm.ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			if req.Model == "fallback-b" {
				t.Errorf("fallback-b must not be invoked when primary returns a Retryable error")
			}
			return llm.Response{}, rateLimit
		},
	}
	c.Register(f)

	// Burn budget quickly: 1 retry = 2 primary attempts total, no sleeps.
	policy := llm.RetryPolicy{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	sleep := func(ctx context.Context, d time.Duration) error { return nil }
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       sleep,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("ProcessInput: got nil error, want retry-exhausted error")
	}

	got := f.Models()
	// All attempts should be against "primary" — fallback-b is never visited.
	for i, m := range got {
		if m != "primary" {
			t.Errorf("attempt %d: got model %q, want all attempts on 'primary'", i, m)
		}
	}
	if len(got) < 2 {
		t.Errorf("primary should have been retried at least twice (got %d attempts)", len(got))
	}
}

func TestFallbackChain_RejectsCrossProviderFallbacks(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	openaiAdapter := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			t.Errorf("adapter should not be called for invalid cross-provider fallback config")
			return llm.Response{}, nil
		},
	}
	c.Register(openaiAdapter)

	policy := llm.RetryPolicy{MaxRetries: 0}
	_, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"anthropic/claude-test", "fallback-b"},
	})
	if err == nil {
		t.Fatal("NewSession succeeded with cross-provider fallback, want error")
	}
	if !strings.Contains(err.Error(), "cross-provider fallbacks are not supported") {
		t.Fatalf("error=%v, want cross-provider rejection", err)
	}
}

// TestFallbackChain_EmptyFallbacksNoEffect: primary returns 403, fallbacks
// empty — behavior matches today: single attempt, error returned.
func TestFallbackChain_EmptyFallbacksNoEffect(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	permErr := llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, permErr
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("primary"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		LLMRetryPolicy: &policy,
		// ModelFallbacks left nil — empty chain.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("ProcessInput: got nil error, want 403")
	}

	got := f.Models()
	if len(got) != 1 || got[0] != "primary" {
		t.Errorf("attempted models: got %v, want exactly [primary]", got)
	}
}
