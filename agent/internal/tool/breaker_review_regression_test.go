package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func registerSemanticReviewTool(t *testing.T, r *Registry, name string, params map[string]any, exec func(map[string]any) (any, error)) {
	t.Helper()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "semantic review regression", Parameters: params},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return exec(args)
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticBreaker_RegisteredDefaultsAndLongTargets(t *testing.T) {
	if omitted, explicit := semanticCallSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp"}), semanticCallSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp", "mode": "foreground"}); omitted != explicit {
		t.Fatalf("runtime foreground default differs: %q != %q", omitted, explicit)
	}
	long := strings.Repeat("a", 257)
	if first, second := semanticCallSignature("read_file", map[string]any{"file_path": long + "/one"}), semanticCallSignature("read_file", map[string]any{"file_path": long + "/two"}); first == second {
		t.Fatalf("long meaningful targets collapsed: %q", first)
	}
}

func TestSemanticBreaker_RecordsPreDispatchFailuresAndStableInvalidRequest(t *testing.T) {
	t.Run("presentation wording", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "stable_invalid", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			calls++
			return nil, fmt.Errorf("invalid_request: display wording %d", calls)
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("word-%d", i), "stable_invalid", fmt.Sprintf(`{"intent":"variant %d"}`, i)))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || res.BreakerSemanticSignature == "") {
				t.Fatalf("presentation variation escaped: %#v", res)
			}
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want third parked", calls)
		}
	})
	t.Run("schema", func(t *testing.T) {
		r := NewRegistry()
		registerSemanticReviewTool(t, r, "schema_invalid", map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}}, "required": []any{"target"}}, func(map[string]any) (any, error) {
			t.Fatal("schema invalid call executed")
			return nil, nil
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("schema-%d", i), "schema_invalid", fmt.Sprintf(`{"intent":"variant %d"}`, i)))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || res.BreakerSemanticSignature == "") {
				t.Fatalf("schema variation escaped: %#v", res)
			}
		}
	})
}

func TestSemanticBreaker_ExactParkTelemetryProtocolAndSecretSafety(t *testing.T) {
	r := NewRegistry()
	registerSemanticReviewTool(t, r, "exact_semantic", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
		return nil, errors.New("invalid_request: fixed boundary")
	})
	call := breakerCall("exact", "exact_semantic", `{"target":"same"}`)
	for range 2 {
		r.ExecuteCall(context.Background(), breakerEnv(t), call)
	}
	parked := r.ExecuteCall(context.Background(), breakerEnv(t), call)
	if parked.BreakerSemanticSignature == "" || !strings.Contains(parked.Output, "normalized boundary") || !strings.Contains(parked.Output, "materially different") {
		t.Fatalf("exact park lost semantic guidance: %#v", parked)
	}

	model := NewRegistry()
	registerSemanticReviewTool(t, model, "model_list", map[string]any{"type": "object"}, func(map[string]any) (any, error) { return "ok", nil })
	if res := model.ExecuteCall(context.Background(), breakerEnv(t), breakerCall("model", "model_list", `{}`)); res.BreakerBypassed {
		t.Fatalf("protocol exemption was reported as human bypass: %#v", res)
	}

	args := json.RawMessage(`{"token":"0427","target":"same"}`)
	first := r.ExecuteCall(context.Background(), breakerEnv(t), llm.ToolCallData{ID: "secret", Name: "exact_semantic", Arguments: args}).BreakerExactSignature
	other := NewRegistry().telemetryExactSignature("exact_semantic", args)
	if first == signature("exact_semantic", args) || first == other || strings.Contains(first, "0427") {
		t.Fatalf("exact telemetry is not session-keyed/redacted: %q", first)
	}
}

func TestSemanticBreaker_PreservesBehaviorDrivingArguments(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
	}{
		{
			name:   "patch bodies",
			first:  `{"file_path":"a.go","patch":"*** Begin Patch\n+old\n*** End Patch"}`,
			second: `{"file_path":"a.go","patch":"*** Begin Patch\n+new\n*** End Patch"}`,
		},
		{
			name:   "nested custom presentation-looking fields",
			first:  `{"target":"a","options":{"intent":"first","description":"first"}}`,
			second: `{"target":"a","options":{"intent":"second","description":"second"}}`,
		},
		{
			name:   "sensitive named arguments",
			first:  `{"target":"a","token":"first-token","authorization":"first-auth"}`,
			second: `{"target":"a","token":"second-token","authorization":"second-auth"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			calls := 0
			registerSemanticReviewTool(t, r, "meaningful", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
				calls++
				return nil, errors.New("invalid_request: fixed boundary")
			})
			for i, args := range []string{tc.first, tc.second, tc.first} {
				res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("%s-%d", tc.name, i), "meaningful", args))
				if strings.Contains(res.Output, "semantic failure loop") {
					t.Fatalf("meaningful correction %d was incorrectly parked: %#v", i+1, res)
				}
			}
			if calls != 3 {
				t.Fatalf("calls = %d, want distinct meaningful arguments to execute", calls)
			}
		})
	}
}

func TestSemanticBreaker_PreDispatchErrorPathsEnterLedger(t *testing.T) {
	assertPark := func(t *testing.T, r *Registry, name string, arguments func(int) string) {
		t.Helper()
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("%s-%d", name, i), name, arguments(i)))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || res.BreakerSemanticSignature == "") {
				t.Fatalf("third %s error did not enter semantic ledger: %#v", name, res)
			}
		}
	}
	intentOnly := func(i int) string { return fmt.Sprintf(`{"intent":"presentation %d"}`, i) }

	t.Run("unknown tool", func(t *testing.T) {
		assertPark(t, NewRegistry(), "unknown_semantic_review", intentOnly)
	})
	t.Run("invalid JSON", func(t *testing.T) {
		assertPark(t, NewRegistry(), "invalid_json", func(i int) string { return fmt.Sprintf(`{"intent":"presentation %d"`, i) })
	})
	t.Run("oversize arguments", func(t *testing.T) {
		oversize := strings.Repeat("x", maxToolArgumentBytes)
		assertPark(t, NewRegistry(), "oversize_semantic_review", func(i int) string {
			return fmt.Sprintf(`{"intent":"presentation %d","body":%q}`, i, oversize)
		})
	})
	t.Run("normalization", func(t *testing.T) {
		r := NewRegistry()
		if err := r.Register(RegisteredTool{
			Definition:    llm.ToolDefinition{Name: "normalize_semantic_review", Description: "normalization review", Parameters: map[string]any{"type": "object"}},
			NormalizeArgs: func(map[string]any) (map[string]any, error) { return nil, errors.New("normalization detail changes") },
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				t.Fatal("normalization failure executed")
				return nil, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		assertPark(t, r, "normalize_semantic_review", intentOnly)
	})
	t.Run("middleware", func(t *testing.T) {
		r := NewRegistry()
		registerSemanticReviewTool(t, r, "middleware_semantic_review", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			t.Fatal("middleware failure executed")
			return nil, nil
		})
		r.Use(func(context.Context, string, map[string]any) error { return errors.New("middleware detail changes") })
		assertPark(t, r, "middleware_semantic_review", intentOnly)
	})
}
