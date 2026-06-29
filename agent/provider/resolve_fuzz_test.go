package provider

import (
	"testing"

	"primeradiant.com/serf/llm/providercfg"
)

// FuzzResolveProfileFromConfig drives ResolveProfileFromConfig — the package's
// real ref-parse + provider-type switch + profile-construction seam (including
// the catalog lookups inside the per-type constructors). The fuzzer varies the
// model ref plus the configured instance's type and api_style so every arm of
// the switch and its constructor is reachable. Beyond no-panic it asserts the
// resolve contract: success yields a non-nil profile whose ID is the instance
// name; failure yields a nil profile.
func FuzzResolveProfileFromConfig(f *testing.F) {
	seeds := []struct {
		ref, typ, style string
	}{
		{"inst/gpt-5.5", "openai", "responses"},
		{"inst/gpt-5.5", "openai", "chat_completions"},
		{"inst/claude-opus-4", "anthropic", ""},
		{"inst/gemini-2.5-pro", "google", ""},
		{"inst/abab", "minimax", ""},
		{"inst/kimi-k2", "kimi-anthropic", ""},
		{"inst/claude", "openrouter-anthropic", ""},
		{"inst/k2", "kimi", ""},
		{"inst/glm-4", "glm", ""},
		{"inst/m", "ollama", ""},
		{"inst/m", "unknown-type", ""},
		{"noslash", "openai", ""},
		{"/model", "openai", ""},
		{"inst/", "openai", ""},
		{"other/model", "openai", ""},
	}
	for _, s := range seeds {
		f.Add(s.ref, s.typ, s.style)
	}

	f.Fuzz(func(t *testing.T, ref, typ, style string) {
		cfg := providercfg.Config{
			Instances: []providercfg.InstanceConfig{{
				Name:     "inst",
				Type:     providercfg.Type(typ),
				APIStyle: providercfg.APIStyle(style),
			}},
		}

		prof, err := ResolveProfileFromConfig(cfg, ref)
		if err != nil {
			if prof != nil {
				t.Fatalf("error path returned non-nil profile: ref=%q typ=%q style=%q", ref, typ, style)
			}
			return
		}
		if prof == nil {
			t.Fatalf("success path returned nil profile: ref=%q typ=%q style=%q", ref, typ, style)
		}
		if prof.ID() != "inst" {
			t.Fatalf("profile ID = %q, want instance name %q (ref=%q)", prof.ID(), "inst", ref)
		}
	})
}
