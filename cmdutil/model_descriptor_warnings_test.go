package cmdutil

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A resolved row's warnings (e.g. Vertex's "regional location cannot call a
// global-only model" note) must survive the mapping to the wire descriptor so
// the web model picker can flag the row. Dropping them here is what kept the
// note out of the hub UI.
func TestModelDescriptorFromResolvedCarriesWarnings(t *testing.T) {
	res := registry.Resolved{
		Instance: "vertex",
		ModelID:  "gemini-3.5-flash",
		Warnings: []string{"model gemini-3.5-flash is served only from the global Vertex endpoint; the configured regional location us-central1 will 404"},
	}

	got := ModelDescriptorFromResolved(res)

	if !slices.Equal(got.Warnings, res.Warnings) {
		t.Fatalf("Warnings=%v, want %v", got.Warnings, res.Warnings)
	}
}

// A row with no warnings must not fabricate any: the field stays nil so the
// wire omits it and the picker renders an unadorned row.
func TestModelDescriptorFromResolvedOmitsAbsentWarnings(t *testing.T) {
	got := ModelDescriptorFromResolved(registry.Resolved{Instance: "openai", ModelID: "gpt-5.5"})

	if len(got.Warnings) != 0 {
		t.Fatalf("Warnings=%v, want none", got.Warnings)
	}
}
