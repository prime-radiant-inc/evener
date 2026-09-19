package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestManagedRawDispatchPreservesNumbersAndSeparatesHostResult(t *testing.T) {
	for _, domainError := range []bool{false, true} {
		r := NewRegistry()
		reached := false
		err := r.Register(RegisteredTool{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "managed", Description: "Managed boundary fixture", Parameters: map[string]any{"type": "object"}}}, OmitIntent: true,
			ValidateRaw: func(raw json.RawMessage) error {
				if !json.Valid(raw) || strings.Contains(string(raw), `"forbidden"`) {
					return errors.New("invalid")
				}
				return nil
			},
			ExecRaw: func(_ context.Context, _ execenv.ExecutionEnvironment, raw json.RawMessage) (any, error) {
				reached = true
				if string(raw) != `{"n":9007199254740993,"e":1e+03}` {
					t.Fatalf("lost raw bytes: %s", raw)
				}
				var err error
				if domainError {
					err = errors.New("domain")
				}
				return ManagedResult{Output: "model-data", Host: &llm.MCPResult{Version: 1, IsError: domainError, StructuredContent: json.RawMessage(`{"private":"host-only-sentinel"}`)}, InvocationID: "occurrence"}, err
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		result := r.ExecuteCall(t.Context(), nil, llm.ToolCallData{ID: "c", Name: "managed", Arguments: json.RawMessage(`{"n":9007199254740993,"e":1e+03}`)})
		if !reached || result.IsError != domainError || result.MCPResult == nil || result.ManagedInvocationID != "occurrence" {
			t.Fatalf("result=%+v", result)
		}
		if result.Output != "model-data" || strings.Contains(result.FullOutput, "host-only-sentinel") {
			t.Fatalf("model projection leaked: %+v", result)
		}
		reached = false
		r.ExecuteCall(t.Context(), nil, llm.ToolCallData{Name: "managed", Arguments: json.RawMessage(`{"forbidden":1}`)})
		if reached {
			t.Fatal("raw validator bypassed")
		}
		r.Use(func(context.Context, string, map[string]any) error { return errors.New("blocked") })
		r.ExecuteCall(t.Context(), nil, llm.ToolCallData{Name: "managed", Arguments: json.RawMessage(`{"n":9007199254740993,"e":1e+03}`)})
		if reached {
			t.Fatal("middleware bypassed")
		}
	}
}
