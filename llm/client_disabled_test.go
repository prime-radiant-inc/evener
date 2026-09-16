package llm_test

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestClientModelsDropsDisabledRows(t *testing.T) {
	r := fixtureRegistry(t, "http://127.0.0.1:9", map[string]registry.Provider{
		"nolist": {Base: "openai", APIKey: "k",
			Transport: registry.Transport{BaseURL: "http://127.0.0.1:9", ModelsEndpoint: registry.EndpointUnsupported},
			Models: map[string]registry.Model{
				"gone": {ID: "gone", Disabled: new(true)},
				"kept": {ID: "kept"},
			}},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	listing, err := c.Models(context.Background(), "nolist")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	for _, m := range listing.Models {
		if m.ModelID == "gone" {
			t.Fatalf("disabled row listed: %+v", listing.Models)
		}
	}
	if _, err := c.Resolve("nolist/gone"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve disabled = %v, want ErrModelDisabled", err)
	}
	if c.CanServe("nolist", "gone") {
		t.Fatal("CanServe(disabled) = true, want false")
	}
	if !c.CanServe("nolist", "kept") {
		t.Fatal("CanServe(kept) = false, want true")
	}
}
