package google

import "testing"

// Gemini's streamGenerateContent commonly carries usageMetadata on the SAME
// final chunk as the candidate's finishReason (the dominant shape in the API and
// in this package's own fixtures). The streaming decoder must capture that usage,
// not only usage that arrives in a separate earlier chunk.
func TestStream_UsageOnFinishChunk(t *testing.T) {
	a := &Adapter{APIKey: "k"}
	sse := []byte("data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}` + "\n\n")

	resp, sawErr := accumulateGeminiSSE(a, sse, false)
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
