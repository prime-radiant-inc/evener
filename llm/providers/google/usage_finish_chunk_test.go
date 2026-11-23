package google

import "testing"

func TestStream_UsageOnInteractionCompleted(t *testing.T) {
	t.Parallel()
	sse := []byte("event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"hi\"}}\n\n" +
		"event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n" +
		"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"int_u1\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":11,\"total_output_tokens\":22,\"total_tokens\":33}}}\n\n")

	resp, sawErr := accumulateInteractionsSSE(sse, false)
	if sawErr {
		t.Fatal("unexpected stream error")
	}
	if resp == nil {
		t.Fatal("nil accumulated response")
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 22 || resp.Usage.TotalTokens != 33 {
		t.Fatalf("usage on interaction.completed dropped: got in=%d out=%d total=%d, want 11/22/33",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
	}
}

func TestStream_UsageExcludesCachedContent(t *testing.T) {
	t.Parallel()
	sse := []byte("event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"hi\"}}\n\n" +
		"event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n" +
		"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"int_u2\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":100,\"total_cached_tokens\":40,\"total_output_tokens\":10,\"total_thought_tokens\":7,\"total_tokens\":117}}}\n\n")

	resp, sawErr := accumulateInteractionsSSE(sse, false)
	if sawErr {
		t.Fatal("unexpected stream error")
	}
	if resp == nil {
		t.Fatal("nil accumulated response")
	}
	if resp.Usage.InputTokens != 60 || resp.Usage.OutputTokens != 17 || resp.Usage.TotalTokens != 117 {
		t.Fatalf("usage = in=%d out=%d total=%d, want 60/17/117",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 40 {
		t.Fatalf("CacheReadTokens = %v, want 40", resp.Usage.CacheReadTokens)
	}
	if got := resp.Usage.InputTokens + *resp.Usage.CacheReadTokens; got != 100 {
		t.Fatalf("InputTokens + CacheReadTokens = %d, want total_input_tokens 100", got)
	}
}
