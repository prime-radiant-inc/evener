package llm

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestEstimateInputTokens_StampedToolBytesMatchTheUnstampedEstimate pins the
// cache to the estimate it replaces: a request stamped with the tool list's
// serialized length must price exactly what re-marshalling the list prices, so
// no compaction threshold, admission decision or continuation plan moves when
// the stamp is populated (#6406).
func TestEstimateInputTokens_StampedToolBytesMatchTheUnstampedEstimate(t *testing.T) {
	t.Parallel()
	tools := benchmarkToolDefs(32)
	unstamped := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{User("hello")},
		Tools:    tools,
	}
	stamped := unstamped
	stamped.ToolsJSONBytes = ToolsJSONBytes(tools)
	if stamped.ToolsJSONBytes <= 0 {
		t.Fatalf("ToolsJSONBytes = %d, want a positive length for a non-empty list", stamped.ToolsJSONBytes)
	}

	want := EstimateInputTokens(unstamped).Tokens
	if got := EstimateInputTokens(stamped).Tokens; got != want {
		t.Fatalf("stamped estimate = %d, want %d: the stamp must be byte-identical to the marshal it replaces", got, want)
	}
	// The resolved path is the one the session uses, and it must agree too.
	res := registry.Resolved{Protocol: registry.ProtocolAnthropic}
	if got := EstimateInputTokensForResolved(res, stamped).Tokens; got != EstimateInputTokensForResolved(res, unstamped).Tokens {
		t.Fatalf("resolved stamped estimate = %d, want %d", got, EstimateInputTokensForResolved(res, unstamped).Tokens)
	}
}

// TestEstimateInputTokens_ZeroStampFallsBackToMarshalling pins that an
// unstamped request still bills its tools: the zero value means "not
// measured", not "no tools", and a caller that never stamps must keep the
// price it always had.
func TestEstimateInputTokens_ZeroStampFallsBackToMarshalling(t *testing.T) {
	t.Parallel()
	tools := benchmarkToolDefs(4)
	bare := Request{Model: "m", Messages: []Message{User("hi")}}
	withTools := bare
	withTools.Tools = tools

	want := len(mustMarshal(t, tools)) / 4
	if got := EstimateInputTokens(withTools).Tokens - EstimateInputTokens(bare).Tokens; got != want {
		t.Fatalf("unstamped tool share = %d, want %d", got, want)
	}
}

// TestEstimateInputTokens_ToolsJSONBytesCountsTheWireForm pins the helper the
// stamp is measured with: it reports exactly the length json.Marshal produces
// for the list the estimate bills, and 0 for an empty one.
func TestEstimateInputTokens_ToolsJSONBytesCountsTheWireForm(t *testing.T) {
	t.Parallel()
	if got := ToolsJSONBytes(nil); got != 0 {
		t.Fatalf("ToolsJSONBytes(nil) = %d, want 0", got)
	}
	if got := ToolsJSONBytes([]ToolDefinition{}); got != 0 {
		t.Fatalf("ToolsJSONBytes(empty) = %d, want 0", got)
	}
	tools := benchmarkToolDefs(3)
	if got, want := ToolsJSONBytes(tools), len(mustMarshal(t, tools)); got != want {
		t.Fatalf("ToolsJSONBytes = %d, want %d", got, want)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
