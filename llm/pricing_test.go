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

func TestEstimateCost_BlendsCacheReadAtItsOwnRate(t *testing.T) {
	price := Price{InputPerM: 5.0, OutputPerM: 25.0, CacheReadPerM: f64(0.5)}
	got := EstimateCost(1_000_000, 1_000_000, 1_000_000, price)
	want := 5.0 + 0.5 + 25.0 // one million tokens of each tier
	if !approxF(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEstimateCost_CacheReadFallsBackToInputRateWhenUncataloged(t *testing.T) {
	price := Price{InputPerM: 5.0, OutputPerM: 25.0} // no CacheReadPerM
	got := EstimateCost(0, 1_000_000, 0, price)
	if !approxF(got, 5.0) {
		t.Errorf("got %v, want 5.0 (cache-read priced at input rate)", got)
	}
}

func TestCostParity_GetPriceMatchesDirectModelInfoFieldReads(t *testing.T) {
	cat := EmbeddedModelCatalog()
	// Representative ids INCLUDING the two shapes P1 fixed: a provider-qualified
	// ref and a "[1m]"-suffixed ref. These must resolve identically whether cost
	// comes from GetPrice (this track's path) or from a direct ModelInfo field
	// read (Track B's picker path).
	ids := []string{
		"claude-opus-4-5",
		"anthropic/claude-opus-4-5", // provider-qualified (P1)
		"claude-opus-4-5[1m]",       // 1M-context suffix (P1)
		"gpt-5-codex",
	}
	const inTok, cacheTok, outTok = int64(123_456), int64(0), int64(7_890)
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			// This track's path.
			price, ok := cat.GetPrice(id)
			if !ok {
				t.Fatalf("GetPrice(%q) returned !ok — id should resolve after P1", id)
			}
			viaGetPrice := EstimateCost(inTok, cacheTok, outTok, price)

			// The picker's path: resolve ModelInfo the same way catalogModelInfo
			// does (LookupModelInfo), then read its cost fields directly.
			mi := cat.LookupModelInfo(id)
			if mi == nil || mi.InputCostPerMillion == nil || mi.OutputCostPerMillion == nil {
				t.Fatalf("LookupModelInfo(%q) missing base cost fields", id)
			}
			viaDirectFields := float64(inTok)*(*mi.InputCostPerMillion)/1e6 +
				float64(outTok)*(*mi.OutputCostPerMillion)/1e6
			// cacheTok is 0 here, so cache-rate differences don't enter — this
			// isolates the base-rate parity that both paths must agree on.

			if !approxF(viaGetPrice, viaDirectFields) {
				t.Errorf("cost parity mismatch for %q: GetPrice path=%v, direct-field path=%v", id, viaGetPrice, viaDirectFields)
			}
		})
	}
}

func approxF(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}
