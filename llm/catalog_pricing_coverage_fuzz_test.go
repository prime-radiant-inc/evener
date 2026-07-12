package llm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// FuzzCatalogPricingCoverage replays deterministic edge cases for the catalog,
// pricing, rate-limit, media, and local token-counting surfaces. The selector
// keeps each corpus entry small while ensuring replay reaches every contract.
func FuzzCatalogPricingCoverage(f *testing.F) {
	for i := byte(0); i < 12; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		in, out, cached := 1.25, 5.0, 0.125
		cat := &ModelCatalog{Models: []ModelInfo{
			{ID: "claude-opus-4-5", Provider: "anthropic", ContextWindow: 200_000,
				InputCostPerMillion: &in, OutputCostPerMillion: &out, CacheReadInputCostPerMillion: &cached},
			{ID: "claude-opus-4", Provider: "anthropic", ContextWindow: 100_000,
				InputCostPerMillion: &in, OutputCostPerMillion: &out},
			{ID: "unpriced", Provider: "other", ContextWindow: 1},
		}}

		switch selector % 12 {
		case 0:
			for _, ref := range []string{
				"router/claude-opus-4-5", "router/claude-opus-4-5[1m]",
				"claude-opus-4-5-20260101", "missing/",
			} {
				_ = cat.LookupModelInfo(ref)
			}
		case 1:
			dir := t.TempDir()
			path := filepath.Join(dir, "catalog.json")
			if err := os.WriteFile(path, []byte(`{"m":{"litellm_provider":"openai"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadModelCatalogFromLiteLLMJSON(path); err != nil {
				t.Fatal(err)
			}
			_, _ = LoadModelCatalogFromLiteLLMJSON(filepath.Join(dir, "missing"))
		case 2:
			for _, raw := range []map[string]any{
				{"supports_minimal_reasoning_effort": true},
				{"supports_low_reasoning_effort": "false", "supports_xhigh_reasoning_effort": true},
				{"supports_max_reasoning_effort": true, "supports_medium_reasoning_effort": 1},
				{"supports_high_reasoning_effort": "invalid"},
			} {
				_ = synthesizeReasoningEffortLevels(raw)
			}
		case 3:
			applyOverrides(cat, []byte(`{"claude-opus-4-5":7,"new":{"context_window":12}}`))
			_ = overrideInt(7)
		case 4:
			for _, id := range []string{"", "missing", "unpriced", "claude-opus-4-5", "claude-opus-4-5-preview", "claude-opus-4-preview"} {
				_, _ = cat.GetPrice(id)
			}
			_, _ = (*ModelCatalog)(nil).GetPrice("m")
			_, _ = priceFromModelInfo(nil)
		case 5:
			_ = EstimateCost(1_000_000, 2_000_000, 3_000_000, Price{InputPerM: in, OutputPerM: out})
			_ = EstimateCost(1, 2, 3, Price{InputPerM: in, OutputPerM: out, CacheReadPerM: &cached})
			_, _ = DefaultPrice("gpt-4o")
		case 6:
			h := http.Header{}
			h.Set("x-ratelimit-reset-tokens", "2026-01-01T00:00:00Z")
			_ = ParseRateLimitHeaders(h)
		case 7:
			for _, path := range []string{"a.png", "a.JPG", "a.gif", "a.webp", "a.bmp", "a.svg"} {
				_ = IsImageFile(path)
			}
		case 8:
			_ = estimateImageTokens("", "", nil)
			_ = estimateImageTokens("", "", &ImageData{Data: []byte("not-image")})
		case 9:
			path := filepath.Join(t.TempDir(), "bad.png")
			if err := os.WriteFile(path, []byte("not-image"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, _ = imageDimensions(&ImageData{URL: path})
			_, _, _ = imageDimensions(&ImageData{URL: filepath.Join(t.TempDir(), "missing.png")})
		case 10:
			client := NewClient()
			client.Register(&fuzzPlainAdapter{name: "plain"})
			_, _ = client.CountInputTokens(context.Background(), Request{Provider: "plain", Model: "m", Messages: []Message{User("x")}})
		case 11:
			client := NewClient()
			_, _ = client.CountInputTokens(context.Background(), Request{Model: "m", Messages: []Message{User("x")}})
			_, _ = client.CountInputTokens(context.Background(), Request{Provider: "unknown", Model: "m", Messages: []Message{User("x")}})

			readErr := errors.New("read failed")
			_ = loadEmbeddedModelCatalog(func(string) ([]byte, error) { return nil, readErr }, parseLiteLLMCatalog)
			_ = loadEmbeddedModelCatalog(func(string) ([]byte, error) { return []byte(`{}`), nil }, func([]byte) (*ModelCatalog, error) { return nil, nil })
			_ = loadEmbeddedModelCatalog(func(name string) ([]byte, error) {
				if name == "data/litellm_model_catalog.json" {
					return []byte(`{"m":{"litellm_provider":"openai"}}`), nil
				}
				return nil, readErr
			}, parseLiteLLMCatalog)
		}
	})
}

type fuzzPlainAdapter struct{ name string }

func (a *fuzzPlainAdapter) Name() string { return a.name }
func (a *fuzzPlainAdapter) Complete(context.Context, Request) (Response, error) {
	return Response{}, nil
}
func (a *fuzzPlainAdapter) Stream(context.Context, Request) (Stream, error) {
	return doneStream{}, nil
}
