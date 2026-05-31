package cmdutil

import (
	"testing"

	"primeradiant.com/serf/llm/providercfg"
)

func TestSeedDescriptorsOnly(t *testing.T) {
	getBase := func(typ string) string {
		switch typ {
		case "openai":
			return "https://oai.example/v1"
		case "openai-compatible":
			return "https://vllm.local/v1"
		case "ollama":
			return "http://localhost:11434/v1"
		}
		return ""
	}
	cfg := Seed([]string{"anthropic", "openai", "openai-compatible", "ollama"}, "anthropic", getBase)
	if cfg.Default != "anthropic" {
		t.Fatalf("default = %q, want anthropic", cfg.Default)
	}
	byName := map[string]providercfg.InstanceConfig{}
	for _, i := range cfg.Instances {
		byName[i.Name] = i
		if i.APIKey != "" {
			t.Errorf("instance %q carries a secret api_key", i.Name)
		}
	}
	oc := byName["openai-compatible"]
	if oc.Type != "openai" || oc.APIStyle != providercfg.StyleChatCompletions || oc.BaseURL != "https://vllm.local/v1" {
		t.Errorf("openai-compatible seed = %+v", oc)
	}
	if byName["openai"].BaseURL != "https://oai.example/v1" || byName["openai"].APIStyle != providercfg.StyleResponses {
		t.Errorf("openai seed = %+v", byName["openai"])
	}
	if byName["ollama"].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ollama base = %q", byName["ollama"].BaseURL)
	}
}

func TestBaseURLEnvVar(t *testing.T) {
	if got := BaseURLEnvVar("kimi"); got != "KIMI_BASE_URL" {
		t.Errorf("kimi = %q", got)
	}
	if got := BaseURLEnvVar("openai-compatible"); got != "OPENAI_COMPATIBLE_BASE_URL" {
		t.Errorf("openai-compatible = %q", got)
	}
	if got := BaseURLEnvVar("ollama"); got != "" {
		t.Errorf("ollama should be empty (caller resolves), got %q", got)
	}
}
