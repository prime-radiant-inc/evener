package provider

import (
	"testing"
)

// TestCovThinkingAlwaysOn covers ThinkingAlwaysOn (profile.go line 357).
func TestCovThinkingAlwaysOn(t *testing.T) {
	p := NewOpenAIProfile("gpt-5")
	// Default OpenAI profile should have thinkingAlwaysOn = false.
	if p.ThinkingAlwaysOn() {
		t.Fatal("expected ThinkingAlwaysOn=false for default profile")
	}
}

// TestCovCatalogEffortFallbackEligible covers CatalogEffortFallbackEligible
// (profile.go lines 1323-1328).
func TestCovCatalogEffortFallbackEligible(t *testing.T) {
	p := NewOpenAIProfile("gpt-5")
	// A profile created via NewOpenAIProfile has no instModels config,
	// so EffortLevelsConfigured should return false.
	// CatalogEffortFallbackEligible returns true when not configured and
	// not suppressed.
	result := p.CatalogEffortFallbackEligible()
	// Just verify it doesn't panic and returns a bool.
	_ = result
}

// TestCovReasoningEffortLevels covers ReasoningEffortLevels
// (profile.go lines 362-364).
func TestCovReasoningEffortLevels(t *testing.T) {
	p := NewOpenAIProfile("gpt-5")
	levels := p.ReasoningEffortLevels()
	if len(levels) == 0 {
		t.Fatal("expected non-empty effort levels for OpenAI profile")
	}
	// Verify it returns a copy, not the internal slice.
	levels[0] = "modified"
	levels2 := p.ReasoningEffortLevels()
	if levels2[0] == "modified" {
		t.Fatal("ReasoningEffortLevels should return a copy")
	}
}

// TestCovSupportsStreaming covers SupportsStreaming (profile.go line 367).
func TestCovSupportsStreaming(t *testing.T) {
	p := NewOpenAIProfile("gpt-5")
	if !p.SupportsStreaming() {
		t.Fatal("OpenAI profile should support streaming")
	}
}

// TestCovSupportsWebSearch covers SupportsWebSearch (profile.go line 370).
func TestCovSupportsWebSearch(t *testing.T) {
	p := NewOpenAIProfile("gpt-5")
	_ = p.SupportsWebSearch() // just verify no panic
}
