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
