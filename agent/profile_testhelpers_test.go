package agent

import (
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm/providercfg"
)

// Test shims for building provider profiles from package agent.
//
// Profile and its constructors live in agent/provider. Agent tests build
// profiles through the public provider API; the unexported-constructor shims
// route through provider.ResolveProfileFromConfig with a single synthesized
// instance whose name equals its type, which reproduces each provider's
// constructor exactly (ID() == the provider type, as the originals returned).

// The profile constructor and With* decorators are re-exported so existing
// tests need not qualify every call.
var (
	NewOpenAIProfile            = provider.NewOpenAIProfile
	ResolveProfileFromConfig    = provider.ResolveProfileFromConfig
	WithProviderID              = provider.WithProviderID
	WithCheapModel              = provider.WithCheapModel
	WithContextWindow           = provider.WithContextWindow
	WithCommunicateOutputSchema = provider.WithCommunicateOutputSchema
	WithAllowedDecisions        = provider.WithAllowedDecisions
)

func resolveTestProfile(inst providercfg.InstanceConfig, model string) *provider.Profile {
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{inst}}
	p, err := provider.ResolveProfileFromConfig(cfg, inst.Name+"/"+model)
	if err != nil {
		panic("resolveTestProfile: " + err.Error())
	}
	return p
}

func newAnthropicProfile(model string) *provider.Profile {
	return resolveTestProfile(providercfg.InstanceConfig{Name: "anthropic", Type: "anthropic"}, model)
}

func newGeminiProfile(model string) *provider.Profile {
	return resolveTestProfile(providercfg.InstanceConfig{Name: "google", Type: "google"}, model)
}

func newMiniMaxProfile(model string) *provider.Profile {
	return resolveTestProfile(providercfg.InstanceConfig{Name: "minimax", Type: "minimax"}, model)
}

func newOpenRouterAnthropicProfile(model string) *provider.Profile {
	return resolveTestProfile(providercfg.InstanceConfig{Name: "openrouter-anthropic", Type: "openrouter-anthropic"}, model)
}

func newOpenAICompatProfile(id, model string, _ int) *provider.Profile {
	inst := providercfg.InstanceConfig{Name: id, Type: providercfg.Type(id)}
	if id == "openai-compatible" {
		// The "openai-compatible" behavior tag comes from an openai instance
		// using the chat-completions API style.
		inst = providercfg.InstanceConfig{Name: id, Type: "openai", APIStyle: providercfg.StyleChatCompletions}
	}
	return resolveTestProfile(inst, model)
}

// testProfile builds a profile for the given provider type and model, with the
// context window overridden when contextWindow > 0. The common fixture for
// context-manager, strategy, and transcript tests.
func testProfile(providerType, model string, contextWindow int) *provider.Profile {
	var p *provider.Profile
	if providerType == "openai" {
		p = provider.NewOpenAIProfile(model)
	} else {
		p = resolveTestProfile(providercfg.InstanceConfig{Name: providerType, Type: providercfg.Type(providerType)}, model)
	}
	if contextWindow > 0 {
		p = provider.WithContextWindow(p, contextWindow)
	}
	return p
}

// toStringSlice converts a JSON-schema array field (either []string directly or
// []any after a JSON round-trip) to []string, for asserting on tool-schema
// fields in tests.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// testOpenAICompatProfile builds an "openai-compatible"-tagged profile (openai
// type, chat-completions style) with the given instance id, model, and window.
func testOpenAICompatProfile(id, model string, contextWindow int) *provider.Profile {
	p := resolveTestProfile(providercfg.InstanceConfig{Name: id, Type: "openai", APIStyle: providercfg.StyleChatCompletions}, model)
	if contextWindow > 0 {
		p = provider.WithContextWindow(p, contextWindow)
	}
	return p
}
