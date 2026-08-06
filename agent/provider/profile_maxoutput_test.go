package provider

import (
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// Instance config wins over the catalog; catalog covers unconfigured models;
// unknown models report 0 so the adapter's own default governs.
func TestProfileMaxOutputTokens(t *testing.T) {
	catalogModel := "claude-sonnet-4-5"
	catalogCap := llm.EmbeddedModelCatalog().MaxOutputTokensFor(catalogModel)
	if catalogCap <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", catalogModel)
	}

	cases := []struct {
		name string
		p    *Profile
		want int
	}{
		{"instance config wins", &Profile{model: catalogModel, instModels: map[string]providercfg.ModelConfig{
			catalogModel: {MaxOutputTokens: 9000},
		}}, 9000},
		{"catalog fallback", &Profile{model: catalogModel}, catalogCap},
		{"unknown model", &Profile{model: "no-such-model-xyz"}, 0},
	}
	for _, tc := range cases {
		if got := tc.p.MaxOutputTokens(); got != tc.want {
			t.Errorf("%s: MaxOutputTokens() = %d, want %d", tc.name, got, tc.want)
		}
	}
}
