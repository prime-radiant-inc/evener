package cmdutil

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// listerAdapter is a ProviderAdapter that also implements llm.LiveModelLister,
// so ListModelsFunc can exercise the real client dispatch and mapping.
type listerAdapter struct {
	name   string
	models []registry.Model
	err    error
}

func (a *listerAdapter) Name() string { return a.name }
func (a *listerAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}
func (a *listerAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("not implemented")
}
func (a *listerAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	return a.models, a.err
}

func TestListModelsFunc(t *testing.T) {
	c := llm.NewClient()
	c.Register(&listerAdapter{
		name: "stub",
		models: []registry.Model{
			{ID: "m1", Caps: registry.Caps{
				ContextWindow:   new(64_000),
				MaxInputTokens:  new(48_000),
				MaxOutputTokens: new(8_192),
				Tools:           new(true),
				Reasoning:       new(true),
				InputModalities: []string{"text", "image"},
				WebSearch:       new(false),
				EffortValues:    []string{"low", "high"},
				Cost:            &registry.Cost{Input: 1.5, Output: 7.5},
			}},
			{ID: "m2"},
		},
	})

	items, err := ListModelsFunc(c, "stub")(context.Background())
	if err != nil {
		t.Fatalf("ListModelsFunc: %v", err)
	}
	if len(items) != 2 || items[0].Model != "m1" || items[1].Model != "m2" ||
		items[0].Provider != "stub" || items[1].Provider != "stub" {
		t.Fatalf("mapped items = %+v", items)
	}
	first := items[0]
	if first.ContextWindow == nil || *first.ContextWindow != 64_000 ||
		first.MaxInputTokens == nil || *first.MaxInputTokens != 48_000 ||
		first.MaxOutputTokens == nil || *first.MaxOutputTokens != 8_192 ||
		first.SupportsTools == nil || !*first.SupportsTools ||
		first.SupportsReasoning == nil || !*first.SupportsReasoning ||
		first.SupportsVision == nil || !*first.SupportsVision ||
		first.SupportsWebSearch == nil || *first.SupportsWebSearch {
		t.Fatalf("mapped metadata = %+v", first)
	}
	if first.InputCostPerMillion == nil || *first.InputCostPerMillion != 1.5 ||
		first.OutputCostPerMillion == nil || *first.OutputCostPerMillion != 7.5 {
		t.Fatalf("mapped cost = %+v", first)
	}
	if len(first.ReasoningEffortLevels) != 2 || first.ReasoningEffortLevels[0] != "low" {
		t.Fatalf("mapped effort levels = %v", first.ReasoningEffortLevels)
	}
	// A row that advertised nothing carries no optional facts at all.
	if items[1].ContextWindow != nil || items[1].SupportsTools != nil || len(items[1].ReasoningEffortLevels) != 0 {
		t.Fatalf("bare row mapped optional facts = %+v", items[1])
	}

	// An unknown provider surfaces the client's error through the closure.
	if _, err := ListModelsFunc(c, "nope")(context.Background()); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
