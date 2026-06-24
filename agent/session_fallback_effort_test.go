package agent

import (
	"context"
	"strings"
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
// Primary claude-opus-4-6 supports "max"; the fallbacks top out at "high" (per
// the embedded catalog), so a "max" request must reach each fallback as "high".
// The cases also exercise the [1m] 1M-context suffix (stripped before catalog
// lookup) and a date-versioned ID (inherits the family's catalog levels).
func TestFallbackChain_ClampsToFallbackModelLevels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		fallback      string
		wantEffortMsg string
	}{
		{
			name:          "PlainFallbackModel",
			fallback:      "claude-opus-4-5",
			wantEffortMsg: "fallback effort = %q, want high (max clamped to the fallback model's catalog levels)",
		},
		{
			name:          "1MSuffix",
			fallback:      "claude-opus-4-5[1m]",
			wantEffortMsg: "fallback effort = %q, want high (max clamped to opus-4-5 catalog levels after stripping [1m])",
		},
		{
			name:          "DatedFallbackToFamilyLevels",
			fallback:      "claude-opus-4-5-20251101[1m]",
			wantEffortMsg: "fallback effort = %q, want high (max clamped to the opus-4-5 family levels for the dated [1m] fallback)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
					case tc.fallback:
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
				ModelFallbacks:  []string{tc.fallback},
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
				t.Fatalf(tc.wantEffortMsg, fbEffort)
			}
		})
	}
}

// An openrouter-anthropic fallback carries an upstream-qualified ID
// ("anthropic/claude-opus-4-5-20251101[1m]"). The clamp must canonicalize the
// provider namespace AND the dated/[1m] suffixes to resolve the opus-4-5 family
// levels — otherwise a "max" request reaches the 4.5 fallback unclamped.
func TestFallbackChain_ClampsQualifiedDatedFallbackToFamilyLevels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort, fbModel string
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("anthropic", 403, "primary denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openrouter-anthropic",
		Respond: func(req llm.Request) (llm.Response, error) {
			if strings.Contains(req.Model, "opus-4-6") {
				return llm.Response{}, permErr
			}
			fbInvoked = true
			fbModel = req.Model
			if req.ReasoningEffort != nil {
				fbEffort = *req.ReasoningEffort
			}
			return agenttest.FinalResponse("fallback answered"), nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, newOpenRouterAnthropicProfile("anthropic/claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"anthropic/claude-opus-4-5-20251101[1m]"},
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
		t.Fatalf("fallback effort = %q for model %q, want high (max clamped to opus-4-5 family after canonicalizing namespace + dated/[1m])", fbEffort, fbModel)
	}
}
