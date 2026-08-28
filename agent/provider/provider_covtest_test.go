package provider

import (
	"reflect"
	"testing"
)

func TestCovOpenAIProfileCapabilities(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.5")
	if !p.SupportsStreaming() {
		t.Fatal("SupportsStreaming() = false, want true for the OpenAI surface")
	}
	if !p.SupportsWebSearch() {
		t.Fatal("SupportsWebSearch() = false, want the openai provider-level capability")
	}

	wantLevels := p.Resolved().Caps.EffortValues
	levels := p.ReasoningEffortLevels()
	if !reflect.DeepEqual(levels, wantLevels) {
		t.Fatalf("ReasoningEffortLevels() = %v, want the row's %v", levels, wantLevels)
	}
	levels[0] = "modified"
	if got := p.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
		t.Fatalf("mutating returned effort levels changed profile: got %v, want %v", got, wantLevels)
	}

	// An uncatalogued model still resolves; it just carries no facts.
	unknown := NewOpenAIProfile("evener-uncatalogued-test-model")
	if unknown.ContextWindowSize() != 0 || len(unknown.Warnings()) == 0 {
		t.Fatalf("uncatalogued model = %d window, warnings %v", unknown.ContextWindowSize(), unknown.Warnings())
	}
}
