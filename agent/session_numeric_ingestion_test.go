package agent

import (
	"context"
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// The extension forces an ordinary first-pass validation error, then delegates
// the second pass to the real constrained validator. This isolates the repaired
// validation gate even when the candidate itself needs no scalar coercion.
type numericSecondGate struct {
	calls       int
	constrained *jsonschema.Schema
}

func (g *numericSecondGate) Validate(ctx jsonschema.ValidationContext, value any) error {
	g.calls++
	if g.calls == 1 {
		return ctx.Error("testFirstPass", "first pass requires repair validation")
	}
	return ctx.Validate(g.constrained, "testConstrained", value, "")
}

func TestPrepareNumericValidationGates(t *testing.T) {
	t.Parallel()
	for _, second := range []bool{false, true} {
		name := "initial"
		if second {
			name = "repaired"
		}
		t.Run(name, func(t *testing.T) {
			r := tool.NewRegistry()
			calls := 0
			def := llm.ToolDefinition{Name: "numeric_prepare", Description: "test", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"minimum": 0}}}}
			if err := r.Register(tool.RegisteredTool{Definition: def, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				calls++
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			registered := r.Get(def.Name)
			var gate *numericSecondGate
			if second {
				// Register a separate unconstrained schema so cached shared schemas are not mutated.
				unconstrained := tool.NewRegistry()
				if err := unconstrained.Register(regTool(llm.ToolDefinition{Name: "numeric_second", Description: "test", Parameters: map[string]any{"type": "object"}})); err != nil {
					t.Fatal(err)
				}
				schemaCopy := *unconstrained.Get("numeric_second").Schema
				gate = &numericSecondGate{constrained: registered.Schema}
				schemaCopy.Extensions = map[string]jsonschema.ExtSchema{"test": gate}
				registered.Schema = &schemaCopy
			}
			bad := llm.ToolCallData{Name: def.Name, Arguments: json.RawMessage(`{"n":1e-1000000000}`)}
			result := prepareToolCall(bad, registered, []string{def.Name}, def.Name, "communicate", "")
			if result.PrevalErr == "" || calls != 0 {
				t.Fatalf("extreme validation did not refuse: %+v calls=%d", result, calls)
			}
			if second && gate.calls != 2 {
				t.Fatalf("repaired validation visits=%d, want2", gate.calls)
			}
			good := llm.ToolCallData{Name: def.Name, Arguments: json.RawMessage(`{"n":0}`)}
			result = prepareToolCall(good, registered, []string{def.Name}, def.Name, "communicate", "")
			if result.PrevalErr != "" {
				t.Fatal(result.PrevalErr)
			}
			executed := r.ExecutePreparedCall(context.Background(), nil, result.Call)
			if executed.IsError || calls != 1 {
				t.Fatalf("following valid call: %+v calls=%d", executed, calls)
			}
		})
	}
}

func TestPrepareNumericPreservation(t *testing.T) {
	t.Parallel()
	r := tool.NewRegistry()
	def := llm.ToolDefinition{Name: "numeric_prepare", Description: "test", Parameters: map[string]any{"type": "object"}}
	if err := r.Register(regTool(def)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"n":9007199254740993,"nested":[1.0000000000000001]}`, `{n:9007199254740993,"nested":[1.0000000000000001]}`} {
		result := prepareToolCall(llm.ToolCallData{Name: def.Name, Arguments: json.RawMessage(raw)}, r.Get(def.Name), []string{def.Name}, def.Name, "communicate", "")
		if result.PrevalErr != "" {
			t.Fatal(result.PrevalErr)
		}
		var decoded struct {
			N      json.Number
			Nested []json.Number
		}
		if err := json.Unmarshal(result.Call.Arguments, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.N.String() != "9007199254740993" || len(decoded.Nested) != 1 || decoded.Nested[0].String() != "1.0000000000000001" {
			t.Fatalf("preparation changed numbers: %s", result.Call.Arguments)
		}
		call := result.Call
		if err := applyUpdatedToolInput(&call, map[string]any{"description": "hook update"}); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(call.Arguments, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.N.String() != "9007199254740993" || decoded.Nested[0].String() != "1.0000000000000001" {
			t.Fatalf("hook update changed numbers: %s", call.Arguments)
		}
	}
	for _, n := range []string{"1e-400", "1e-1000000000", "0e-1000000000"} {
		output, err := decodeDefaultCommunicateOutputString(`{"n":` + n + `}`)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := output["n"].(json.Number); !ok || value.String() != n {
			t.Fatalf("promotion changed %s to %#v", n, output)
		}
	}
	for _, raw := range []string{"", " ", "null", "[]", "1", "{} {}", `{"n":1e400}`} {
		if _, err := decodeDefaultCommunicateOutputString(raw); err == nil {
			t.Errorf("promoted non-object %q", raw)
		}
	}
}

func TestNumericReaders(t *testing.T) {
	t.Parallel()
	t.Run("worktree_prune_threshold", func(t *testing.T) {
		t.Parallel()
		for _, threshold := range []string{"0.000001", "1e-6"} {
			h := newScriptedWorktreeSession(t)
			before := len(h.git.calls)
			result := h.s.reg.ExecuteCall(context.Background(), h.s.currentEnv(), llm.ToolCallData{
				Name: "manage_worktree", Arguments: json.RawMessage(`{"operation":"prune","orphaned_delegate_days":` + threshold + `}`),
			})
			if !result.IsError || result.Err == nil || len(h.git.calls) != before {
				t.Fatalf("supplied threshold %s bypassed handler minimum: error=%v err=%v git calls=%d->%d", threshold, result.IsError, result.Err, before, len(h.git.calls))
			}
		}
	})
	for _, tc := range []struct {
		raw   string
		want  int
		valid bool
	}{
		{"12e2", 1200, true}, {"1.0", 1, true}, {"-1", -1, true}, {"1.0000000000000001", 0, false}, {"1e-400", 0, false}, {"9223372036854775808", 0, false},
	} {
		args := map[string]any{"n": json.Number(tc.raw)}
		got, present, err := watchIntArg(args, "n")
		if (err == nil) != tc.valid || tc.valid && (!present || got != tc.want) {
			t.Errorf("watch %s: %d,%v,%v", tc.raw, got, present, err)
		}
		ptr, err := optionalWholeIntArg(args, "n")
		valid := tc.valid && tc.want >= 0
		if (err == nil) != valid || valid && (ptr == nil || *ptr != tc.want) {
			t.Errorf("whole %s: %v,%v", tc.raw, ptr, err)
		}
	}
	for _, tc := range []struct {
		raw   string
		want  int
		valid bool
	}{
		{"1.0", 1, true}, {"1e2", 100, true}, {"9007199254740991", 9007199254740991, true}, {"9007199254740991.1", 0, false}, {"9007199254740992", 0, false}, {"0", 0, false},
	} {
		got, ok := taskIDValue(json.Number(tc.raw))
		if ok != tc.valid || ok && got != tc.want {
			t.Errorf("task %s: %d,%v", tc.raw, got, ok)
		}
	}
	for _, raw := range []string{"1.9", "-1.9", "1e100", "9223372036854775807"} {
		args := map[string]any{"n": json.Number(raw)}
		f, _ := json.Number(raw).Float64()
		want := int(f)
		ptr := optionalIntArg(args, "n")
		if ptr == nil || *ptr != want {
			t.Errorf("legacy optional %s: %v want%d", raw, ptr, want)
		}
		n, ok := shellIntArg(args, "n")
		if !ok || n != want {
			t.Errorf("legacy shell %s: %d,%v want%d", raw, n, ok, want)
		}
	}
	for _, raw := range []string{"1e-400", "1e-1000000000", "1.0000000000000001"} {
		if isNeutralRetainedInteger(json.Number(raw)) || watchIntegerValue(json.Number(raw)) == 0 {
			t.Errorf("nonzero removed: %s", raw)
		}
	}
	if !isNeutralRetainedInteger(json.Number("0e-1000000000")) {
		t.Error("exact zero not neutral")
	}
	grep := parseFileGrepArgs(map[string]any{"max_results": json.Number("12e0"), "context_lines": json.Number("15")})
	if grep.maxResults != 12 || grep.contextLines != 10 {
		t.Fatalf("grep numbers ignored: %+v", grep)
	}

}

// Both registered normalizers must give decimal/exponent numbers the same
// meaning on the fingerprint and dispatch paths. Middleware observes real
// normalized dispatches, and real executor errors feed the failure ledger.
func TestNumericNormalizersDispatchFingerprintAgreement(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"task_list", "read_transcript"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var reg *tool.Registry
			var calls []string
			var equivalent []string
			var want, distinct, wantDistinct string
			if name == "task_list" {
				h := newDependentTaskHarness(t)
				reg = h.reg
				for _, id := range []string{"1", "1.0", "1e0"} {
					equivalent = append(equivalent, `{"update":[{"id":99,"status":"done"},{"id":`+id+`,"notes":""}]}`)
				}
				want = `{"update":[{"id":99,"status":"done"}]}`
				distinct = `{"update":[{"id":99,"status":"done","depends_on":[0.0]}]}`
				wantDistinct = `{"update":[{"depends_on":[0.0],"id":99,"status":"done"}]}`
			} else {
				reg = tool.NewRegistry()
				if err := reg.Register(readTranscriptTool(&toolDeps{stateDir: t.TempDir()})); err != nil {
					t.Fatal(err)
				}
				for _, zero := range []string{"0", "0.0", "0e0"} {
					equivalent = append(equivalent, `{"transcript_ref":"job:job_numeric_missing","context_lines":`+zero+`}`)
				}
				want = `{"transcript_ref":"job:job_numeric_missing"}`
				distinct = `{"transcript_ref":"job:job_numeric_missing","offset_bytes":0.0}`
				wantDistinct = `{"offset_bytes":0.0,"transcript_ref":"job:job_numeric_missing"}`
			}
			reg.Use(func(_ context.Context, _ string, args map[string]any) error {
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				calls = append(calls, string(encoded))
				return nil
			})
			execute := func(raw string) tool.ExecResult {
				return reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: name, Arguments: json.RawMessage(raw)})
			}
			for i, raw := range equivalent {
				result := execute(raw)
				if !result.IsError {
					t.Fatalf("call %d unexpectedly succeeded: %+v", i, result)
				}
				if i < 2 && (result.Err == nil || len(calls) != i+1 || calls[i] != want) {
					t.Fatalf("call %d did not reach the real executor with normalized args: calls=%v err=%v", i, calls, result.Err)
				}
				if i == 2 && (len(calls) != 2 || result.Err != nil) {
					t.Fatalf("equivalent decimal/exponent call did not park before dispatch: calls=%v err=%v", calls, result.Err)
				}
			}
			result := execute(distinct)
			if !result.IsError || result.Err == nil || len(calls) != 3 || calls[2] != wantDistinct {
				t.Fatalf("meaningful numeric presence merged with omission: calls=%v result=%+v", calls, result)
			}
		})
	}
}
