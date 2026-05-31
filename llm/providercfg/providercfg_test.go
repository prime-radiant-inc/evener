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
		{"openai", "", "openai"}, // default style = responses
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
