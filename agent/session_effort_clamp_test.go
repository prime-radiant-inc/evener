package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
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

// nonReasoningProfile builds an openai-compatible profile for a model
// declared reasoning = false in providers.toml, the shape newOpenAICompatProfile
// produces: SupportsReasoning() == false and an empty (non-nil) effort list.
func nonReasoningProfile(t *testing.T, model string) *provider.Profile {
	t.Helper()
	reasoningOff := false
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "lunaroute",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			model: {Reasoning: &reasoningOff},
		},
	}}}
	p, err := provider.ResolveProfileFromConfig(cfg, "lunaroute/"+model)
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}
	if p.SupportsReasoning() {
		t.Fatalf("fixture profile SupportsReasoning = true, want false")
	}
	return p
}

// A model explicitly declared non-reasoning must never receive
// reasoning_effort on the wire, even when a session effort is configured —
// ClampReasoningEffort passes the value through unchanged against an empty
// supported-levels list, so without the SupportsReasoning guard the request
// leaks reasoning_effort to a model that 400s on it.
func TestBuildModelRequest_OmitsEffortWhenProfileDoesNotSupportReasoning(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := nonReasoningProfile(t, "tiny-chat")

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "high")

	if req.ReasoningEffort != nil {
		t.Fatalf("ReasoningEffort = %q, want nil (profile declared non-reasoning)", *req.ReasoningEffort)
	}
}

// The vision side-channel (describeImage) builds its request manually rather
// than via buildModelRequest, so it needs its own SupportsReasoning guard —
// covered separately here since it has its own bug-prone code path.
func TestDescribeImage_OmitsEffortWhenProfileDoesNotSupportReasoning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var sawEffort bool
	adapter := &fakeAdapter{
		name: "lunaroute",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				sawEffort = req.ReasoningEffort != nil
				return llm.Response{Message: llm.Assistant("an image of a cat")}
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)

	profile := nonReasoningProfile(t, "tiny-chat")
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	desc := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("fake-png-bytes"),
		ImageMediaType: "image/png",
		ImagePurpose:   "what is in this image",
	})
	if desc == "" {
		t.Fatal("describeImage returned empty description")
	}
	if sawEffort {
		t.Fatal("vision request set ReasoningEffort, want nil (profile declared non-reasoning)")
	}
}

// When the fallback chain switches to a same-provider model explicitly
// declared non-reasoning, the fallback request must not carry
// reasoning_effort either — the guard on the primary path in
// buildModelRequest does not cover the separately-constructed fallback
// request in callModelWithFallback. Cross-provider fallbacks are rejected by
// validateModelFallbacks, so both the primary ("glm-5.2-nvfp4") and the
// fallback ("tiny-chat") live on the same "lunaroute" instance.
func TestFallbackChain_OmitsEffortWhenFallbackProfileDoesNotSupportReasoning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var fbSawEffort bool
	fbInvoked := false
	permErr := llm.ErrorFromHTTPStatus("lunaroute", 403, "primary denied", nil, nil)

	adapter := &agenttest.ModelTrackingAdapter{
		Provider: "lunaroute",
		Respond: func(req llm.Request) (llm.Response, error) {
			if req.Model == "tiny-chat" {
				fbInvoked = true
				fbSawEffort = req.ReasoningEffort != nil
				return agenttest.FinalResponse("fallback answered"), nil
			}
			return llm.Response{}, permErr
		},
	}
	c.Register(adapter)

	reasoningOff := false
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "lunaroute",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"tiny-chat": {Reasoning: &reasoningOff},
		},
	}}}
	primary, err := provider.ResolveProfileFromConfig(cfg, "lunaroute/glm-5.2-nvfp4")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, primary, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"tiny-chat"},
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
	if fbSawEffort {
		t.Fatal("fallback request set ReasoningEffort, want nil (fallback profile declared non-reasoning)")
	}
}
