package llm

import (
	"primeradiant.com/evener/llm/registry"
)

// Price captures a model's per-million-token rates in USD.
//
// InputPerM and OutputPerM are always populated when Price is returned via
// PriceFromCost. The cache-tier fields are optional and are nil when the
// registry row has no corresponding rate for the model.
type Price struct {
	InputPerM           float64
	OutputPerM          float64
	CacheReadPerM       *float64
	CacheCreation5mPerM *float64
}

// PriceFromCost is the registry's cost as per-million rates; models.dev
// carries one cache-write rate, reported as the 5-minute tier. A nil cost is
// a row the registry has no price for, which is reported as (Price{}, false)
// so callers render nothing rather than a misleading zero (spec §7.5).
// registry.Cost.Tiers is deliberately not priced: a tiered row is charged at
// its base rate at every context size, which the plan chose over carrying a
// second rate table nothing else in the tree reads.
func PriceFromCost(c *registry.Cost) (Price, bool) {
	if c == nil {
		return Price{}, false
	}
	p := Price{InputPerM: c.Input, OutputPerM: c.Output}
	if c.CacheRead > 0 {
		p.CacheReadPerM = new(c.CacheRead)
	}
	if c.CacheWrite > 0 {
		p.CacheCreation5mPerM = new(c.CacheWrite)
	}
	return p, true
}

// EstimateCost returns the blended dollar cost of the given token counts at
// price's rates. Cache-read tokens price at CacheReadPerM when the row
// has one, else at the input rate (an accepted approximation, not a hard
// guarantee). Cache-creation cost is not counted: no caller here tracks a
// cache-creation token count (cache-read/write cost breakout is explicitly
// out of scope for the consistency-sweep cost feature).
func EstimateCost(inputTokens, cacheReadTokens, outputTokens int64, price Price) float64 {
	cacheReadRate := price.InputPerM
	if price.CacheReadPerM != nil {
		cacheReadRate = *price.CacheReadPerM
	}
	return float64(inputTokens)*price.InputPerM/1e6 +
		float64(cacheReadTokens)*cacheReadRate/1e6 +
		float64(outputTokens)*price.OutputPerM/1e6
}
