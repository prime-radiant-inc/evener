package llm

import (
	"strings"
)

// Price captures a model's per-million-token rates in USD.
//
// Input and output are always populated when Price is returned via
// GetPrice. Cache-tier fields are optional — providers that don't
// distinguish (e.g. OpenAI has no cache-creation charge, Google has no
// 1-hour tier) leave them nil.
//
// Callers that need to bill a token breakdown compute cost by summing
// bucket_tokens * matching_rate. For token classes the provider doesn't
// charge separately (e.g. reasoning on OpenAI is billed as output),
// the caller routes those tokens through the output rate.
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
//  1. Exact match on ID or alias (via GetModelInfo).
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
	if mi := c.GetModelInfo(id); mi != nil {
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

// DefaultPrice looks up pricing via the embedded LiteLLM catalog.
// Equivalent to EmbeddedModelCatalog().GetPrice(modelID).
func DefaultPrice(modelID string) (Price, bool) {
	return EmbeddedModelCatalog().GetPrice(modelID)
}
