package main

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// stubFastCheapAdapter is a minimal registered provider for validation tests.
// Its completion methods are never called here.
type stubFastCheapAdapter struct{ name string }

func (s stubFastCheapAdapter) Name() string { return s.name }
func (s stubFastCheapAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (s stubFastCheapAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func clientWithProviders(names ...string) *llm.Client {
	c := llm.NewClient()
	for _, n := range names {
		c.Register(stubFastCheapAdapter{name: n})
	}
	return c
}

func TestApplyFastCheapModel_SameProviderOverridesCheapModel(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "openai/gpt-4.1-mini", clientWithProviders("openai"))
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	if got.ID() != "openai" || got.Model() != "gpt-5.2" {
		t.Fatalf("active profile changed: id=%q model=%q", got.ID(), got.Model())
	}
	if got.CheapModel() != "gpt-4.1-mini" {
		t.Fatalf("CheapModel() = %q, want gpt-4.1-mini", got.CheapModel())
	}
	if got.CheapProvider() != "openai" {
		t.Fatalf("CheapProvider() = %q, want openai", got.CheapProvider())
	}
}

func TestApplyFastCheapModel_CrossProviderWhenRegistered(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "anthropic/claude-haiku-4-5-20251001", clientWithProviders("openai", "anthropic"))
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	// Active profile is untouched; only the cheap routing changes.
	if got.ID() != "openai" || got.Model() != "gpt-5.2" {
		t.Fatalf("active profile changed: id=%q model=%q", got.ID(), got.Model())
	}
	prov, model := got.CheapModelRef()
	if prov != "anthropic" || model != "claude-haiku-4-5-20251001" {
		t.Fatalf("CheapModelRef() = (%q, %q), want (anthropic, claude-haiku-4-5-20251001)", prov, model)
	}
}

func TestApplyFastCheapModel_CrossProviderRejectedWhenNotRegistered(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	_, err := applyFastCheapModel(profile, "anthropic/claude-haiku-4-5-20251001", clientWithProviders("openai"))
	if err == nil {
		t.Fatal("expected error for unregistered cheap provider")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error = %v, want it to name the unavailable provider anthropic", err)
	}
}

func TestApplyFastCheapModel_BareModelKeepsActiveProvider(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "gpt-4.1-mini", clientWithProviders("openai"))
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	if got.CheapModel() != "gpt-4.1-mini" || got.CheapProvider() != "openai" {
		t.Fatalf("cheap = (%q, %q), want (gpt-4.1-mini, openai)", got.CheapProvider(), got.CheapModel())
	}
}

func TestApplyFastCheapModel_BlankKeepsDefault(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "", clientWithProviders("openai"))
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	if got.CheapModel() != "gpt-4.1-nano" {
		t.Fatalf("CheapModel() = %q, want gpt-4.1-nano", got.CheapModel())
	}
}
