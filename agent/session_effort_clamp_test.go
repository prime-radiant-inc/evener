package agent

import (
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// buildModelRequest must clamp the requested reasoning effort to the active
// model's supported levels, so loop-detector escalation / flag / UI values that
// exceed what the model accepts (e.g. "xhigh" to a model topping out at "high")
// don't reach the provider and 400.
func TestBuildModelRequest_ClampsEffortToProfileLevels(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := provider.NewOpenAIProfile("kimi-for-coding").
		WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"minimal", "low", "medium", "high"}})

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "xhigh")

	if req.ReasoningEffort == nil {
		t.Fatal("ReasoningEffort is nil, want clamped value")
	}
	if *req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high (clamped from xhigh)", *req.ReasoningEffort)
	}
}

func TestBuildModelRequest_KeepsSupportedEffort(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := provider.NewOpenAIProfile("m").
		WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"low", "medium", "high"}})
	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "medium")
	if req.ReasoningEffort == nil || *req.ReasoningEffort != "medium" {
		t.Fatalf("ReasoningEffort = %v, want medium (supported, unchanged)", req.ReasoningEffort)
	}
}
