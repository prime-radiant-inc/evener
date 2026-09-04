package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestExecuteCallUnknownToolNameProjectionIsPrivateAndBounded pins the split
// between the raw provider name used for registry lookup/breaker identity and
// the display name returned to callers. Unknown names that satisfy the provider
// grammar remain useful diagnostics; unreadable names must not escape through
// ExecResult.ToolName after their diagnostic text has already been redacted.
func TestExecuteCallUnknownToolNameProjectionIsPrivateAndBounded(t *testing.T) {
	const (
		secret      = "EXEC_RESULT_PRIVATE_TOOL_NAME"
		displayName = "invalid tool name"
		readable    = "readable_unknown_tool"
	)
	invalid := strings.Repeat(secret, 300)

	t.Run("unreadable name", func(t *testing.T) {
		res := NewRegistry().ExecuteCall(context.Background(), breakerEnv(t), llm.ToolCallData{
			ID:        "invalid-name",
			Name:      invalid,
			Arguments: json.RawMessage(`{}`),
		})
		if !res.IsError {
			t.Fatalf("unknown tool result = %#v, want error", res)
		}
		if strings.Contains(res.Output, secret) || strings.Contains(res.FullOutput, secret) {
			t.Fatal("unknown-tool diagnostic leaked the unreadable provider name")
		}
		if got := res.ToolName; got != displayName {
			t.Errorf("ExecResult.ToolName is not the bounded display projection: bytes=%d contains_secret=%t want=%q", len(got), strings.Contains(got, secret), displayName)
		}
	})

	t.Run("readable unknown name", func(t *testing.T) {
		res := NewRegistry().ExecuteCall(context.Background(), breakerEnv(t), llm.ToolCallData{
			ID:        "readable-name",
			Name:      readable,
			Arguments: json.RawMessage(`{}`),
		})
		if res.ToolName != readable {
			t.Errorf("ExecResult.ToolName = %q, want readable unknown name %q", res.ToolName, readable)
		}
		if !strings.Contains(res.Output, readable) || !strings.Contains(res.FullOutput, readable) {
			t.Errorf("readable unknown name missing from diagnostics: output=%q full=%q", res.Output, res.FullOutput)
		}
	})
}
