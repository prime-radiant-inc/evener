package agent

import (
	"testing"

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
