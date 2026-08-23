package llm

import (
	"testing"
)

// TestIsChatModelIDEmpty covers the empty ID path (line 244).
func TestIsChatModelIDEmpty(t *testing.T) {
	if IsChatModelID("") {
		t.Fatal("empty ID should return false")
	}
}

// TestResolveLiveModelInfoLookupFound covers the direct lookup path (lines
// 286-287).
func TestResolveLiveModelInfoLookupFound(t *testing.T) {
	c := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "gpt-5"},
		},
	}
	mi := c.ResolveLiveModelInfo("openai", "gpt-5")
	if mi == nil || mi.ID != "gpt-5" {
		t.Fatalf("ResolveLiveModelInfo = %v, want gpt-5", mi)
	}
}

// TestResolveLiveModelInfoBehaviorTagFallback covers the behavior tag
// fallback path (line 291).
func TestResolveLiveModelInfoBehaviorTagFallback(t *testing.T) {
	c := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "openai/gpt-5"},
		},
	}
	mi := c.ResolveLiveModelInfo("openai", "gpt-5")
	if mi == nil || mi.ID != "openai/gpt-5" {
		t.Fatalf("ResolveLiveModelInfo with tag fallback = %v, want openai/gpt-5", mi)
	}
}

// TestResolveLiveModelInfoNotFound covers the nil return path.
func TestResolveLiveModelInfoNotFound(t *testing.T) {
	c := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "other-model"},
		},
	}
	mi := c.ResolveLiveModelInfo("openai", "missing")
	if mi != nil {
		t.Fatalf("ResolveLiveModelInfo for missing = %v, want nil", mi)
	}
}
