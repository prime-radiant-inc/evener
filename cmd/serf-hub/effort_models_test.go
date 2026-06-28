package main

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
)

// wantOpus46Levels is the canonical effort-level slice for claude-opus-4-6 as
// defined in the embedded model catalog.
var wantOpus46Levels = []string{"low", "medium", "high", "max"}

// The model picker needs each model's supported reasoning-effort levels so the
// effort chip can offer per-model choices instead of a static list.
func TestModelDescriptorsToAPIModels_IncludesReasoningEffortLevels(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	})
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels, ok := models[0]["reasoning_effort_levels"].([]string)
	if !ok {
		t.Fatalf("reasoning_effort_levels = %v (%T), want []string", models[0]["reasoning_effort_levels"], models[0]["reasoning_effort_levels"])
	}
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("reasoning_effort_levels = %v, want %v", levels, wantOpus46Levels)
	}
}

// Provider-qualified models (e.g. an openrouter-anthropic instance serving
// "anthropic/claude-opus-4-6") must still resolve the catalog override keyed by
// the bare model id, so the effort chip offers per-model levels (incl. "max").
func TestModelDescriptorsToAPIModels_ProviderQualifiedModelResolvesLevels(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "openrouter-anthropic", Model: "anthropic/claude-opus-4-6"},
	})
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels, ok := models[0]["reasoning_effort_levels"].([]string)
	if !ok {
		t.Fatalf("namespaced model: reasoning_effort_levels = %v (%T), want []string (catalog override via last segment)", models[0]["reasoning_effort_levels"], models[0]["reasoning_effort_levels"])
	}
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("namespaced model: reasoning_effort_levels = %v, want %v (catalog strip-prefix must resolve to opus-4-6 entry)", levels, wantOpus46Levels)
	}
}
