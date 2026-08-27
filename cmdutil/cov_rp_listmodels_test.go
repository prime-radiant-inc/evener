package cmdutil

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
)

// listerAdapter is a ProviderAdapter that also implements llm.ModelLister,
// so ListModelsFunc can exercise the real client dispatch and mapping.
type listerAdapter struct {
	name   string
	models []llm.ModelInfo
	err    error
}

func (a *listerAdapter) Name() string { return a.name }
func (a *listerAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}
func (a *listerAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("not implemented")
}
func (a *listerAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return a.models, a.err
}

func TestListModelsFunc(t *testing.T) {
	c := llm.NewClient()
	supportsWebSearch := false
	c.Register(&listerAdapter{
		name: "stub",
		models: []llm.ModelInfo{
			{
				ID: "m1", DisplayName: "Model One", ContextWindow: 64_000,
				CapabilitiesAdvertised: true, SupportsVision: true,
				SupportsWebSearch: &supportsWebSearch,
			},
			{ID: "m2", DisplayName: "Model Two"},
		},
	})

	items, err := ListModelsFunc(c, "stub")(context.Background())
	if err != nil {
		t.Fatalf("ListModelsFunc: %v", err)
	}
	if len(items) != 2 || items[0].Model != "m1" || items[0].DisplayName != "Model One" ||
		items[1].Model != "m2" || items[1].DisplayName != "Model Two" {
		t.Fatalf("mapped items = %+v", items)
	}
	if items[0].ContextWindow == nil || *items[0].ContextWindow != 64_000 ||
		items[0].SupportsVision == nil || !*items[0].SupportsVision ||
		items[0].SupportsWebSearch == nil || *items[0].SupportsWebSearch {
		t.Fatalf("mapped metadata = %+v", items[0])
	}

	// An unknown provider surfaces the client's error through the closure.
	if _, err := ListModelsFunc(c, "nope")(context.Background()); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
