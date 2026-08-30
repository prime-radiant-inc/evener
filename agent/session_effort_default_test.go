package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// A reasoning-capable model with no session effort configured gets the
// default effort, clamped to its levels, so the provider never picks an
// unbounded thinking budget on its own (a gateway-fronted glm-5.3 spent 25k
// reasoning tokens on one turn when the request carried no effort).
func TestBuildModelRequest_DefaultsEffortWhenUnset(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := provider.NewOpenAIProfile("lunaroute/glm-5.3").
		WithLiveModelInfo(llm.ModelInfo{
			SupportsReasoning:     true,
			ReasoningEffortLevels: []string{"high", "max"},
		})

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "")

	if req.ReasoningEffort == nil {
		t.Fatal("ReasoningEffort = nil, want the default effort for a reasoning model")
	}
	if *req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high (medium clamped to the model's levels)", *req.ReasoningEffort)
	}
}

// resolveRequestEffort is the one rule for what effort a request carries.
func TestResolveRequestEffort(t *testing.T) {
	t.Parallel()
	levels := []string{"low", "medium", "high"}
	str := func(s string) *string { return &s }
	cases := []struct {
		name         string
		configured   string
		supports     bool
		levels       []string
		modelDefault string
		want         *string
	}{
		{"non-reasoning model gets nothing even when configured", "high", false, []string{}, "", nil},
		{"explicit none omits the field when the model has no off level", "none", true, levels, "", nil},
		{"explicit none is sent when the model lists it", "none", true, []string{"none", "low", "high"}, "", str("none")},
		{"configured effort is clamped", "xhigh", true, levels, "", str("high")},
		{"unset uses the model's stated default", "", true, levels, "high", str("high")},
		{"unset falls back to medium", "", true, levels, "", str("medium")},
		{"fallback medium is clamped to the model's levels", "", true, []string{"high", "max"}, "", str("high")},
		{"unset with unknown levels still sends medium", "", true, nil, "", str("medium")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRequestEffort(tc.configured, tc.supports, tc.levels, tc.modelDefault)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("got nil, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("got %q, want %q", *got, *tc.want)
			}
		})
	}
}

// An explicit off from the user is never overridden by the default.
func TestBuildModelRequest_ExplicitNoneOmitsEffort(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := provider.NewOpenAIProfile("lunaroute/glm-5.3").
		WithLiveModelInfo(llm.ModelInfo{SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "medium", "high"}})

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "none")

	if req.ReasoningEffort != nil {
		t.Fatalf("ReasoningEffort = %q, want nil for an explicit none", *req.ReasoningEffort)
	}
}

// A model whose data states its default runs at that default, not at medium.
func TestBuildModelRequest_UsesModelDefaultEffort(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := provider.NewOpenAIProfile("gateway-model").
		WithLiveModelInfo(llm.ModelInfo{SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "medium", "high"}, DefaultReasoningEffort: "high"})

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "")

	if req.ReasoningEffort == nil || *req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %v, want high (model default)", req.ReasoningEffort)
	}
}

// The default never reaches a model the user declared non-reasoning.
func TestBuildModelRequest_NoDefaultEffortWhenProfileDoesNotSupportReasoning(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := nonReasoningProfile(t, "tiny-chat")

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "")

	if req.ReasoningEffort != nil {
		t.Fatalf("ReasoningEffort = %q, want nil (profile declared non-reasoning)", *req.ReasoningEffort)
	}
}

// The fallback chain applies the same rule as the primary path: with no
// effort configured, a reasoning-capable fallback model gets the default,
// clamped to its own levels, rather than a reasoning-less request.
func TestFallbackChain_DefaultsEffortWhenUnset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("anthropic", 403, "primary denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openrouter-anthropic",
		Respond: func(req llm.Request) (llm.Response, error) {
			if strings.Contains(req.Model, "opus-4-6") {
				return llm.Response{}, permErr
			}
			fbInvoked = true
			if req.ReasoningEffort != nil {
				fbEffort = *req.ReasoningEffort
			}
			return agenttest.FinalResponse("fallback answered"), nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, newOpenRouterAnthropicProfile("anthropic/claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"anthropic/claude-opus-4-5-20251101"},
		testOnly:       testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !fbInvoked {
		t.Fatal("fallback model was not invoked")
	}
	if fbEffort != "medium" {
		t.Fatalf("fallback effort = %q, want medium (default, within opus-4-5's levels)", fbEffort)
	}
}
