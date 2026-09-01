package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestPriceFromCost_NilCost(t *testing.T) {
	p, ok := PriceFromCost(nil)
	if ok {
		t.Fatal("PriceFromCost(nil) reported a price")
	}
	if p != (Price{}) {
		t.Errorf("PriceFromCost(nil) price = %+v, want the zero Price", p)
	}
}

func TestPriceFromCost_BaseRatesOnly(t *testing.T) {
	p, ok := PriceFromCost(&registry.Cost{Input: 3, Output: 15})
	if !ok {
		t.Fatal("PriceFromCost returned false for a row with base rates")
	}
	if p.InputPerM != 3 || p.OutputPerM != 15 {
		t.Errorf("base rates: got in=%v out=%v, want 3/15", p.InputPerM, p.OutputPerM)
	}
	if p.CacheReadPerM != nil || p.CacheCreation5mPerM != nil {
		t.Errorf("cache tiers: got read=%v 5m=%v, want both nil", p.CacheReadPerM, p.CacheCreation5mPerM)
	}
}

func TestPriceFromCost_CacheRates(t *testing.T) {
	p, ok := PriceFromCost(&registry.Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75})
	if !ok {
		t.Fatal("PriceFromCost returned false for a row with cache rates")
	}
	if p.CacheReadPerM == nil || *p.CacheReadPerM != 0.3 {
		t.Errorf("cache_read: got %v, want 0.3", p.CacheReadPerM)
	}
	// models.dev carries one cache-write rate, reported as the 5-minute tier.
	if p.CacheCreation5mPerM == nil || *p.CacheCreation5mPerM != 3.75 {
		t.Errorf("cache_create_5m: got %v, want 3.75", p.CacheCreation5mPerM)
	}
}

func TestPriceFromCost_PricelessRowStillPrices(t *testing.T) {
	// An all-zero cost is a row that says its rates are zero, not a row with
	// no cost at all: only a nil *registry.Cost means "no price".
	p, ok := PriceFromCost(&registry.Cost{})
	if !ok {
		t.Fatal("PriceFromCost returned false for a present, all-zero cost")
	}
	if p.InputPerM != 0 || p.OutputPerM != 0 {
		t.Errorf("got %+v, want zero rates", p)
	}
}

func TestEstimateCost_BlendsCacheReadAtItsOwnRate(t *testing.T) {
	price := Price{InputPerM: 5.0, OutputPerM: 25.0, CacheReadPerM: new(0.5)}
	got := EstimateCost(1_000_000, 1_000_000, 1_000_000, price)
	want := 5.0 + 0.5 + 25.0 // one million tokens of each tier
	if !approxF(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEstimateCost_CacheReadFallsBackToInputRateWhenRowHasNoRate(t *testing.T) {
	price := Price{InputPerM: 5.0, OutputPerM: 25.0} // no CacheReadPerM
	got := EstimateCost(0, 1_000_000, 0, price)
	if !approxF(got, 5.0) {
		t.Errorf("got %v, want 5.0 (cache-read priced at input rate)", got)
	}
}

func approxF(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}
