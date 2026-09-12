package appwire

import (
	"fmt"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// EvenerUsageFromLLM converts a raw llm.Usage into the wire EvenerUsage shape,
// returning nil when every total (including CacheReadTokens) is zero so
// callers hide the usage cluster rather than render ↑0 ↓0 — the established
// WS2 convention (mirrors cmd/evener/serve.go's evenerUsageFromLLM and
// cmd/evener-hub's evenerUsageFromCumulative; this is the appwire-level home the
// other two should eventually delegate to).
func EvenerUsageFromLLM(u llm.Usage) *EvenerUsage {
	cacheRead := int64(0)
	if u.CacheReadTokens != nil {
		cacheRead = int64(*u.CacheReadTokens)
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && cacheRead == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &EvenerUsage{
		InputTokens:     int64(u.InputTokens),
		OutputTokens:    int64(u.OutputTokens),
		CacheReadTokens: cacheRead,
		TotalTokens:     int64(u.TotalTokens),
	}
}

// EstimateCost returns a "~$X.XX" estimate for usage at the registry row's
// cost (spec §7.5: cost comes from Resolved). Returns "" when usage is nil or
// the row carries no cost, so callers render nothing rather than a misleading
// "~$0.00" for a priceless row. The "~" marks every non-empty result as an
// estimate, not a billing-exact figure.
func EstimateCost(cost *registry.Cost, usage *EvenerUsage) string {
	if usage == nil {
		return ""
	}
	price, ok := llm.PriceFromCost(cost)
	if !ok {
		return ""
	}
	dollars := llm.EstimateCost(usage.InputTokens, usage.CacheReadTokens, usage.OutputTokens, price)
	return fmt.Sprintf("~$%.2f", dollars)
}
