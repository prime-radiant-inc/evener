package providercfg

import (
	"sort"
	"testing"
)

func TestKnownTypeNames_SortedAndComplete(t *testing.T) {
	names := KnownTypeNames()
	if len(names) == 0 {
		t.Fatal("KnownTypeNames returned empty slice")
	}
	// Verify sorted.
	if !sort.StringsAreSorted(names) {
		t.Errorf("KnownTypeNames not sorted: %v", names)
	}
	// Verify every name is non-empty and appears in knownTypes.
	for _, n := range names {
		if n == "" {
			t.Errorf("KnownTypeNames returned empty string")
		}
		if !knownTypes[Type(n)] {
			t.Errorf("KnownTypeNames returned %q which is not in knownTypes", n)
		}
	}
	// Verify knownTypes has no entry missing from the returned slice.
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for t2 := range knownTypes {
		if !nameSet[string(t2)] {
			t.Errorf("knownTypes entry %q missing from KnownTypeNames", t2)
		}
	}
	// Verify "openai" and "anthropic" are always present (canary check).
	for _, must := range []string{"openai", "anthropic"} {
		if !nameSet[must] {
			t.Errorf("KnownTypeNames missing expected type %q", must)
		}
	}
}

func TestBehaviorTag(t *testing.T) {
	cases := []struct{ typ, style, want string }{
		{"openai", "responses", "openai"},
		{"openai", "chat-completions", "openai-compatible"},
		{"openai", "auto", "openai"}, // auto style falls through to typ identity
		{"openai", "", "openai"},     // default style = responses
		{"anthropic", "", "anthropic"},
		{"google", "", "google"},
		{"openrouter", "", "openrouter"},
		{"openrouter-anthropic", "", "openrouter-anthropic"},
		{"kimi", "", "kimi"},
		{"glm", "", "glm"},
		{"minimax", "", "minimax"},
		{"ollama", "", "ollama"},
	}
	for _, c := range cases {
		if got := BehaviorTag(c.typ, c.style); got != c.want {
			t.Errorf("BehaviorTag(%q,%q)=%q want %q", c.typ, c.style, got, c.want)
		}
	}
}

// TestCredentialTag pins the one place where "which key authenticates this
// instance" parts company with "how does this instance behave". Only
// openai-compatible splits, because it is the one tag that labels a protocol
// rather than a provider; every other tag names a provider with an endpoint of
// its own, so base_url cannot change whose key signs the request.
func TestCredentialTag(t *testing.T) {
	cases := []struct{ typ, style, baseURL, want, why string }{
		{"openai", "chat-completions", "", "openai",
			"no base_url: the adapter targets api.openai.com, so the openai row's key signs it"},
		{"openai", "chat-completions", "  ", "openai",
			"whitespace is not a base_url"},
		{"openai", "chat-completions", "http://127.0.0.1:8080/v1", "",
			"a gateway inherits no type-level key; OPENAI_COMPATIBLE_API_KEY belongs to another host"},
		{"openai", "responses", "", "openai", "responses is OpenAI proper either way"},
		{"openai", "responses", "https://gw.example/v1", "openai",
			"the responses adapter is OpenAI's own dialect; base_url does not re-home its credential"},
		{"openai", "auto", "", "openai", "auto resolves as openai"},
		{"anthropic", "", "", "anthropic", "tag identity"},
		{"anthropic", "", "https://gw.example/v1", "anthropic",
			"a typed provider keeps its own key at a custom endpoint"},
		{"ollama", "", "", "ollama", "tag identity"},
		{"glm", "", "https://api.z.ai/api/paas/v4", "glm", "tag identity"},
	}
	for _, c := range cases {
		if got := CredentialTag(c.typ, c.style, c.baseURL); got != c.want {
			t.Errorf("CredentialTag(%q,%q,%q)=%q want %q: %s", c.typ, c.style, c.baseURL, got, c.want, c.why)
		}
	}
}

func TestNameToTagIdentityForTypeNames(t *testing.T) {
	cfg := Config{Instances: []InstanceConfig{
		{Name: "openai", Type: "openai"},
		{Name: "work", Type: "openai", APIStyle: "chat-completions"},
	}}
	m := NameToTag(cfg)
	if m["openai"] != "openai" || m["work"] != "openai-compatible" {
		t.Errorf("NameToTag = %v", m)
	}
}
