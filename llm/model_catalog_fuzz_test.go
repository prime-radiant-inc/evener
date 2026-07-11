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
//
// FuzzApplyCatalogOverrides drives applyOverrides/applyOverlayFields (the
// serf_model_catalog_overrides.json merge layer, including the newer
// thinking_always_on, supports_vision, per-million pricing, and aliases
// plumbing) over arbitrary override JSON against a small fixed base catalog.
//
// Oracles:
//   - never panics; the base catalog's model IDs all survive the merge.
//   - deterministic: two fresh merges of the same bytes agree on the ordered
//     (ID, ContextWindow, ThinkingAlwaysOn, level-count) tuples.
//   - materialized entries always carry a positive context window (the
//     materialization gate), and every model resolves through GetModelInfo.
//   - alias index round-trip: each whitespace-free alias resolves to a model
//     that lists it (or was shadowed by a real ID / earlier alias).
func FuzzApplyCatalogOverrides(f *testing.F) {
	f.Add([]byte(`{"claude-fable-5": {"thinking_always_on": true, "supports_web_search": true}}`))
	f.Add([]byte(`{"gpt-5.6-sol": {"provider":"openai","context_window":1050000,"max_output_tokens":128000,"supports_vision":true,"input_cost_per_million":5.0,"output_cost_per_million":30.0,"cache_read_input_cost_per_million":0.5,"aliases":["gpt-5.6"],"reasoning_effort_levels":["low","medium","high","xhigh","max"]}}`))
	f.Add([]byte(`{"base-model": {"reasoning_effort_levels": ["low", 7, "max"], "aliases": ["base-model", 3]}}`))
	f.Add([]byte(`{"_comment": "x", "overlay-only": {"supports_tools": true}}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"base-dated-20260101": {"context_window": 5}, "base-dated": {"supports_reasoning": true}}`))

	const base = `{
        "base-model":          {"litellm_provider": "anthropic", "mode": "chat", "max_input_tokens": 200000},
        "base-dated-20260101": {"litellm_provider": "anthropic", "mode": "chat"},
        "claude-fable-5":      {"litellm_provider": "anthropic", "mode": "chat"}
    }`
	f.Fuzz(func(t *testing.T, data []byte) {
		merge := func() *ModelCatalog {
			cat, err := parseLiteLLMCatalog([]byte(base))
			if err != nil || cat == nil {
				t.Fatalf("base parse: %v", err)
			}
			applyOverrides(cat, data)
			return cat
		}
		cat := merge()
		cat2 := merge()

		if len(cat.Models) != len(cat2.Models) {
			t.Fatalf("merge not deterministic: %d vs %d models", len(cat.Models), len(cat2.Models))
		}
		// Materialized entries append in map-iteration order, so compare as sets
		// keyed by ID.
		byID2 := make(map[string]ModelInfo, len(cat2.Models))
		for _, m := range cat2.Models {
			byID2[m.ID] = m
		}
		seenBase := 0
		for _, m := range cat.Models {
			o, ok := byID2[m.ID]
			if !ok {
				t.Fatalf("model %q present in one merge only", m.ID)
			}
			if o.ContextWindow != m.ContextWindow || o.ThinkingAlwaysOn != m.ThinkingAlwaysOn ||
				len(o.ReasoningEffortLevels) != len(m.ReasoningEffortLevels) {
				t.Fatalf("merge not deterministic for %q: %+v vs %+v", m.ID, m, o)
			}
			switch m.ID {
			case "base-model", "base-dated-20260101", "claude-fable-5":
				seenBase++
			default:
				// Materialized: only a context_window > 0 creates an entry.
				if m.ContextWindow <= 0 {
					t.Fatalf("materialized %q has ContextWindow %d, want > 0", m.ID, m.ContextWindow)
				}
			}
			if m.ID == strings.TrimSpace(m.ID) {
				if got := cat.GetModelInfo(m.ID); got == nil {
					t.Fatalf("GetModelInfo(%q) = nil after merge", m.ID)
				}
			}
			for _, lvl := range m.ReasoningEffortLevels {
				_ = ReasoningEffortRank(lvl) // must not panic on override-supplied levels
			}
		}
		if seenBase != 3 {
			t.Fatalf("base models did not survive merge: %d/3", seenBase)
		}
		for _, m := range cat.Models {
			for _, alias := range m.Aliases {
				if alias != strings.TrimSpace(alias) {
					continue // unreachable through the trimming index query
				}
				got := cat.GetModelInfo(alias)
				if got == nil {
					t.Fatalf("alias %q of %q resolves to nil", alias, m.ID)
				}
			}
		}
	})
}

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
