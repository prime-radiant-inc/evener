package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/llm"
)

// kata cxw8: when the primary model returns a Permanent class error
// (403/404/...) and ModelFallbacks is configured, the session must try each
// fallback in order before giving up.

// modelTrackingAdapter records every model passed in req.Model and replies
// with a per-model response factory. Useful for verifying the literal order
// of fallback attempts.
type modelTrackingAdapter struct {
	name string

	mu       sync.Mutex
	models   []string
	requests []llm.Request
	respond  func(req llm.Request) (llm.Response, error)
}

func (a *modelTrackingAdapter) Name() string { return a.name }

func (a *modelTrackingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	resp, err := a.respond(req)
	if err != nil {
		return resp, err
	}
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *modelTrackingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *modelTrackingAdapter) Models() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.models...)
}

func (a *modelTrackingAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

// TestFallbackChain_PermanentErrorTriesNextModel: primary returns 403, the
// first fallback succeeds, the second fallback is never called.
func TestFallbackChain_PermanentErrorTriesNextModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	permErr := llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil)

	f := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, permErr
			case "fallback-b":
				return finalResponse("fallback B answered"), nil
			case "fallback-c":
				t.Errorf("fallback-c must not be invoked once fallback-b succeeds")
				return finalResponse("c"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), NewLocalExecutionEnvironment(dir), SessionConfig{
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

// TestFallbackChain_ExhaustionReturnsLastError: primary + all fallbacks return
// 403; the error returned to the caller is the LAST attempt's error.
func TestFallbackChain_ExhaustionReturnsLastError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	errPrimary := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)
	errB := llm.ErrorFromHTTPStatus("openai", 403, "fallback-b denied", nil, nil)
	errC := llm.ErrorFromHTTPStatus("openai", 403, "fallback-c denied", nil, nil)

	f := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
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
	sess, err := NewSession(c, NewOpenAIProfile("primary"), NewLocalExecutionEnvironment(dir), SessionConfig{
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

	f := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
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
	sess, err := NewSession(c, NewOpenAIProfile("primary"), NewLocalExecutionEnvironment(dir), SessionConfig{
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

func TestFallbackChain_TriesCrossProviderFallbacks(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	permErr := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)

	openaiAdapter := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, permErr
			case "fallback-b":
				t.Errorf("same-provider fallback must not be invoked once cross-provider fallback succeeds")
			}
			return llm.Response{}, nil
		},
	}
	anthropicAdapter := &modelTrackingAdapter{
		name: "anthropic",
		respond: func(req llm.Request) (llm.Response, error) {
			if req.Model != "claude-test" {
				t.Errorf("anthropic fallback model = %q, want claude-test", req.Model)
			}
			if req.Provider != "anthropic" {
				t.Errorf("anthropic fallback provider = %q, want anthropic", req.Provider)
			}
			return finalResponse("cross-provider fallback answered"), nil
		},
	}
	c.Register(openaiAdapter)
	c.Register(anthropicAdapter)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"anthropic/claude-test", "fallback-b"},
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
		t.Fatalf("ProcessInput: got error %v, want nil (cross-provider fallback should succeed)", err)
	}
	if !strings.Contains(out, "cross-provider fallback answered") {
		t.Errorf("output: got %q, want cross-provider fallback answer", out)
	}

	if got := openaiAdapter.Models(); len(got) != 1 || got[0] != "primary" {
		t.Fatalf("openai attempted models: got %v want [primary]", got)
	}
	if got := anthropicAdapter.Models(); len(got) != 1 || got[0] != "claude-test" {
		t.Fatalf("anthropic attempted models: got %v want [claude-test]", got)
	}
	reqs := anthropicAdapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("anthropic requests: got %d want 1", len(reqs))
	}
	if _, ok := reqs[0].ProviderOptions["anthropic"]; !ok {
		t.Fatalf("cross-provider fallback did not use anthropic provider options: %#v", reqs[0].ProviderOptions)
	}
}

// TestFallbackChain_EmptyFallbacksNoEffect: primary returns 403, fallbacks
// empty — behavior matches today: single attempt, error returned.
func TestFallbackChain_EmptyFallbacksNoEffect(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	permErr := llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)

	f := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, permErr
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), NewLocalExecutionEnvironment(dir), SessionConfig{
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

// TestFallbackChain_ConfigPlumbing: ModelFallbacks survives the wire
// (appwire.LaunchConfigLayer ⇄ launchconfig.Layer) and merges correctly
// across layers (launch overrides global).
func TestFallbackChain_ConfigPlumbing(t *testing.T) {
	// Wire → internal.
	wireLayer := appwire.LaunchConfigLayer{
		ModelFallbacks: []string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"},
	}
	internal := launchconfig.FromWire(wireLayer)
	if got, want := internal.ModelFallbacks, wireLayer.ModelFallbacks; !equalStrings(got, want) {
		t.Errorf("FromWire ModelFallbacks: got %v want %v", got, want)
	}

	// Internal → wire roundtrip.
	roundtrip := launchconfig.ToWire(internal)
	if got, want := roundtrip.ModelFallbacks, wireLayer.ModelFallbacks; !equalStrings(got, want) {
		t.Errorf("ToWire ModelFallbacks: got %v want %v", got, want)
	}

	// Verify the JSON tag is snake_case (project convention for launch
	// config-adjacent surfaces; appwire is camelCase per codex requirement).
	// The appwire side uses camelCase: encode and check the key.
	enc, err := json.Marshal(wireLayer)
	if err != nil {
		t.Fatalf("marshal appwire: %v", err)
	}
	if !strings.Contains(string(enc), `"modelFallbacks"`) {
		t.Errorf("appwire JSON tag for ModelFallbacks: expected camelCase 'modelFallbacks', got: %s", enc)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
