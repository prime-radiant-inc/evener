package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestTaskTool_ExactDependencyNumbers(t *testing.T) {
	type dependencyCase struct {
		name, deps        string
		add, clear        bool
		reject, zeroError bool
	}
	var cases []dependencyCase
	for _, zero := range []string{"0", "-0", "0.0", "0e12", "0e-1000000000"} {
		cases = append(cases,
			dependencyCase{name: "clear/" + zero, deps: zero, clear: true},
			dependencyCase{name: "mixed/" + zero, deps: zero + ",1", reject: true, zeroError: true},
			dependencyCase{name: "add/" + zero, deps: zero, add: true, reject: true, zeroError: true})
	}
	for _, tiny := range []string{"1e-400", "1e-1000000000"} {
		cases = append(cases,
			dependencyCase{name: "tiny/" + tiny, deps: tiny, reject: true},
			dependencyCase{name: "tiny-mixed/" + tiny, deps: tiny + ",1", reject: true},
			dependencyCase{name: "tiny-add/" + tiny, deps: tiny, add: true, reject: true})
	}
	cases = append(cases,
		dependencyCase{name: "valid-update", deps: "1.0"},
		dependencyCase{name: "valid-add", deps: "1e0", add: true})
	for _, prepared := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("prepared=%t/%s", prepared, tc.name), func(t *testing.T) {
				h := newDependentTaskHarness(t)
				raw := `{"update":[{"id":2,"depends_on":[` + tc.deps + `]}]}`
				if tc.add {
					raw = `{"add":[{"type":"implement","description":"third","prompt":"p3","depends_on":[` + tc.deps + `]}]}`
				}
				args, err := tool.DecodeArguments([]byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = decodeTaskArgs(args)
				if (err != nil) != tc.reject || errors.Is(err, errZeroTaskID) != tc.zeroError {
					t.Fatalf("decode rejection=%v zero-classification=%v, want %v/%v: %v", err != nil, errors.Is(err, errZeroTaskID), tc.reject, tc.zeroError, err)
				}
				before, err := json.Marshal(h.store.View())
				if err != nil {
					t.Fatal(err)
				}
				call := llm.ToolCallData{ID: "numeric-dependencies", Name: "task_list", Arguments: json.RawMessage(raw)}
				var result tool.ExecResult
				if prepared {
					prep := prepareToolCall(call, h.reg.Get(call.Name), []string{call.Name}, call.Name, "communicate", "")
					if prep.PrevalErr != "" {
						if !tc.reject {
							t.Fatalf("valid call refused in preparation: %s", prep.PrevalErr)
						}
						result.IsError = true
					} else {
						result = h.reg.ExecutePreparedCall(context.Background(), nil, prep.Call)
					}
				} else {
					result = h.reg.ExecuteCall(context.Background(), nil, call)
				}
				if result.IsError != tc.reject {
					t.Fatalf("registry rejection=%v, want %v: %+v", result.IsError, tc.reject, result)
				}
				if tc.zeroError && !errors.Is(result.Err, errZeroTaskID) {
					t.Fatalf("registry lost zero classification: %v", result.Err)
				}
				if tc.reject {
					after, err := json.Marshal(h.store.View())
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("refused dependency call changed tasks: %s -> %s, %v", before, after, err)
					}
					return
				}
				wantDeps := []int{1}
				if tc.clear {
					wantDeps = nil
				}
				if got := taskByID(t, h.store, 2).DependsOn; !slices.Equal(got, wantDeps) {
					t.Fatalf("dependencies=%v, want %v", got, wantDeps)
				}
				if tc.add {
					if len(h.store.View()) != 3 || !slices.Equal(taskByID(t, h.store, 3).DependsOn, []int{1}) {
						t.Fatalf("valid add did not retain dependency: %+v", h.store.View())
					}
				}
			})
		}
	}
}

func TestTaskDependencyZeroClassification(t *testing.T) {
	for _, tc := range []struct {
		value any
		zero  bool
	}{
		{float64(0), true}, {float64(1), false},
		{int(0), false}, {int64(0), false}, {"0", false}, {nil, false},
		{json.Number("0.0"), true}, {json.Number("0e-1000000000"), true},
		{json.Number("1e-400"), false}, {json.Number("1e-1000000000"), false},
	} {
		_, err := decodeIDList([]any{tc.value})
		if errors.Is(err, errZeroTaskID) != tc.zero {
			t.Errorf("%T(%v): zero classification=%v, want %v: %v", tc.value, tc.value, errors.Is(err, errZeroTaskID), tc.zero, err)
		}
	}
}
