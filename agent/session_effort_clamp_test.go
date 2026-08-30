package agent

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// buildModelRequest must clamp the requested reasoning effort to the active
// model's supported levels, so loop-detector escalation / flag / UI values that
// exceed what the model accepts (e.g. "xhigh" to a model topping out at "high")
// don't reach the provider and 400.
func TestBuildModelRequest_ClampsEffortToProfileLevels(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := withEffortLevels(provider.NewOpenAIProfile("kimi-for-coding"), "minimal", "low", "medium", "high")

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
	profile := withEffortLevels(provider.NewOpenAIProfile("m"), "low", "medium", "high")
	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "medium")
	if req.ReasoningEffort == nil || *req.ReasoningEffort != "medium" {
		t.Fatalf("ReasoningEffort = %v, want medium (supported, unchanged)", req.ReasoningEffort)
	}
}

// lunarouteInstance is a chat-completions gateway named "lunaroute" carrying
// the given per-model user rows — the providers.toml [providers.x.models.y]
// table, as the registry sees it.
func lunarouteInstance(models map[string]registry.Model) map[string]registry.Provider {
	return map[string]registry.Provider{"lunaroute": {
		Base: "openai-compatible", APIKey: "test",
		Transport: registry.Transport{BaseURL: "https://lunaroute.example.com/v1"},
		Models:    models,
	}}
}

// nonReasoningProfile builds a profile for a model the user declared
// reasoning = false: SupportsReasoning() == false and no effort ladder.
func nonReasoningProfile(t *testing.T, model string) *provider.Profile {
	t.Helper()
	p := resolveTestProfile("lunaroute", lunarouteInstance(map[string]registry.Model{
		model: {Caps: registry.Caps{Reasoning: new(false)}},
	}), model)
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

// alwaysOnThinkingProfile is a mandatory-reasoning row: thinking_always_on
// with a ladder of its own.
func alwaysOnThinkingProfile() *provider.Profile {
	p := provider.NewOpenAIProfile("stealth/ox-alpha")
	res := p.Resolved()
	res.Caps.Reasoning = new(true)
	res.Caps.ThinkingAlwaysOn = new(true)
	res.Caps.EffortValues = []string{"low", "high", "max"}
	return p.WithResolved(res)
}

// TestBuildModelRequest_ThinkingAlwaysOn_InjectsNoEffort pins spec §7.4: a
// mandatory-reasoning model is a builder concern, never an injected effort.
// With no --reasoning-effort configured the request carries none, and the
// protocol's builder is what keeps the always-on model legal.
func TestBuildModelRequest_ThinkingAlwaysOn_InjectsNoEffort(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := alwaysOnThinkingProfile()

	// No session reasoning effort configured (empty string).
	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "")

	if req.ReasoningEffort != nil {
		t.Fatalf("ReasoningEffort = %q, want nil (no effort is ever injected)", *req.ReasoningEffort)
	}
}

// TestBuildModelRequest_ThinkingAlwaysOn_ExplicitEffortWins verifies that an
// explicit session effort still reaches a ThinkingAlwaysOn model's request.
func TestBuildModelRequest_ThinkingAlwaysOn_ExplicitEffortWins(t *testing.T) {
	t.Parallel()
	s := &Session{}
	profile := alwaysOnThinkingProfile()

	req := s.buildModelRequest(profile, "sys", []llm.Message{llm.User("hi")}, nil, "max")

	if req.ReasoningEffort == nil || *req.ReasoningEffort != "max" {
		t.Fatalf("ReasoningEffort = %v, want max (an explicit effort is honored)", req.ReasoningEffort)
	}
}

// TestBuildModelRequest_ThinkingAlwaysOn_ReasoningOffOmits verifies that a
// model the user declared reasoning=false gets no effort even when its row
// also carries thinking_always_on: an unconfigured effort is never filled in,
// and the protocol builder is what keeps a mandatory-reasoning model legal.
func TestBuildModelRequest_ThinkingAlwaysOn_ReasoningOffOmits(t *testing.T) {
	t.Parallel()
	s := &Session{}
	p := resolveTestProfile("lunaroute", lunarouteInstance(map[string]registry.Model{
		"mandatory-model": {Caps: registry.Caps{Reasoning: new(false), ThinkingAlwaysOn: new(true)}},
	}), "mandatory-model")
	if p.SupportsReasoning() || len(p.ReasoningEffortLevels()) != 0 {
		t.Fatalf("reasoning=false must clear the ladder: %v %v", p.SupportsReasoning(), p.ReasoningEffortLevels())
	}

	req := s.buildModelRequest(p, "sys", []llm.Message{llm.User("hi")}, nil, "")
	if req.ReasoningEffort != nil {
		t.Fatalf("ReasoningEffort = %q, want nil (an unconfigured effort is never filled in)", *req.ReasoningEffort)
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
		testOnly:        testConfig{metaFS: afero.NewMemMapFs()},
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
		ImageIntent:    "what is in this image",
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

	primary := resolveTestProfile("lunaroute", lunarouteInstance(map[string]registry.Model{
		"tiny-chat": {Caps: registry.Caps{Reasoning: new(false)}},
	}), "glm-5.2-nvfp4")

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, primary), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"tiny-chat"},
		ReasoningEffort: "max",
		testOnly:        testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

// Explicit providers.toml thinking_levels on the fallback model are
// authoritative: the fallback clamp must not replace them with catalog levels
// for the same model id. glm-5.2 ships catalog levels [high, max]; the
// instance here restricts it to [low, medium], so a session effort of "max"
// must clamp DOWN to medium — with the old catalog-first behavior it would
// stay at the catalog's "max".
func TestFallbackChain_ConfiguredLevelsBeatCatalog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	permErr := llm.ErrorFromHTTPStatus("lunaroute", 403, "primary denied", nil, nil)

	adapter := &agenttest.ModelTrackingAdapter{
		Provider: "lunaroute",
		Respond: func(req llm.Request) (llm.Response, error) {
			if req.Model == "glm-5.2" {
				if req.ReasoningEffort != nil {
					fbEffort = *req.ReasoningEffort
				}
				return agenttest.FinalResponse("fallback answered"), nil
			}
			return llm.Response{}, permErr
		},
	}
	c.Register(adapter)

	primary := resolveTestProfile("lunaroute", lunarouteInstance(map[string]registry.Model{
		"glm-5.2": {Caps: registry.Caps{EffortValues: []string{"low", "medium"}}},
	}), "glm-5.2-nvfp4")

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, primary), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"glm-5.2"},
		ReasoningEffort: "max",
		testOnly:        testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v (fallback should succeed)", err)
	}
	if fbEffort != "medium" {
		t.Fatalf("fallback ReasoningEffort = %q, want %q (configured levels beat catalog)", fbEffort, "medium")
	}
}

// An ollama fallback model whose local name collides with another provider's
// catalog id must clamp against its OWN instance's ladder: the registry never
// crosses providers, so "glm-5.2" on an ollama instance is an uncatalogued row
// with no ladder and effort "low" passes through unchanged (spec §7.4).
func TestFallbackChain_OllamaSkipsCatalogLevels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var fbEffort string
	permErr := llm.ErrorFromHTTPStatus("local", 403, "primary denied", nil, nil)

	adapter := &agenttest.ModelTrackingAdapter{
		Provider: "local",
		Respond: func(req llm.Request) (llm.Response, error) {
			if req.Model == "glm-5.2" {
				if req.ReasoningEffort != nil {
					fbEffort = *req.ReasoningEffort
				}
				return agenttest.FinalResponse("fallback answered"), nil
			}
			return llm.Response{}, permErr
		},
	}
	c.Register(adapter)

	primary := resolveTestProfile("local", map[string]registry.Provider{
		"local": {Base: "ollama"},
	}, "llama3:8b")

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, primary), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		LLMRetryPolicy:  &policy,
		ModelFallbacks:  []string{"glm-5.2"},
		ReasoningEffort: "low",
		testOnly:        testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v (fallback should succeed)", err)
	}
	if fbEffort != "low" {
		t.Fatalf("fallback ReasoningEffort = %q, want %q (ollama must not inherit catalog levels)", fbEffort, "low")
	}
}
