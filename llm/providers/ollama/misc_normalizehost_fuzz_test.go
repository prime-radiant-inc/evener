package ollama

import (
	"net/url"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

// FuzzMiscOllamaNormalizeHost drives normalizeHost — the OLLAMA_HOST resolver
// that turns a bare host, host:port, bracketed/bare IPv6 literal, or full URL
// into a base URL ending in /v1 — over arbitrary fuzzed input.
//
// Oracles:
//   - never panics (floor);
//   - successful results are structurally valid http(s) URLs with an authority
//     and a /v1 path; invalid input is rejected rather than rewritten;
//   - determinism: normalizeHost is pure — two calls agree;
//   - idempotence (metamorphic): normalizeHost already emits a scheme-bearing
//     value ending in /v1, and that form is preserved verbatim by the function,
//     so normalizeHost(normalizeHost(h)) == normalizeHost(h). A second pass that
//     re-mangles a value it just produced would be a real bug.
func FuzzMiscOllamaNormalizeHost(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"localhost",
		"localhost:11434",
		"http://localhost:11434/v1",
		"https://proxy.example/ollama/v1",
		"https://proxy.example/ollama",
		"127.0.0.1",
		"127.0.0.1:1234",
		"::1",
		"fe80::1",
		"[::1]",
		"[::1]:8080",
		"host:a:b",
		"http://x",
		"://",
		"/v1",
		"trailing///",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, h string) {
		_ = resolveBaseURL(" https://base.invalid/v1/ ", h)
		_ = resolveBaseURL("", h)
		_ = resolveBaseURL("", "")
		newAdapter("", nil).NonDefaultEligible()
		_ = newForInstance(InstanceParams{})
		_ = newForInstance(InstanceParams{Name: "seed", BaseURL: "https://ollama.invalid/v1"})
		t.Setenv(envvars.OllamaBaseURL.Name, "")
		t.Setenv(envvars.OllamaHost.Name, strings.ReplaceAll(h, "\x00", ""))
		t.Setenv(envvars.OllamaAPIKey.Name, "seed-key")
		_, _ = llm.NewFromEnv()
		_, _ = llm.NewFromProviders(providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "ollama-seed", Type: providerName}}})
		got, err := normalizeHostResult(h)
		if err != nil {
			if got != "" {
				t.Fatalf("normalizeHost(%q) returned %q with error %v", h, got, err)
			}
			return
		}

		u, err := url.Parse(got)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v1") {
			t.Fatalf("normalizeHost(%q) = %q, not a supported endpoint: %v", h, got, err)
		}

		if got2 := normalizeHost(h); got2 != got {
			t.Fatalf("normalizeHost nondeterministic: %q then %q for input %q", got, got2, h)
		}

		if again, err := normalizeHostResult(got); err != nil || again != got {
			t.Fatalf("normalizeHost not idempotent on its own output:\n input=%q\n once=%q\n twice=%q", h, got, again)
		}
	})
}
