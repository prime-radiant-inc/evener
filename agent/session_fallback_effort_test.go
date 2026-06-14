package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

// When the primary fails over to a same-provider fallback whose model supports
// FEWER effort levels, the fallback request must clamp to the FALLBACK model's
// levels — not the primary's. WithModel keeps the primary's levels for anthropic,
// so the clamp consults the catalog for the fallback model.
//
// Primary claude-opus-4-6 supports "max"; fallback claude-opus-4-5 tops out at
// "high" (per the embedded catalog). A "max" request must reach the fallback as
// "high".
func TestFallbackChain_ClampsToFallbackModelLevels(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("anthropic", 403, "primary denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "anthropic",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "claude-opus-4-6":
				return llm.Response{}, permErr
			case "claude-opus-4-5":
				fbInvoked = true
				if req.ReasoningEffort != nil {
					fbEffort = *req.ReasoningEffort
				}
				return agenttest.FinalResponse("fallback answered"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"claude-opus-4-5"},
		ReasoningEffort: "max",
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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v (fallback should succeed)", err)
	}
	if !fbInvoked {
		t.Fatal("fallback model was not invoked")
	}
	if fbEffort != "high" {
		t.Fatalf("fallback effort = %q, want high (max clamped to the fallback model's catalog levels)", fbEffort)
	}
}

// Same as above, but the fallback carries the Anthropic "[1m]" 1M-context suffix,
// which must be stripped before the catalog lookup so the clamp still finds the
// fallback model's levels.
func TestFallbackChain_ClampsToFallbackModelLevels_1MSuffix(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("anthropic", 403, "primary denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "anthropic",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "claude-opus-4-6":
				return llm.Response{}, permErr
			case "claude-opus-4-5[1m]":
				fbInvoked = true
				if req.ReasoningEffort != nil {
					fbEffort = *req.ReasoningEffort
				}
				return agenttest.FinalResponse("fallback answered"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"claude-opus-4-5[1m]"},
		ReasoningEffort: "max",
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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !fbInvoked {
		t.Fatal("fallback model was not invoked")
	}
	if fbEffort != "high" {
		t.Fatalf("fallback effort = %q, want high (max clamped to opus-4-5 catalog levels after stripping [1m])", fbEffort)
	}
}

// A date-versioned Anthropic fallback ID (claude-opus-4-5-20251101[1m]) must
// still clamp to the family's catalog levels: the dated snapshot carries no
// effort metadata of its own, so it inherits claude-opus-4-5's levels. Without
// that inheritance the lookup misses and a "max" request reaches the 4.5
// fallback unclamped.
func TestFallbackChain_ClampsDatedFallbackToFamilyLevels(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("anthropic", 403, "primary denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "anthropic",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "claude-opus-4-6":
				return llm.Response{}, permErr
			case "claude-opus-4-5-20251101[1m]":
				fbInvoked = true
				if req.ReasoningEffort != nil {
					fbEffort = *req.ReasoningEffort
				}
				return agenttest.FinalResponse("fallback answered"), nil
			}
			t.Errorf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"claude-opus-4-5-20251101[1m]"},
		ReasoningEffort: "max",
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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !fbInvoked {
		t.Fatal("fallback model was not invoked")
	}
	if fbEffort != "high" {
		t.Fatalf("fallback effort = %q, want high (max clamped to the opus-4-5 family levels for the dated [1m] fallback)", fbEffort)
	}
}
