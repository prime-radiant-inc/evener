package provider

import (
	"testing"

	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/llm/registry"
)

func TestRegistryKeysFromBehaviorTag(t *testing.T) {
	cases := []struct{ typ, style, surface, protocol, providerID string }{
		{"openai", "responses", registry.SurfaceOpenAI, registry.ProtocolOpenAIResponses, "openai"},
		{"openai", "chat-completions", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "openai-compatible"},
		{"anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "anthropic"},
		{"google", "", registry.SurfaceGoogle, registry.ProtocolGoogle, "google"},
		{"minimax", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "minimax"},
		{"kimi-anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "kimi-for-coding"},
		{"openrouter-anthropic", "", registry.SurfaceAnthropic, registry.ProtocolAnthropic, "openrouter"},
		{"openrouter", "", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "openrouter"},
		{"ollama", "", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "ollama"},
	}
	for _, tc := range cases {
		cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "inst", Type: providercfg.Type(tc.typ), APIStyle: providercfg.APIStyle(tc.style)}}}
		p, err := ResolveProfileFromConfig(cfg, "inst/some-model")
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.typ, tc.style, err)
		}
		if p.Surface() != tc.surface || p.Protocol() != tc.protocol || p.ProviderID() != tc.providerID {
			t.Fatalf("%s/%s: got %s %s %s", tc.typ, tc.style, p.Surface(), p.Protocol(), p.ProviderID())
		}
	}
}
