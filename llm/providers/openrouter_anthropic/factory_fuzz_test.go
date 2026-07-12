package openrouter_anthropic

import (
	"net/http"
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
	t.Setenv(envvars.OpenRouterBaseURL.Name, " https://example.test/api/ ")
	a, ok, err := envAdapterFactory(llm.EnvConfig{})
	if err != nil || !ok || a == nil {
		t.Fatalf("configured = (%v, %v, %v)", a, ok, err)
	}
	got := a.(*adapter)
	if got.APIKey != "key" || got.BaseURL != "https://example.test/api" {
		t.Fatalf("adapter = {key:%q base:%q}", got.APIKey, got.BaseURL)
	}
}

func FuzzInstanceFactory(f *testing.F) {
	f.Add("named", " https://example.test/api/ ", "secret", "X-Test", "yes")
	f.Add("", "", "", "", "")
	f.Fuzz(func(t *testing.T, name, base, key, header, value string) {
		if strings.ContainsAny(header, "\r\n") {
			return
		}
		a, err := instanceAdapterFactory(providercfg.InstanceConfig{Name: name, BaseURL: base, APIKey: key, Headers: map[string]string{header: value}}, "ignored")
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
		} else {
			wantBase = strings.TrimRight(wantBase, "/")
		}
		if got.BaseURL != wantBase || got.APIKey != key {
			t.Fatalf("adapter = {base:%q key:%q}, want {%q %q}", got.BaseURL, got.APIKey, wantBase, key)
		}
		canonicalHeader := http.CanonicalHeaderKey(header)
		if header != "" && got.DefaultHeaders[canonicalHeader] != value {
			t.Fatalf("header %q = %q, want %q", canonicalHeader, got.DefaultHeaders[canonicalHeader], value)
		}
	})
}
