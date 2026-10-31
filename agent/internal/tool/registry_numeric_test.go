package tool

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// The registry must deliver the caller's integer to every argument consumer,
// including inside unconstrained objects and arrays, without changing ordinary
// JSON numbers or admitting invalid argument documents.
func TestRegistryNumericIngestion(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		mode := "direct"
		if prepared {
			mode = "prepared"
		}
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name, number, want string
			}{
				{"small_integer", "42", "42"},
				{"fraction", "1.25", "1.25"},
				{"integer_exponent", "12e2", "1200"},
				{"fraction_exponent", "125e-2", "1.25"},
				{"large_integer", "9007199254740993", "9007199254740993"},
				{"large_integer_exponent", "9.007199254740993e15", "9007199254740993"},
				{"large_integer_decimal", "9007199254740993.0", "9007199254740993"},
				{"negative_large_integer", "-9007199254740993", "-9007199254740993"},
				{"max_int64", "9223372036854775807", "9223372036854775807"},
				{"past_uint64", "18446744073709551617", "18446744073709551617"},
				{"precise_fraction", "1.0000000000000001", "1.0000000000000001"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := NewRegistry()
					observed := map[string]int{}
					checkNumber := func(stage string, value any) {
						t.Helper()
						encoded, err := json.Marshal(value)
						if err != nil {
							t.Fatal(err)
						}
						got, ok := new(big.Rat).SetString(string(encoded))
						want, valid := new(big.Rat).SetString(tc.want)
						if !valid {
							t.Fatal("invalid numeric fixture")
						}
						if !ok || got.Cmp(want) != 0 {
							t.Errorf("%s: got %s (%T), want numeric value %s", stage, encoded, value, tc.want)
						}
					}
					check := func(stage string, args map[string]any) {
						t.Helper()
						observed[stage]++
						checkNumber(stage+" value", args["value"])
						nested, ok := args["nested"].(map[string]any)
						if !ok {
							t.Fatalf("%s nested map type = %T", stage, args["nested"])
						}
						items, ok := nested["items"].([]any)
						if !ok || len(items) != 1 {
							t.Fatalf("%s nested items = %#v", stage, nested["items"])
						}
						checkNumber(stage+" nested item", items[0])
					}
					err := r.Register(RegisteredTool{
						Definition: llm.ToolDefinition{
							Name: "numeric_ingestion", Description: "Observe numeric arguments at dispatch boundaries",
							Parameters: map[string]any{"type": "object", "required": []string{"value", "nested"}, "properties": map[string]any{"value": map[string]any{"type": "number"}, "nested": map[string]any{"type": "object"}}},
						},
						NormalizeArgs: func(args map[string]any) (map[string]any, error) { check("normalize", args); return args, nil },
						PreValidate:   func(args map[string]any) error { check("prevalidate", args); return nil },
						Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
							check("execute", args)
							return args["nested"], nil
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					r.Use(func(_ context.Context, _ string, args map[string]any) error { check("middleware", args); return nil })
					call := llm.ToolCallData{Name: "numeric_ingestion", ID: "numeric", Arguments: json.RawMessage(`{"value":` + tc.number + `,"nested":{"items":[` + tc.number + `]}}`)}
					execute := r.ExecuteCall
					if prepared {
						execute = r.ExecutePreparedCall
					}
					result := execute(context.Background(), nil, call)
					if result.IsError {
						t.Fatalf("dispatch failed: %+v", result)
					}
					for _, stage := range []string{"normalize", "middleware", "execute"} {
						if observed[stage] == 0 {
							t.Errorf("%s never observed the call", stage)
						}
					}
					wantPrevalidate := 1
					if prepared {
						wantPrevalidate = 0
					}
					if observed["prevalidate"] != wantPrevalidate {
						t.Errorf("prevalidate count = %d, want %d", observed["prevalidate"], wantPrevalidate)
					}
				})
			}
		})
	}
}

// Recovery must be confined to validation, and must leave the registry usable.
func TestRegistryNumericValidationRecovery(t *testing.T) {
	for _, constraint := range []string{`"type":"integer"`, `"minimum":0`, `"maximum":1`, `"multipleOf":1`, `"const":0`, `"enum":[0]`, `"type":"array","uniqueItems":true`} {
		t.Run(constraint, func(t *testing.T) {
			for _, size := range []int{2, 21} {
				t.Run(strconv.Itoa(size), func(t *testing.T) {
					var property map[string]any
					if err := json.Unmarshal([]byte(`{`+constraint+`}`), &property); err != nil {
						t.Fatal(err)
					}
					calls := 0
					r := NewRegistry()
					if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "numeric_recovery", Description: "test", Parameters: map[string]any{"type": "object", "properties": map[string]any{"value": property}}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
						calls++
						return nil, nil
					}}); err != nil {
						t.Fatal(err)
					}
					bad, good := "1e-1000000000", "0"
					if strings.Contains(constraint, "uniqueItems") {
						values := []string{"1e-1000000000"}
						for i := 1; i < size; i++ {
							values = append(values, strconv.Itoa(i))
						}
						bad = "[" + strings.Join(values, ",") + "]"
						good = "[0,1]"
					}
					result := r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "numeric_recovery", Arguments: json.RawMessage(`{"value":` + bad + `}`)})
					if !result.IsError || calls != 0 {
						t.Fatalf("extreme constraint: error=%v calls=%d", result.IsError, calls)
					}
					result = r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "numeric_recovery", Arguments: json.RawMessage(`{"value":` + good + `}`)})
					if result.IsError || calls != 1 {
						t.Fatalf("following valid call: %+v calls=%d", result, calls)
					}
				})
			}
		})
	}
}
