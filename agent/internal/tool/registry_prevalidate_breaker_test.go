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

func TestExecuteCall_PreValidateFailureUsesBreaker(t *testing.T) {
	r := NewRegistry()
	dispatches := 0
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "prevalidate_breaker",
			Description: "integration regression",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"value"},
			},
		},
		OmitIntent: true,
		PreValidate: func(map[string]any) error {
			return errors.New("targeted prevalidation failure")
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			dispatches++
			return "unexpected", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	raw := []json.RawMessage{
		json.RawMessage(`{"value":"same"}`),
		json.RawMessage(`{ "value" : "same" }`),
		json.RawMessage(`{"value": "same"}`),
	}
	results := make([]ExecResult, 0, len(raw))
	for i, arguments := range raw {
		res := r.ExecuteCall(context.Background(), breakerEnv(t), llm.ToolCallData{
			ID:        "prevalidate-breaker",
			Name:      "prevalidate_breaker",
			Arguments: arguments,
		})
		results = append(results, res)
		if !res.IsError || dispatches != 0 {
			t.Fatalf("call %d = %#v, dispatches=%d", i+1, res, dispatches)
		}
		if res.BreakerExactSignature == "" || res.BreakerSemanticSignature == "" {
			t.Fatalf("call %d bypassed breaker finalization: %#v", i+1, res)
		}
		if len(res.BreakerExactSignature) > 96 || len(res.BreakerSemanticSignature) > 96 {
			t.Fatalf("call %d returned unbounded breaker signatures: %#v", i+1, res)
		}
	}
	for i := range 2 {
		if strings.Contains(results[i].Output, "semantic failure loop") {
			t.Fatalf("call %d parked before the semantic threshold: %#v", i+1, results[i])
		}
	}
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			if results[i].BreakerExactSignature == results[j].BreakerExactSignature {
				t.Fatalf("distinct raw calls %d and %d shared exact signature %q", i+1, j+1, results[i].BreakerExactSignature)
			}
		}
	}
	for i := 1; i < len(results); i++ {
		if results[0].BreakerSemanticSignature != results[i].BreakerSemanticSignature {
			t.Fatalf("equivalent calls did not share semantic failure identity: %#v", results)
		}
	}
	if !strings.Contains(results[2].Output, "semantic failure loop") || !strings.Contains(results[2].Output, "normalized boundary tool_execution") {
		t.Fatalf("third semantically equivalent PreValidate failure was not parked at the registered-hook boundary: %#v", results[2])
	}
}
