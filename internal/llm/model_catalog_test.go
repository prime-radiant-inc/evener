package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelCatalogFromLiteLLMJSON_GetListLatest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	body := `{
  "sample_spec": {"litellm_provider":"openai","mode":"chat"},
  "gpt-5.2": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,"max_output_tokens":2000,
    "input_cost_per_token":0.000001,"output_cost_per_token":0.000002,
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gpt-5.2-mini": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":500,"max_output_tokens":1000,
    "supports_function_calling":true
  },
  "claude-opus-4-6": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":"200000","max_output_tokens":"8192",
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gemini-3-flash-preview": {
    "litellm_provider":"gemini","mode":"chat",
    "max_input_tokens":1000000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_vision":true
  },
  "text-embedding-3-large": {
    "litellm_provider":"openai","mode":"embedding","max_input_tokens":8191
  }
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadModelCatalogFromLiteLLMJSON(p)
	if err != nil {
		t.Fatalf("LoadModelCatalogFromLiteLLMJSON: %v", err)
	}
	// sample_spec + embedding entry should be skipped.
	if got, wantMin := len(c.Models), 4; got != wantMin {
		t.Fatalf("models: got %d want %d", got, wantMin)
	}

	mi := c.GetModelInfo("gpt-5.2")
	if mi == nil {
		t.Fatalf("GetModelInfo returned nil")
	}
	if mi.Provider != "openai" {
		t.Fatalf("provider: got %q want %q", mi.Provider, "openai")
	}
	if mi.ContextWindow != 1000 {
		t.Fatalf("context_window: got %d want %d", mi.ContextWindow, 1000)
	}
	if mi.MaxOutputTokens == nil || *mi.MaxOutputTokens != 2000 {
		t.Fatalf("max_output_tokens: got %v want %d", mi.MaxOutputTokens, 2000)
	}
	if mi.InputCostPerMillion == nil || *mi.InputCostPerMillion != 1.0 {
		t.Fatalf("input_cost_per_million: got %v want %v", mi.InputCostPerMillion, 1.0)
	}
	if mi.OutputCostPerMillion == nil || *mi.OutputCostPerMillion != 2.0 {
		t.Fatalf("output_cost_per_million: got %v want %v", mi.OutputCostPerMillion, 2.0)
	}

	opens := c.ListModels("openai")
	if got, want := len(opens), 2; got != want {
		t.Fatalf("openai models: got %d want %d", got, want)
	}
	gems := c.ListModels("gemini") // alias => google
	if got, want := len(gems), 1; got != want {
		t.Fatalf("gemini/google models: got %d want %d", got, want)
	}
	if gems[0].Provider != "google" {
		t.Fatalf("gemini provider normalized: got %q want %q", gems[0].Provider, "google")
	}

	latestOpenAI := c.GetLatestModel("openai", "")
	if latestOpenAI == nil || latestOpenAI.ID != "gpt-5.2" {
		t.Fatalf("latest openai: got %+v want gpt-5.2", latestOpenAI)
	}
	latestVision := c.GetLatestModel("openai", "vision")
	if latestVision == nil || latestVision.ID != "gpt-5.2" {
		t.Fatalf("latest openai vision: got %+v want gpt-5.2", latestVision)
	}
	latestReasoning := c.GetLatestModel("google", "reasoning")
	if latestReasoning != nil {
		t.Fatalf("expected no google reasoning model in sample catalog; got %+v", latestReasoning)
	}
}

func TestParseLiteLLMCatalog(t *testing.T) {
	body := `{
  "sample_spec": {"litellm_provider":"openai","mode":"chat"},
  "gpt-5.2": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,"max_output_tokens":2000,
    "supports_function_calling":true
  }
}`
	cat, err := parseLiteLLMCatalog([]byte(body))
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	if len(cat.Models) != 1 {
		t.Fatalf("models: got %d want 1", len(cat.Models))
	}
	if cat.Models[0].ID != "gpt-5.2" {
		t.Fatalf("model ID = %q", cat.Models[0].ID)
	}
}

func TestEmbeddedModelCatalog(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("EmbeddedModelCatalog returned nil")
	}
	if len(cat.Models) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	// Spot-check a well-known model.
	info := cat.GetModelInfo("gpt-4o")
	if info == nil {
		t.Fatal("gpt-4o not found in embedded catalog")
	}
	if !strings.EqualFold(info.Provider, "openai") {
		t.Fatalf("gpt-4o provider = %q", info.Provider)
	}
}

