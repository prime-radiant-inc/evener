package llm

import (
	"testing"
)

func TestGetPrice_ExactMatch(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{
				ID:                            "claude-opus-4-5",
				Provider:                      "anthropic",
				InputCostPerMillion:           f64(5.0),
				OutputCostPerMillion:          f64(25.0),
				CacheReadInputCostPerMillion:  f64(0.5),
				CacheCreation5mCostPerMillion: f64(6.25),
				CacheCreation1hCostPerMillion: f64(10.0),
			},
		},
	}
	p, ok := cat.GetPrice("claude-opus-4-5")
	if !ok {
		t.Fatalf("GetPrice returned false for known model")
	}
	if p.InputPerM != 5.0 || p.OutputPerM != 25.0 {
		t.Errorf("base rates: got in=%v out=%v, want 5/25", p.InputPerM, p.OutputPerM)
	}
	if p.CacheReadPerM == nil || *p.CacheReadPerM != 0.5 {
		t.Errorf("cache_read: got %v, want 0.5", p.CacheReadPerM)
	}
	if p.CacheCreation5mPerM == nil || *p.CacheCreation5mPerM != 6.25 {
		t.Errorf("cache_create_5m: got %v, want 6.25", p.CacheCreation5mPerM)
	}
	if p.CacheCreation1hPerM == nil || *p.CacheCreation1hPerM != 10.0 {
		t.Errorf("cache_create_1h: got %v, want 10.0", p.CacheCreation1hPerM)
	}
}

func TestGetPrice_LongestPrefix(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
			{ID: "claude-opus-4", InputCostPerMillion: f64(15.0), OutputCostPerMillion: f64(75.0)},
			{ID: "claude", InputCostPerMillion: f64(100.0), OutputCostPerMillion: f64(500.0)},
		},
	}
	// "claude-opus-4-5-20260101" should resolve to claude-opus-4-5 (longer prefix wins over claude-opus-4).
	p, ok := cat.GetPrice("claude-opus-4-5-20260101")
	if !ok {
		t.Fatal("expected match via longest prefix")
	}
	if p.InputPerM != 5.0 {
		t.Errorf("got input=%v, want 5.0 (claude-opus-4-5)", p.InputPerM)
	}
}

func TestGetPrice_ProviderQualifiedRef(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
		},
	}
	// Real stored session model ids can carry a provider namespace the
	// catalog's bare key never sees (agent/provider/profile.go:505-528 keeps
	// the namespace for meta-provider upstreams like openrouter/minimax).
	p, ok := cat.GetPrice("anthropic/claude-opus-4-5")
	if !ok {
		t.Fatal("expected provider-qualified ref to resolve via LookupModelInfo")
	}
	if p.InputPerM != 5.0 || p.OutputPerM != 25.0 {
		t.Errorf("got in=%v out=%v, want 5/25", p.InputPerM, p.OutputPerM)
	}
}

func TestGetPrice_OneMillionContextSuffix(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
		},
	}
	// The "[1m]" suffix (agent/provider/profile.go:743,
	// llm/providers/anthropic/models.go:90) selects the 1M-context beta but
	// carries no separate pricing entry — it must resolve to the base model.
	p, ok := cat.GetPrice("claude-opus-4-5[1m]")
	if !ok {
		t.Fatal("expected [1m]-suffixed ref to resolve via LookupModelInfo")
	}
	if p.InputPerM != 5.0 {
		t.Errorf("got in=%v, want 5.0", p.InputPerM)
	}
}

func TestGetPrice_UnknownModel(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "claude-opus-4-5", InputCostPerMillion: f64(5.0), OutputCostPerMillion: f64(25.0)},
		},
	}
	if _, ok := cat.GetPrice("totally-different-provider-model"); ok {
		t.Error("expected false for unknown model")
	}
	if _, ok := cat.GetPrice(""); ok {
		t.Error("expected false for empty string")
	}
}

func TestGetPrice_MissingBaseRates(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			// known model but no pricing (like claude-sonnet-4-6 in the real catalog)
			{ID: "claude-sonnet-4-6", Provider: "anthropic"},
			// parent family with pricing
			{ID: "claude-sonnet-4-5", InputCostPerMillion: f64(3.0), OutputCostPerMillion: f64(15.0)},
		},
	}
	// Exact match fails (no base rates); no prefix fallback either since claude-sonnet-4-6
	// is not a prefix of claude-sonnet-4-5 and vice versa.
	if _, ok := cat.GetPrice("claude-sonnet-4-6"); ok {
		t.Error("expected false: model known but has no pricing, no fallback")
	}
}

func TestDefaultPrice_WellKnownModels(t *testing.T) {
	cases := []struct {
		model     string
		wantIn    float64
		wantOut   float64
		wantCRead float64 // 0 = don't check (model may not have cache pricing in catalog)
	}{
		{model: "claude-opus-4-5", wantIn: 5.0, wantOut: 25.0, wantCRead: 0.5},
		{model: "claude-sonnet-4-5", wantIn: 3.0, wantOut: 15.0, wantCRead: 0.3},
		{model: "claude-haiku-4-5", wantIn: 1.0, wantOut: 5.0, wantCRead: 0.1},
		{model: "gpt-5-codex", wantIn: 1.25, wantOut: 10.0, wantCRead: 0.125},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			p, ok := DefaultPrice(tc.model)
			if !ok {
				t.Fatalf("%s not in embedded catalog", tc.model)
			}
			if approxF(p.InputPerM, tc.wantIn) == false {
				t.Errorf("input: got %v, want %v", p.InputPerM, tc.wantIn)
			}
			if approxF(p.OutputPerM, tc.wantOut) == false {
				t.Errorf("output: got %v, want %v", p.OutputPerM, tc.wantOut)
			}
			if tc.wantCRead > 0 {
				if p.CacheReadPerM == nil || !approxF(*p.CacheReadPerM, tc.wantCRead) {
					t.Errorf("cache_read: got %v, want %v", p.CacheReadPerM, tc.wantCRead)
				}
			}
		})
	}
}

func TestDefaultPrice_DatedSnapshot(t *testing.T) {
	// claude-opus-4-5-20251101 exists in the catalog as its own entry;
	// either exact match or prefix fallback should resolve it.
	p, ok := DefaultPrice("claude-opus-4-5-20251101")
	if !ok {
		t.Skip("claude-opus-4-5-20251101 not in embedded catalog")
	}
	if !approxF(p.InputPerM, 5.0) {
		t.Errorf("dated snapshot input: got %v, want 5.0", p.InputPerM)
	}
}

func approxF(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}
