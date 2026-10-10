package registry

import (
	"slices"
	"testing"
)

const additiveModelOverlay = `
[providers.openai.models."overlay-only"]
context_window = 222000
`

const additiveModelConfig = `
[providers.openai.models."user-only"]
context_window = 333000
`

func additiveModelRegistry(t *testing.T) *Registry {
	t.Helper()
	return fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "sk-test"}, additiveModelConfig, WithOverlay(overlayWith(additiveModelOverlay)))
}

func TestModelIDs_LiveHidesBaseCatalogAndKeepsAdditiveRows(t *testing.T) {
	r := additiveModelRegistry(t)
	r.ApplyLive("openai", []Model{{ID: "live-only"}})

	got, err := r.ModelIDs("openai")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"live-only", "overlay-only", "user-only"} {
		if !slices.Contains(got, want) {
			t.Errorf("ModelIDs(openai) = %v, want additive row %q", got, want)
		}
	}
	if slices.Contains(got, "gpt-4o") {
		t.Fatalf("ModelIDs(openai) = %v, base snapshot row gpt-4o must be hidden by a usable live listing", got)
	}
}

func TestModelIDs_EmptyLiveListingFallsBackToBaseCatalog(t *testing.T) {
	r := additiveModelRegistry(t)
	r.ApplyLive("openai", nil)

	got, err := r.ModelIDs("openai")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gpt-4o", "overlay-only", "user-only"} {
		if !slices.Contains(got, want) {
			t.Errorf("ModelIDs(openai) = %v, want fallback row %q", got, want)
		}
	}
}
