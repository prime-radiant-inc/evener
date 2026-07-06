package llm

import (
	"strings"
)

// Price captures a model's per-million-token rates in USD.
//
// InputPerM and OutputPerM are always populated when Price is returned via
// GetPrice. The cache-tier fields are optional and are nil when the catalog
// has no corresponding rate for the model (e.g. OpenAI models generally
// carry no cache-creation rate, and Google models carry no 1-hour rate).
type Price struct {
	InputPerM           float64
	OutputPerM          float64
	CacheReadPerM       *float64
	CacheCreation5mPerM *float64
	CacheCreation1hPerM *float64
}

// GetPrice looks up pricing for a model ID. Returns (price, true) when the
// model is in the catalog with at least input+output rates. Unknown models
// and models missing base rates return (Price{}, false).
//
// Lookup order:
//  1. Resolve via LookupModelInfo, which handles exact/alias matches plus
//     provider-qualified refs (e.g. "anthropic/claude-opus-4-5") and
//     "[1m]"-suffixed refs.
//  2. Longest-prefix match over all model IDs. This handles date-stamped
//     snapshots like "claude-opus-4-5-20260101" resolving to the
//     "claude-opus-4-5" family when only the family entry has pricing.
func (c *ModelCatalog) GetPrice(modelID string) (Price, bool) {
	if c == nil {
		return Price{}, false
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		return Price{}, false
	}
	if mi := c.LookupModelInfo(id); mi != nil {
		if p, ok := priceFromModelInfo(mi); ok {
			return p, true
		}
	}
	// Longest-prefix fallback: pick the longest catalog ID that is a
	// prefix of modelID and has pricing.
	var bestKey string
	var bestPrice Price
	var bestFound bool
	for _, m := range c.Models {
		if !strings.HasPrefix(id, m.ID) {
			continue
		}
		if len(m.ID) <= len(bestKey) {
			continue
		}
		p, ok := priceFromModelInfo(&m)
		if !ok {
			continue
		}
		bestKey = m.ID
		bestPrice = p
		bestFound = true
	}
	return bestPrice, bestFound
}

func priceFromModelInfo(mi *ModelInfo) (Price, bool) {
	if mi == nil || mi.InputCostPerMillion == nil || mi.OutputCostPerMillion == nil {
		return Price{}, false
	}
	return Price{
		InputPerM:           *mi.InputCostPerMillion,
		OutputPerM:          *mi.OutputCostPerMillion,
		CacheReadPerM:       mi.CacheReadInputCostPerMillion,
		CacheCreation5mPerM: mi.CacheCreation5mCostPerMillion,
		CacheCreation1hPerM: mi.CacheCreation1hCostPerMillion,
	}, true
}

// EstimateCost returns the blended dollar cost of the given token counts at
// price's rates. Cache-read tokens price at CacheReadPerM when the catalog
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

// DefaultPrice looks up pricing via the embedded LiteLLM catalog.
// Equivalent to EmbeddedModelCatalog().GetPrice(modelID).
func DefaultPrice(modelID string) (Price, bool) {
	return EmbeddedModelCatalog().GetPrice(modelID)
}
