package main

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm/providercfg"
)

// wantOpus46Levels is the canonical effort-level slice for claude-opus-4-6 as
// defined in the embedded model catalog.
var wantOpus46Levels = []string{"low", "medium", "high", "max"}

// The model picker needs each model's supported reasoning-effort levels so the
// effort chip can offer per-model choices instead of a static list.
func TestModelDescriptorsToAPIModels_IncludesReasoningEffortLevels(t *testing.T) {
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, nil)
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
	}, nil)
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

// An instance-defined model in providers.toml must win over the embedded
// catalog: its ThinkingLevels keys, rank-ordered, are what the effort chip
// offers — even when the catalog has its own (different) opinion for the same
// bare model id.
func TestModelDescriptorsToAPIModels_InstanceModelLevelsWinOverCatalog(t *testing.T) {
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "lunaroute",
				Type: "openai",
				Models: map[string]providercfg.ModelConfig{
					"glm-5.2-nvfp4": {
						// Deliberately out of rank order to prove the hub
						// re-sorts by llm.ReasoningEffortRank rather than
						// trusting map/TOML iteration order.
						ThinkingLevels: map[string]string{
							"xhigh":   "xhigh",
							"low":     "low",
							"medium":  "medium",
							"minimal": "minimal",
							"high":    "high",
						},
					},
				},
			},
		},
	}
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "lunaroute", Model: "glm-5.2-nvfp4"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels, ok := models[0]["reasoning_effort_levels"].([]string)
	if !ok {
		t.Fatalf("reasoning_effort_levels = %v (%T), want []string", models[0]["reasoning_effort_levels"], models[0]["reasoning_effort_levels"])
	}
	want := []string{"minimal", "low", "medium", "high", "xhigh"}
	if !reflect.DeepEqual(levels, want) {
		t.Errorf("reasoning_effort_levels = %v, want %v (rank order)", levels, want)
	}
}

// An instance model declared non-reasoning (reasoning=false) must report no
// effort levels at all, even if the catalog has levels for the same bare id.
func TestModelDescriptorsToAPIModels_InstanceModelReasoningFalseClearsLevels(t *testing.T) {
	no := false
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "anthropic",
				Type: "anthropic",
				Models: map[string]providercfg.ModelConfig{
					"claude-opus-4-6": {Reasoning: &no},
				},
			},
		},
	}
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if levels, ok := models[0]["reasoning_effort_levels"].([]string); ok && len(levels) > 0 {
		t.Errorf("reasoning_effort_levels = %v, want empty (reasoning=false)", levels)
	}
	if got, ok := models[0]["supports_reasoning"].(bool); !ok || got {
		t.Errorf("supports_reasoning = %v, want false", models[0]["supports_reasoning"])
	}
}

// A model absent from the instance's Models map falls back to catalog
// behavior unchanged, even when the instance defines other models.
func TestModelDescriptorsToAPIModels_ModelAbsentFromInstanceFallsBackToCatalog(t *testing.T) {
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "anthropic",
				Type: "anthropic",
				Models: map[string]providercfg.ModelConfig{
					"some-other-model": {ContextWindow: 999},
				},
			},
		},
	}
	models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels, ok := models[0]["reasoning_effort_levels"].([]string)
	if !ok {
		t.Fatalf("reasoning_effort_levels = %v (%T), want []string", models[0]["reasoning_effort_levels"], models[0]["reasoning_effort_levels"])
	}
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("reasoning_effort_levels = %v, want %v (unchanged catalog behavior)", levels, wantOpus46Levels)
	}
}

// reasoning = true without custom levels must still override a live/catalog
// supports_reasoning=false — the session treats the model as reasoning-capable,
// so the spawn UI must not hide the effort picker.
func TestModelDescriptorsToAPIModels_InstanceReasoningTrueOverridesCatalog(t *testing.T) {
	on := true
	cfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "gw",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"local-reasoner": {Reasoning: &on},
		},
	}}}
	entry := map[string]any{"supports_reasoning": false}
	applyInstanceModelOverride(entry, cfg, "gw", "local-reasoner")
	if got, _ := entry["supports_reasoning"].(bool); !got {
		t.Errorf("supports_reasoning = %v, want true (explicit reasoning=true beats catalog/live)", entry["supports_reasoning"])
	}
	if _, ok := entry["reasoning_effort_levels"]; ok {
		t.Errorf("levels should stay derived when only reasoning=true is configured: %v", entry["reasoning_effort_levels"])
	}
}
