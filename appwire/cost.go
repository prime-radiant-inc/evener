package appwire

import (
	"fmt"

	"primeradiant.com/serf/llm"
)

// SerfUsageFromLLM converts a raw llm.Usage into the wire SerfUsage shape,
// returning nil when every total (including CacheReadTokens) is zero so
// callers hide the usage cluster rather than render ↑0 ↓0 — the established
// WS2 convention (mirrors cmd/serf/serve.go's serfUsageFromLLM and
// cmd/serf-hub's serfUsageFromCumulative; this is the appwire-level home the
// other two should eventually delegate to).
func SerfUsageFromLLM(u llm.Usage) *SerfUsage {
	cacheRead := int64(0)
	if u.CacheReadTokens != nil {
		cacheRead = int64(*u.CacheReadTokens)
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && cacheRead == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &SerfUsage{
		InputTokens:     int64(u.InputTokens),
		OutputTokens:    int64(u.OutputTokens),
		CacheReadTokens: cacheRead,
		TotalTokens:     int64(u.TotalTokens),
	}
}

// EstimateCost returns a "~$X.XX" estimate of model's total cost for usage,
// via llm's embedded-catalog pricing (llm.DefaultPrice — GetPrice's first
// real caller). Returns "" when usage is nil or the model has no catalog
// pricing, so callers render nothing rather than a misleading "~$0.00" for
// an uncataloged model. The "~" marks every non-empty result as an
// estimate, not a billing-exact figure.
func EstimateCost(model string, usage *SerfUsage) string {
	if usage == nil {
		return ""
	}
	price, ok := llm.DefaultPrice(model)
	if !ok {
		return ""
	}
	dollars := llm.EstimateCost(usage.InputTokens, usage.CacheReadTokens, usage.OutputTokens, price)
	return fmt.Sprintf("~$%.2f", dollars)
}
