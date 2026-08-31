package appwire

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestEvenerUsageFromLLM_NilWhenAllZero(t *testing.T) {
	if got := EvenerUsageFromLLM(llm.Usage{}); got != nil {
		t.Errorf("got %+v, want nil for all-zero usage", got)
	}
}

func TestEvenerUsageFromLLM_MapsFields(t *testing.T) {
	cacheRead := 7
	got := EvenerUsageFromLLM(llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 10, CacheReadTokens: &cacheRead})
	if got == nil || got.InputTokens != 1 || got.OutputTokens != 2 || got.TotalTokens != 10 || got.CacheReadTokens != 7 {
		t.Errorf("got %+v, want {1 2 7 10}", got)
	}
}

func TestEstimateCost_NilUsageReturnsEmpty(t *testing.T) {
	if got := EstimateCost(&registry.Cost{Input: 5, Output: 25}, nil); got != "" {
		t.Errorf("got %q, want empty for nil usage", got)
	}
}

func TestEstimateCost_UnpricedModelReturnsEmpty(t *testing.T) {
	got := EstimateCost(nil, &EvenerUsage{InputTokens: 100})
	if got != "" {
		t.Errorf("got %q, want empty for a row with no cost (not a misleading ~$0.00)", got)
	}
}

func TestEstimateCost_FormatsToCents(t *testing.T) {
	// 1_000_000/1e6*3 + 100_000/1e6*15 = 3.00 + 1.50 = 4.50
	got := EstimateCost(&registry.Cost{Input: 3, Output: 15}, &EvenerUsage{InputTokens: 1_000_000, OutputTokens: 100_000})
	if got != "~$4.50" {
		t.Errorf("got %q, want ~$4.50", got)
	}
}

func TestEstimateCost_CacheReadPricesAtItsOwnRate(t *testing.T) {
	// 1_000_000/1e6*3 + 1_000_000/1e6*0.3 = 3.00 + 0.30 = 3.30
	got := EstimateCost(&registry.Cost{Input: 3, Output: 15, CacheRead: 0.3}, &EvenerUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000})
	if got != "~$3.30" {
		t.Errorf("got %q, want ~$3.30", got)
	}
}
