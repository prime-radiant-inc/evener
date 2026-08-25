package provider

import (
	"reflect"
	"testing"
)

func TestCovOpenAIProfileCapabilities(t *testing.T) {
	p := NewOpenAIProfile("evener-uncatalogued-test-model")
	if p.ThinkingAlwaysOn() {
		t.Fatal("ThinkingAlwaysOn() = true, want false for default OpenAI profile")
	}
	if !p.CatalogEffortFallbackEligible() {
		t.Fatal("CatalogEffortFallbackEligible() = false, want true without explicit model configuration")
	}
	if !p.SupportsStreaming() {
		t.Fatal("SupportsStreaming() = false, want true for OpenAI profile")
	}
	if !p.SupportsWebSearch() {
		t.Fatal("SupportsWebSearch() = false, want the OpenAI default for an uncatalogued model")
	}

	wantLevels := []string{"low", "medium", "high", "xhigh"}
	levels := p.ReasoningEffortLevels()
	if !reflect.DeepEqual(levels, wantLevels) {
		t.Fatalf("ReasoningEffortLevels() = %v, want %v", levels, wantLevels)
	}
	levels[0] = "modified"
	if got := p.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
		t.Fatalf("mutating returned effort levels changed profile: got %v, want %v", got, wantLevels)
	}
}
