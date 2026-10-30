package google

import "testing"

// Gemini's streamGenerateContent commonly carries usageMetadata on the SAME
// final chunk as the candidate's finishReason (the dominant shape in the API and
// in this package's own fixtures). The streaming decoder must capture that usage,
// not only usage that arrives in a separate earlier chunk.
func TestStream_UsageOnFinishChunk(t *testing.T) {
	sse := []byte("data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}` + "\n\n")

	resp, sawErr := accumulateGeminiSSE(sse, false)
	if sawErr {
		t.Fatal("unexpected stream error")
	}
	if resp == nil {
		t.Fatal("nil accumulated response")
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 22 || resp.Usage.TotalTokens != 33 {
		t.Fatalf("usage on finish chunk dropped: got in=%d out=%d total=%d, want 11/22/33",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
	}
}

// TestStream_UsageExcludesCachedContent covers the streaming parseUsage call
// site with cached usage. Gemini's promptTokenCount includes
// cachedContentTokenCount, so the accumulated usage must report only the
// uncached remainder as InputTokens and keep the cached count separate.
func TestStream_UsageExcludesCachedContent(t *testing.T) {
	sse := []byte("data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":40,"candidatesTokenCount":10,"totalTokenCount":117,"thoughtsTokenCount":7}}` + "\n\n")

	resp, sawErr := accumulateGeminiSSE(sse, false)
	if sawErr {
		t.Fatal("unexpected stream error")
	}
	if resp == nil {
		t.Fatal("nil accumulated response")
	}
	if resp.Usage.InputTokens != 60 || resp.Usage.OutputTokens != 10 || resp.Usage.TotalTokens != 117 {
		t.Fatalf("usage = in=%d out=%d total=%d, want 60/10/117",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 40 {
		t.Fatalf("CacheReadTokens = %v, want 40", resp.Usage.CacheReadTokens)
	}
	if got := resp.Usage.InputTokens + *resp.Usage.CacheReadTokens; got != 100 {
		t.Fatalf("InputTokens + CacheReadTokens = %d, want promptTokenCount 100", got)
	}
}
