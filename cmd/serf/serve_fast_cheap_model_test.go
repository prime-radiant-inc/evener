package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
)

func TestApplyFastCheapModel_SameProviderOverridesCheapModel(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "openai/gpt-4.1-mini")
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	if got.ID() != "openai" || got.Model() != "gpt-5.2" {
		t.Fatalf("active profile changed: id=%q model=%q", got.ID(), got.Model())
	}
	if got.CheapModel() != "gpt-4.1-mini" {
		t.Fatalf("CheapModel() = %q, want gpt-4.1-mini", got.CheapModel())
	}
}

func TestApplyFastCheapModel_RejectsCrossProvider(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	_, err := applyFastCheapModel(profile, "anthropic/claude-haiku-4-5-20251001")
	if err == nil {
		t.Fatal("expected cross-provider error")
	}
	if !strings.Contains(err.Error(), "provider") || !strings.Contains(err.Error(), "openai") || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error = %v, want clear provider mismatch", err)
	}
}

func TestApplyFastCheapModel_BlankKeepsDefault(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5.2")
	got, err := applyFastCheapModel(profile, "")
	if err != nil {
		t.Fatalf("applyFastCheapModel: %v", err)
	}
	if got.CheapModel() != profile.CheapModel() {
		t.Fatalf("CheapModel() = %q, want %q", got.CheapModel(), profile.CheapModel())
	}
}
