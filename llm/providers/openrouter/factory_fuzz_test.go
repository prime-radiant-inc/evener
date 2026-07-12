package openrouter

import (
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestEnvFactory(t *testing.T) {
	t.Setenv(envvars.OpenRouterAPIKey.Name, "")
	if a, ok, err := envAdapterFactory(llm.EnvConfig{}); err != nil || ok || a != nil {
		t.Fatalf("unconfigured = (%v, %v, %v)", a, ok, err)
	}
	t.Setenv(envvars.OpenRouterAPIKey.Name, " key ")
	t.Setenv(envvars.OpenRouterBaseURL.Name, " https://example.test/v1 ")
	a, ok, err := envAdapterFactory(llm.EnvConfig{})
	if err != nil || !ok || a == nil {
		t.Fatalf("configured = (%v, %v, %v)", a, ok, err)
	}
	got := a.(*adapter)
	if got.APIKey != "key" || got.BaseURL != "https://example.test/v1" {
		t.Fatalf("adapter = {key:%q base:%q}", got.APIKey, got.BaseURL)
	}
}

func FuzzInstanceFactory(f *testing.F) {
	f.Add("named", " https://example.test/v1 ", "secret", "model", uint16(8192))
	f.Add("", "", "", "", uint16(0))
	f.Fuzz(func(t *testing.T, name, base, key, model string, tokens uint16) {
		t.Setenv(envvars.OpenRouterAPIKey.Name, "")
		_, _, _ = envAdapterFactory(llm.EnvConfig{})
		t.Setenv(envvars.OpenRouterAPIKey.Name, "seed-key")
		t.Setenv(envvars.OpenRouterBaseURL.Name, strings.ReplaceAll(base, "\x00", ""))
		_, _, _ = envAdapterFactory(llm.EnvConfig{})
		models := map[string]providercfg.ModelConfig{model: {MaxOutputTokens: int(tokens)}}
		a, err := instanceAdapterFactory(providercfg.InstanceConfig{Name: name, BaseURL: base, APIKey: key, Compat: &providercfg.CompatConfig{ThinkingFormat: "openai"}, Models: models, Headers: map[string]string{"X-Test": "yes"}}, "ignored")
		wantName := name
		if wantName == "" {
			wantName = providerName
		}
		if err != nil || a == nil || a.Name() != wantName {
			t.Fatalf("factory = (%v, %v)", a, err)
		}
		got := a.(*adapter)
		wantBase := strings.TrimSpace(base)
		if wantBase == "" {
			wantBase = defaultBaseURL
		}
		wantBase = strings.TrimRight(wantBase, "/")
		if got.BaseURL != wantBase || got.APIKey != key || got.DefaultHeaders["X-Test"] != "yes" {
			t.Fatalf("adapter forwarding failed: %+v", got)
		}
		if got.Quirks.ThinkingFormat != "openai" {
			t.Fatalf("ThinkingFormat = %q", got.Quirks.ThinkingFormat)
		}
	})
}
