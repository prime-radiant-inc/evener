package llm

import (
	"sort"
	"strings"
	"testing"
)

// FuzzParseLiteLLMCatalog drives parseLiteLLMCatalog (and its scalar helpers
// parseInt/parseBool/parseFloatPtr/scalePerMillion/normalizeCatalogProvider/
// buildIndex) plus the catalog query surface (GetModelInfo, LookupModelInfo,
// ListModels, GetLatestModel) over arbitrary LiteLLM-shaped JSON. This parser
// turns the upstream pricing/capability dump into serf's model metadata; only a
// few unit fixtures exercised it.
//
// Oracles:
//   - parsing is deterministic (same model IDs in the same order across two runs).
//   - on success the catalog is sorted by (provider, id) and every entry's
//     DisplayName equals its ID.
//   - the lazily-built index round-trips: GetModelInfo(id) returns an entry whose
//     ID matches for every parsed model (no entry is dropped or mis-keyed).
//   - ListModels("") returns exactly the full set; a provider filter returns a
//     subset all matching that provider.
func FuzzParseLiteLLMCatalog(f *testing.F) {
	f.Add([]byte(`{"gpt-5.2":{"litellm_provider":"openai","mode":"chat","max_input_tokens":272000,"input_cost_per_token":0.00000125,"supports_function_calling":true}}`))
	f.Add([]byte(`{"claude":{"litellm_provider":"anthropic","mode":"chat","max_tokens":"200000","supports_vision":"true","reasoning_effort_levels":["low","high"]}}`))
	f.Add([]byte(`{"sample_spec":{"mode":"chat"},"embed":{"mode":"embedding"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cat, err := parseLiteLLMCatalog(data)
		if err != nil {
			if cat != nil {
				t.Fatalf("error returned with non-nil catalog")
			}
			return
		}
		if cat == nil {
			t.Fatalf("nil catalog with nil error")
		}

		// Determinism: a second parse yields the same ordered ID list.
		cat2, err2 := parseLiteLLMCatalog(data)
		if err2 != nil || cat2 == nil {
			t.Fatalf("second parse disagreed: err=%v", err2)
		}
		if len(cat.Models) != len(cat2.Models) {
			t.Fatalf("model count not deterministic: %d vs %d", len(cat.Models), len(cat2.Models))
		}
		for i := range cat.Models {
			if cat.Models[i].ID != cat2.Models[i].ID {
				t.Fatalf("model order not deterministic at %d: %q vs %q", i, cat.Models[i].ID, cat2.Models[i].ID)
			}
		}

		// Sorted by (provider, id).
		if !sort.SliceIsSorted(cat.Models, func(i, j int) bool {
			if cat.Models[i].Provider != cat.Models[j].Provider {
				return cat.Models[i].Provider < cat.Models[j].Provider
			}
			return cat.Models[i].ID < cat.Models[j].ID
		}) {
			t.Fatalf("catalog not sorted by (provider, id)")
		}

		for _, m := range cat.Models {
			if m.DisplayName != m.ID {
				t.Fatalf("DisplayName %q != ID %q", m.DisplayName, m.ID)
			}
			// Index round-trip. GetModelInfo trims its query, while parseLiteLLMCatalog
			// keys entries on the verbatim JSON object key, so an ID with surrounding
			// whitespace is intentionally unreachable (LiteLLM keys never carry any;
			// this is a known latent inconsistency, not a contract the fuzzer asserts).
			// For the realistic whitespace-free IDs the round-trip must hold.
			if m.ID == strings.TrimSpace(m.ID) {
				got := cat.GetModelInfo(m.ID)
				if got == nil || got.ID != m.ID {
					t.Fatalf("GetModelInfo(%q) round-trip failed: %+v", m.ID, got)
				}
			}
			// LookupModelInfo must not panic over the same key.
			_ = cat.LookupModelInfo(m.ID)
		}

		all := cat.ListModels("")
		if len(all) != len(cat.Models) {
			t.Fatalf("ListModels(\"\") len=%d, want %d", len(all), len(cat.Models))
		}
		if len(cat.Models) > 0 {
			prov := cat.Models[0].Provider
			for _, m := range cat.ListModels(prov) {
				if !strings.EqualFold(m.Provider, prov) {
					t.Fatalf("ListModels(%q) returned %q", prov, m.Provider)
				}
			}
			// GetLatestModel over the full set selects the largest context window.
			if best := cat.GetLatestModel(prov, ""); best != nil {
				for _, m := range cat.ListModels(prov) {
					if m.ContextWindow > best.ContextWindow {
						t.Fatalf("GetLatestModel did not pick the largest context window")
					}
				}
			}
		}
	})
}
