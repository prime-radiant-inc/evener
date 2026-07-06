package appwire

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func TestSerfUsageFromLLM_NilWhenAllZero(t *testing.T) {
	if got := SerfUsageFromLLM(llm.Usage{}); got != nil {
		t.Errorf("got %+v, want nil for all-zero usage", got)
	}
}

func TestSerfUsageFromLLM_MapsFields(t *testing.T) {
	cacheRead := 7
	got := SerfUsageFromLLM(llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 10, CacheReadTokens: &cacheRead})
	if got == nil || got.InputTokens != 1 || got.OutputTokens != 2 || got.TotalTokens != 10 || got.CacheReadTokens != 7 {
		t.Errorf("got %+v, want {1 2 7 10}", got)
	}
}

func TestEstimateCost_NilUsageReturnsEmpty(t *testing.T) {
	if got := EstimateCost("claude-opus-4-5", nil); got != "" {
		t.Errorf("got %q, want empty for nil usage", got)
	}
}

func TestEstimateCost_UnpricedModelReturnsEmpty(t *testing.T) {
	got := EstimateCost("totally-unknown-model-xyz", &SerfUsage{InputTokens: 100})
	if got != "" {
		t.Errorf("got %q, want empty for an unpriced model (not a misleading ~$0.00)", got)
	}
}

func TestEstimateCost_FormatsToCents(t *testing.T) {
	// claude-opus-4-5: $5/$25 per million (llm/pricing_test.go's known fixture values
	// also hold for the embedded catalog per TestDefaultPrice_WellKnownModels).
	got := EstimateCost("claude-opus-4-5", &SerfUsage{InputTokens: 100_000, OutputTokens: 20_000})
	// 100_000/1e6*5 + 20_000/1e6*25 = 0.5 + 0.5 = 1.00
	if got != "~$1.00" {
		t.Errorf("got %q, want ~$1.00", got)
	}
}
