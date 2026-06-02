package provider_test

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestResolveProfileFromConfig_OpenAIResponses(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "responses"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "work/gpt-5.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "work" {
		t.Errorf("ID() = %q, want %q", p.ID(), "work")
	}
	if p.BehaviorTag() != "openai" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "openai")
	}
	if p.Model() != "gpt-5.2" {
		t.Errorf("Model() = %q, want %q", p.Model(), "gpt-5.2")
	}
}

func TestResolveProfileFromConfig_OpenAIDefaultStyle(t *testing.T) {
	// Empty APIStyle on openai type defaults to responses behavior (not chat-completions)
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "myopenai", Type: "openai", APIStyle: ""},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "myopenai/gpt-4.1-mini")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "myopenai" {
		t.Errorf("ID() = %q, want %q", p.ID(), "myopenai")
	}
	if p.BehaviorTag() != "openai" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "openai")
	}
	if p.Model() != "gpt-4.1-mini" {
		t.Errorf("Model() = %q, want %q", p.Model(), "gpt-4.1-mini")
	}
}

func TestResolveProfileFromConfig_ChatCompletions(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "localai", Type: "openai", APIStyle: "chat-completions"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "localai/gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "localai" {
		t.Errorf("ID() = %q, want %q", p.ID(), "localai")
	}
	if p.BehaviorTag() != "openai-compatible" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "openai-compatible")
	}
	if p.Model() != "gpt-3.5-turbo" {
		t.Errorf("Model() = %q, want %q", p.Model(), "gpt-3.5-turbo")
	}
}

func TestResolveProfileFromConfig_Kimi(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "kc", Type: "kimi"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "kc/kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "kc" {
		t.Errorf("ID() = %q, want %q", p.ID(), "kc")
	}
	if p.BehaviorTag() != "kimi" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "kimi")
	}
	if p.Model() != "kimi-k2" {
		t.Errorf("Model() = %q, want %q", p.Model(), "kimi-k2")
	}
}

func TestResolveProfileFromConfig_Anthropic(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "ant", Type: "anthropic"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "ant/claude-opus-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "ant" {
		t.Errorf("ID() = %q, want %q", p.ID(), "ant")
	}
	if p.BehaviorTag() != "anthropic" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "anthropic")
	}
	if p.Model() != "claude-opus-4-6" {
		t.Errorf("Model() = %q, want %q", p.Model(), "claude-opus-4-6")
	}
}

func TestResolveProfileFromConfig_Google(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "g1", Type: "google"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "g1/gemini-2.5-pro")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "g1" {
		t.Errorf("ID() = %q, want %q", p.ID(), "g1")
	}
	if p.BehaviorTag() != "google" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "google")
	}
	if p.Model() != "gemini-2.5-pro" {
		t.Errorf("Model() = %q, want %q", p.Model(), "gemini-2.5-pro")
	}
}

func TestResolveProfileFromConfig_GLM(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "glm-inst", Type: "glm"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "glm-inst/glm-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "glm-inst" {
		t.Errorf("ID() = %q, want %q", p.ID(), "glm-inst")
	}
	if p.BehaviorTag() != "glm" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "glm")
	}
}

func TestResolveProfileFromConfig_MiniMax(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "mm", Type: "minimax"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "mm/minimax/minimax-m2.7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "mm" {
		t.Errorf("ID() = %q, want %q", p.ID(), "mm")
	}
	if p.BehaviorTag() != "minimax" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "minimax")
	}
	// Model is everything after the first slash
	if p.Model() != "minimax/minimax-m2.7" {
		t.Errorf("Model() = %q, want %q", p.Model(), "minimax/minimax-m2.7")
	}
}

func TestResolveProfileFromConfig_OpenRouterAnthropic(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "ora", Type: "openrouter-anthropic"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "ora/anthropic/claude-opus-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "ora" {
		t.Errorf("ID() = %q, want %q", p.ID(), "ora")
	}
	if p.BehaviorTag() != "openrouter-anthropic" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "openrouter-anthropic")
	}
	if p.Model() != "anthropic/claude-opus-4" {
		t.Errorf("Model() = %q, want %q", p.Model(), "anthropic/claude-opus-4")
	}
}

func TestResolveProfileFromConfig_Ollama(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "local", Type: "ollama"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "local/llama3.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "local" {
		t.Errorf("ID() = %q, want %q", p.ID(), "local")
	}
	if p.BehaviorTag() != "ollama" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "ollama")
	}
}

func TestResolveProfileFromConfig_UnknownInstance(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai"},
			{Name: "ant", Type: "anthropic"},
		},
	}
	_, err := provider.ResolveProfileFromConfig(cfg, "nope/gpt-5")
	if err == nil {
		t.Fatal("expected error for unknown instance, got nil")
	}
	// Error should list the configured names.
	if !strings.Contains(err.Error(), "work") || !strings.Contains(err.Error(), "ant") {
		t.Errorf("error should list configured instance names, got: %v", err)
	}
}

func TestResolveProfileFromConfig_UnknownType(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "exotic", Type: "exotic-provider"},
		},
	}
	_, err := provider.ResolveProfileFromConfig(cfg, "exotic/some-model")
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
}

func TestResolveProfileFromConfig_ChatCompletionsTagNotDerivedFromInstanceName(t *testing.T) {
	// The tag must be "openai-compatible" (from the type+style), NOT derived
	// from the instance name "work". Passing inst.Name to newOpenAICompatProfile
	// would derive a wrong tag via BehaviorTag("work","").
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "chat-completions"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "work/gpt-5.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.BehaviorTag() != "openai-compatible" {
		t.Errorf("BehaviorTag() = %q, want %q — tag must come from type+style, not instance name", p.BehaviorTag(), "openai-compatible")
	}
	if p.ID() != "work" {
		t.Errorf("ID() = %q, want %q", p.ID(), "work")
	}
}

// TestResolveProfileFromConfig_KimiContextWindowFromCatalog confirms that the
// config path sources the context window from the embedded catalog keyed on the
// behavior tag — not from KIMI_* env vars and not from a live /v1/models query.
//
// "moonshot/kimi-latest-128k" is present in the embedded catalog with a context
// window of 131072.  The 128K default (131072 != 128000) is distinct, so any
// slip back to the default exposes the gap.  No KIMI_* env must be set.
func TestResolveProfileFromConfig_KimiContextWindowFromCatalog(t *testing.T) {
	// Confirm no KIMI_* env is active — if it were, the test would be vacuous
	// (the config path doesn't read env anyway, but we make the contract explicit).
	for _, key := range []string{"KIMI_API_KEY", "KIMI_BASE_URL"} {
		if v := os.Getenv(key); v != "" {
			t.Setenv(key, "")
		}
	}

	// Determine the expected context window from the catalog.
	const catalogModel = "moonshot/kimi-latest-128k"
	cat := llm.EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog is nil — cannot confirm catalog-keyed lookup")
	}
	mi := cat.GetModelInfo(catalogModel)
	if mi == nil {
		t.Fatalf("model %q not found in embedded catalog; pick a different model for this test", catalogModel)
	}
	wantCtx := mi.ContextWindow
	if wantCtx == 0 {
		t.Fatalf("catalog entry for %q has zero context window", catalogModel)
	}
	// Sanity: catalog window must differ from the 128K fallback so the test is meaningful.
	const defaultWindow = 128_000
	if wantCtx == defaultWindow {
		t.Fatalf("catalog window %d == default %d; choose a model with a distinct window", wantCtx, defaultWindow)
	}

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "kc", Type: "kimi"},
		},
	}
	p, err := provider.ResolveProfileFromConfig(cfg, "kc/"+catalogModel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID() != "kc" {
		t.Errorf("ID() = %q, want %q", p.ID(), "kc")
	}
	if p.BehaviorTag() != "kimi" {
		t.Errorf("BehaviorTag() = %q, want %q", p.BehaviorTag(), "kimi")
	}
	if got := p.ContextWindowSize(); got != wantCtx {
		t.Errorf("ContextWindowSize() = %d, want %d (from catalog %q); config path must source window from catalog by tag, not from env",
			got, wantCtx, catalogModel)
	}
}
