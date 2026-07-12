package kimi_anthropic

import (
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/kimicoding"
)

func TestEnvFactory(t *testing.T) {
	t.Setenv(envvars.KimiCodingAPIKey.Name, "")
	if a, ok, err := envAdapterFactory(llm.EnvConfig{}); err != nil || ok || a != nil {
		t.Fatalf("unconfigured = (%v, %v, %v), want (nil, false, nil)", a, ok, err)
	}
	t.Setenv(envvars.KimiCodingAPIKey.Name, "  key  ")
	t.Setenv(envvars.KimiCodingBaseURL.Name, "  https://example.test/root/  ")
	a, ok, err := envAdapterFactory(llm.EnvConfig{})
	if err != nil || !ok || a == nil || a.Name() != providerName {
		t.Fatalf("configured = (%v, %v, %v)", a, ok, err)
	}
	got := a.(*adapter)
	if got.APIKey != "key" || got.BaseURL != "https://example.test/root" {
		t.Fatalf("adapter = {key:%q base:%q}", got.APIKey, got.BaseURL)
	}
}

func FuzzInstanceFactory(f *testing.F) {
	f.Add("named", " https://example.test/v1/ ", "secret", "X-Test", "yes")
	f.Add("", "", "", "User-Agent", "custom")
	f.Fuzz(func(t *testing.T, name, base, key, header, value string) {
		t.Setenv(envvars.KimiCodingAPIKey.Name, "")
		_, _, _ = envAdapterFactory(llm.EnvConfig{})
		t.Setenv(envvars.KimiCodingAPIKey.Name, "seed-key")
		t.Setenv(envvars.KimiCodingBaseURL.Name, strings.ReplaceAll(base, "\x00", ""))
		_, _, _ = envAdapterFactory(llm.EnvConfig{})
		_ = newTestAdapter(base, key, &http.Client{})
		if strings.ContainsAny(header, "\r\n") {
			return
		}
		headers := map[string]string{header: value}
		a, err := instanceAdapterFactory(providercfg.InstanceConfig{Name: name, BaseURL: base, APIKey: key, Headers: headers}, "ignored")
		wantName := name
		if wantName == "" {
			wantName = providerName
		}
		if err != nil || a == nil || a.Name() != wantName {
			t.Fatalf("factory = (%v, %v), want named adapter", a, err)
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
		if canonicalHeader != "User-Agent" && got.DefaultHeaders["User-Agent"] != kimicoding.UserAgent {
			t.Fatalf("default User-Agent = %q", got.DefaultHeaders["User-Agent"])
		}
	})
}
