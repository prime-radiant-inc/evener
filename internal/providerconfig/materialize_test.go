package providerconfig

import (
	"strings"
	"testing"
)

func TestSeedDescriptorsOnly(t *testing.T) {
	getBase := func(typ string) string {
		switch typ {
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
	byName := map[string]InstanceConfig{}
	for _, i := range cfg.Instances {
		byName[i.Name] = i
		if i.APIKey != "" {
			t.Errorf("instance %q carries a secret api_key", i.Name)
		}
	}
	oc := byName["openai-compatible"]
	if oc.Type != "openai" || oc.APIStyle != StyleChatCompletions || oc.BaseURL != "https://vllm.local/v1" {
		t.Errorf("openai-compatible seed = %+v", oc)
	}
	if byName["openai"].BaseURL != "" || byName["openai"].APIStyle != StyleResponses {
		t.Errorf("openai seed = %+v", byName["openai"])
	}
	if byName["ollama"].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ollama base = %q", byName["ollama"].BaseURL)
	}
}

func TestMarshalDescriptorsOnly(t *testing.T) {
	cfg := Config{Default: "openai", Instances: []InstanceConfig{
		{Name: "openai", Type: "openai", APIStyle: StyleResponses, APIKey: "sk-LEAK"},
		{Name: "vllm", Type: "openai", APIStyle: StyleChatCompletions, BaseURL: "https://vllm.local/v1"},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-LEAK") || strings.Contains(string(data), "api_key") {
		t.Fatalf("Marshal leaked a secret:\n%s", data)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != "openai" || len(got.Instances) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
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
