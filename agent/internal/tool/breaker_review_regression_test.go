package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	r := NewRegistry()
	registerSemanticReviewTool(t, r, "exec_command", DefShell().Parameters, func(map[string]any) (any, error) { return nil, nil })
	r.MarkRegisteredToolsCoreSemanticMetadata()
	if omitted, explicit := r.semanticSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp"}), r.semanticSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp", "mode": "foreground"}); omitted != explicit {
		t.Fatalf("runtime foreground default differs: %q != %q", omitted, explicit)
	}
	long := strings.Repeat("a", 257)
	if first, second := semanticCallSignature("read_file", map[string]any{"file_path": long + "/one"}), semanticCallSignature("read_file", map[string]any{"file_path": long + "/two"}); first == second {
		t.Fatalf("long meaningful targets collapsed: %q", first)
	}
}

func TestSemanticBreaker_ToolSpecificDefaultsAndReadFileIntent(t *testing.T) {
	custom := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{"type": "string", "default": "safe"},
		},
	}
	r := NewRegistry()
	registerSemanticReviewTool(t, r, "custom", custom, func(map[string]any) (any, error) { return nil, nil })
	if omitted, explicit := r.semanticSignature("custom", map[string]any{}), r.semanticSignature("custom", map[string]any{"mode": "safe"}); omitted == explicit {
		t.Fatalf("custom schema annotation collapsed omission with explicit behavior: %q", omitted)
	}
	image := map[string]any{"file_path": "scan.pdf", "intent": "extract invoice totals"}
	pdf := map[string]any{"file_path": "scan.pdf", "intent": "locate signature blocks"}
	if first, second := semanticCallSignature("read_file", image), semanticCallSignature("read_file", pdf); first == second {
		t.Fatalf("read_file analysis intent was removed from semantic identity: %q", first)
	}
	if first, second := semanticCallSignature("custom_description", map[string]any{"description": "analyze totals"}), semanticCallSignature("custom_description", map[string]any{"description": "find signatures"}); first == second {
		t.Fatalf("custom behavior-driving descriptions were removed from semantic identity: %q", first)
	}
	registerSemanticReviewTool(t, r, "shell", DefShell().Parameters, func(map[string]any) (any, error) { return nil, nil })
	r.MarkRegisteredToolsCoreSemanticMetadata()
	if first, second := r.semanticSignature("shell", map[string]any{"command": "false", "description": "first narration"}), r.semanticSignature("shell", map[string]any{"command": "false", "description": "second narration"}); first != second {
		t.Fatalf("built-in presentation descriptions changed semantic identity: %q != %q", first, second)
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

func TestSemanticBreaker_RecursiveAndHandlerDefaultsAreEquivalent(t *testing.T) {
	t.Run("job stop max wait", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "job_stop", DefJobStop().Parameters, func(map[string]any) (any, error) {
			calls++
			return nil, errors.New("invalid_request: target unavailable")
		})
		r.MarkRegisteredToolsCoreSemanticMetadata()
		variants := []string{
			`{"target":"job_same","intent":"first"}`,
			`{"target":"job_same","max_wait_ms":0,"intent":"second"}`,
			`{"target":"job_same","intent":"third"}`,
		}
		for i, args := range variants {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("job-stop-default-%d", i), "job_stop", args))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || calls != 2) {
				t.Fatalf("neutral job_stop max_wait_ms evaded semantic breaker: calls=%d result=%#v", calls, res)
			}
		}
	})

	t.Run("nested ask user multi select", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "ask_user", DefAskUser().Parameters, func(map[string]any) (any, error) {
			calls++
			return nil, errors.New("invalid_request: user channel unavailable")
		})
		r.MarkRegisteredToolsCoreSemanticMetadata()
		question := func(multi string, i int) string {
			return fmt.Sprintf(`{"questions":[{"question":"Choose","options":[{"label":"A","detail":"a"},{"label":"B","detail":"b"}]%s}],"intent":"variant %d"}`, multi, i)
		}
		for i, args := range []string{question("", 1), question(`,"multi_select":false`, 2), question("", 3)} {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("ask-user-default-%d", i), "ask_user", args))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || calls != 2) {
				t.Fatalf("nested multi_select default evaded semantic breaker: calls=%d result=%#v", calls, res)
			}
		}
	})
}

func TestSemanticBreaker_CoreRuntimeDefaultsGroupEquivalentFailures(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		params   map[string]any
		args     []string
	}{
		{"job stop max wait", "job_stop", DefJobStop().Parameters, []string{`{"target":"job_same"}`, `{"target":"job_same","max_wait_ms":0}`, `{"target":"job_same"}`}},
		{"job stop include children", "job_stop", DefJobStop().Parameters, []string{`{"target":"job_same"}`, `{"target":"job_same","include_children":false}`, `{"target":"job_same"}`}},
		{"job list include nested", "job_list", DefJobList().Parameters, []string{`{}`, `{"include_nested":false}`, `{}`}},
		{"job list include descendants", "job_list", DefJobList().Parameters, []string{`{}`, `{"include_descendants":false}`, `{}`}},
		{"job list limit", "job_list", DefJobList().Parameters, []string{`{}`, `{"limit":50}`, `{}`}},
		{"job list offset", "job_list", DefJobList().Parameters, []string{`{}`, `{"offset":0}`, `{}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			calls := 0
			registerSemanticReviewTool(t, r, tc.toolName, tc.params, func(map[string]any) (any, error) {
				calls++
				return nil, errors.New("invalid_request: unavailable")
			})
			r.MarkRegisteredToolsCoreSemanticMetadata()
			callerArgs := map[string]any{}
			if err := json.Unmarshal([]byte(tc.args[0]), &callerArgs); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(callerArgs)
			if err != nil {
				t.Fatal(err)
			}
			_ = r.semanticSignature(tc.toolName, callerArgs)
			after, err := json.Marshal(callerArgs)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("semantic defaults mutated caller args: before=%s after=%s", before, after)
			}
			var first, second ExecResult
			for i, args := range tc.args {
				result := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("%s-%d", tc.name, i), tc.toolName, args))
				if i == 0 {
					first = result
				}
				if i == 1 {
					second = result
				}
				if i == 2 && (!strings.Contains(result.Output, "semantic failure loop") || calls != 2 || result.BreakerSemanticSignature != first.BreakerSemanticSignature) {
					t.Fatalf("equivalent defaults evaded semantic breaker: calls=%d first=%#v second=%#v parked=%#v", calls, first, second, result)
				}
			}
			if first.BreakerExactSignature == second.BreakerExactSignature {
				t.Fatalf("default variants unexpectedly shared exact identity: %#v %#v", first, second)
			}
		})
	}
}

func TestSemanticBreaker_CoreRuntimeNonDefaultsRemainDistinct(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		params   map[string]any
		args     []string
	}{
		{"job stop max wait", "job_stop", DefJobStop().Parameters, []string{`{"target":"job_same"}`, `{"target":"job_same","max_wait_ms":1}`, `{"target":"job_same"}`}},
		{"job stop include children", "job_stop", DefJobStop().Parameters, []string{`{"target":"job_same"}`, `{"target":"job_same","include_children":true}`, `{"target":"job_same"}`}},
		{"job list include nested", "job_list", DefJobList().Parameters, []string{`{}`, `{"include_nested":true}`, `{}`}},
		{"job list include descendants", "job_list", DefJobList().Parameters, []string{`{}`, `{"include_descendants":true}`, `{}`}},
		{"job list limit", "job_list", DefJobList().Parameters, []string{`{}`, `{"limit":49}`, `{}`}},
		{"job list offset", "job_list", DefJobList().Parameters, []string{`{}`, `{"offset":1}`, `{}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			calls := 0
			registerSemanticReviewTool(t, r, tc.toolName, tc.params, func(map[string]any) (any, error) {
				calls++
				return nil, errors.New("invalid_request: unavailable")
			})
			r.MarkRegisteredToolsCoreSemanticMetadata()
			for i, args := range tc.args {
				result := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("%s-%d", tc.name, i), tc.toolName, args))
				if strings.Contains(result.Output, "semantic failure loop") {
					t.Fatalf("non-default variant was grouped with omission: %#v", result)
				}
			}
			if calls != 3 {
				t.Fatalf("calls=%d, want non-default variant to execute", calls)
			}
		})
	}
}

func TestSemanticBreaker_CustomReplacementDoesNotInheritBuiltInDefaults(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		args   []string
	}{
		{"shell", DefShell().Parameters, []string{`{"command":"false"}`, `{"command":"false","mode":"foreground"}`, `{"command":"false","intent":"retry"}`}},
		{"job_stop", DefJobStop().Parameters, []string{`{"target":"same"}`, `{"target":"same","max_wait_ms":0}`, `{"target":"same","intent":"retry"}`}},
		{"job_stop_include_children", DefJobStop().Parameters, []string{`{"target":"same"}`, `{"target":"same","include_children":false}`, `{"target":"same","intent":"retry"}`}},
		{"job_list", DefJobList().Parameters, []string{`{}`, `{"include_nested":false}`, `{}`}},
		{"job_list_include_descendants", DefJobList().Parameters, []string{`{}`, `{"include_descendants":false}`, `{}`}},
		{"job_list_limit", DefJobList().Parameters, []string{`{}`, `{"limit":50}`, `{}`}},
		{"job_list_offset", DefJobList().Parameters, []string{`{}`, `{"offset":0}`, `{}`}},
		{"read_transcript", DefReadTranscript().Parameters, []string{`{"transcript_ref":"job:same","output_match":"ready"}`, `{"transcript_ref":"job:same","output_match":"ready","context_lines":0}`, `{"transcript_ref":"job:same","output_match":"ready"}`}},
		{"ask_user", DefAskUser().Parameters, []string{`{"questions":[{"question":"Choose","options":[{"label":"A","detail":"a"},{"label":"B","detail":"b"}]}],"intent":"first"}`, `{"questions":[{"question":"Choose","options":[{"label":"A","detail":"a"},{"label":"B","detail":"b"}],"multi_select":false}],"intent":"second"}`, `{"questions":[{"question":"Choose","options":[{"label":"A","detail":"a"},{"label":"B","detail":"b"}]}],"intent":"third"}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			calls := 0
			baseName := tc.name
			if strings.HasPrefix(baseName, "job_stop") {
				baseName = "job_stop"
			} else if strings.HasPrefix(baseName, "job_list") {
				baseName = "job_list"
			}
			registerSemanticReviewTool(t, r, baseName, tc.params, func(map[string]any) (any, error) { return nil, nil })
			r.MarkRegisteredToolsCoreSemanticMetadata()
			registerSemanticReviewTool(t, r, baseName, tc.params, func(map[string]any) (any, error) {
				calls++
				return nil, errors.New("custom failure")
			})
			for i, args := range tc.args {
				res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("custom-%s-%d", tc.name, i), baseName, args))
				if strings.Contains(res.Output, "semantic failure loop") {
					t.Fatalf("custom %s inherited built-in default grouping: %#v", tc.name, res)
				}
			}
			if calls != 3 {
				t.Fatalf("custom %s calls=%d, want 3", tc.name, calls)
			}
		})
	}
}

type reviewCodedError struct {
	code string
	text string
}

func (e reviewCodedError) Error() string        { return e.text }
func (e reviewCodedError) FailureClass() string { return e.code }

func TestExactFailureParkDoesNotClaimSubthresholdSemanticLoop(t *testing.T) {
	r := NewRegistry()
	calls := 0
	registerSemanticReviewTool(t, r, "exact_distinct_semantics", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
		calls++
		code := "second_class"
		if calls == 1 {
			code = "first_class"
		}
		return nil, reviewCodedError{code: code, text: "same rendered failure"}
	})
	call := breakerCall("exact-distinct-semantics", "exact_distinct_semantics", `{}`)
	for i := range 2 {
		result := r.ExecuteCall(context.Background(), breakerEnv(t), call)
		if !result.IsError || strings.Contains(result.Output, "semantic failure loop") {
			t.Fatalf("attempt %d = %#v, want ordinary executed failure", i+1, result)
		}
	}
	parked := r.ExecuteCall(context.Background(), breakerEnv(t), call)
	if calls != 2 || !parked.IsError || !strings.HasPrefix(parked.Output, failureParkText(call.Name, nil)) {
		t.Fatalf("third exact call was not parked: calls=%d result=%#v", calls, parked)
	}
	if strings.Contains(parked.Output, "semantic failure loop") || parked.BreakerSemanticSignature != "" {
		t.Fatalf("exact park claimed a subthreshold semantic loop: %#v", parked)
	}
}

func TestSemanticBreaker_TypedErrorsIgnorePresentationButUntypedRemainCompatible(t *testing.T) {
	t.Run("typed class", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "typed_failure", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			calls++
			return nil, reviewCodedError{code: "upstream_unavailable", text: fmt.Sprintf("[trace %d] upstream unavailable", calls)}
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("typed-%d", i), "typed_failure", fmt.Sprintf(`{"intent":"%d"}`, i)))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || calls != 2) {
				t.Fatalf("typed class did not survive presentation changes: calls=%d result=%#v", calls, res)
			}
		}
	})
	t.Run("untyped presentation", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "untyped_failure", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			calls++
			return nil, errors.New([]string{"backend temporarily unavailable [trace a]", "backend temporarily unavailable [trace b]", "backend temporarily unavailable [trace c]"}[calls-1])
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("untyped-%d", i), "untyped_failure", fmt.Sprintf(`{"intent":"%d"}`, i)))
			if i == 2 && (!strings.Contains(res.Output, "semantic failure loop") || calls != 2) {
				t.Fatalf("untyped presentation changes evaded semantic breaker: calls=%d result=%#v", calls, res)
			}
		}
	})
	t.Run("typed classes remain distinct", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "typed_distinct", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			calls++
			return nil, reviewCodedError{code: []string{"backend_a", "backend_b", "backend_a"}[calls-1], text: "backend failure"}
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("typed-distinct-%d", i), "typed_distinct", fmt.Sprintf(`{"intent":"%d"}`, i)))
			if strings.Contains(res.Output, "semantic failure loop") {
				t.Fatalf("different typed classes were collapsed: %#v", res)
			}
		}
		if calls != 3 {
			t.Fatalf("calls=%d, want distinct typed classes to execute", calls)
		}
	})
	t.Run("typed class precedes rendered boundary", func(t *testing.T) {
		r := NewRegistry()
		calls := 0
		registerSemanticReviewTool(t, r, "typed_boundary", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
			calls++
			return nil, reviewCodedError{code: []string{"backend_a", "backend_b", "backend_a"}[calls-1], text: "invalid_request: backend failure"}
		})
		for i := range 3 {
			res := r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall(fmt.Sprintf("typed-boundary-%d", i), "typed_boundary", fmt.Sprintf(`{"intent":"%d"}`, i)))
			if strings.Contains(res.Output, "semantic failure loop") {
				t.Fatalf("rendered boundary collapsed distinct typed classes: %#v", res)
			}
		}
		if calls != 3 {
			t.Fatalf("calls=%d, want distinct typed classes to execute", calls)
		}
	})
}

func TestSemanticBreaker_TelemetryComponentsAreSessionKeyed(t *testing.T) {
	first, second := NewRegistry(), NewRegistry()
	oversize := make([]byte, maxToolArgumentBytes+1)
	for _, tc := range []struct {
		name string
		one  string
		two  string
	}{
		{"oversize marker", first.semanticSignatureFromRaw("write_file", oversize), second.semanticSignatureFromRaw("write_file", oversize)},
		{"invalid JSON marker", first.semanticSignatureFromRaw("write_file", []byte(`{"target":`)), second.semanticSignatureFromRaw("write_file", []byte(`{"target":`))},
		{"error class", first.telemetryComponent("semantic-error-class", "unavailable"), second.telemetryComponent("semantic-error-class", "unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.one == tc.two {
				t.Fatalf("cross-session telemetry component is static: %q", tc.one)
			}
		})
	}
	if got, want := first.telemetryComponent("semantic-error-class", "unavailable"), first.telemetryComponent("semantic-error-class", "unavailable"); got != want {
		t.Fatalf("session telemetry component is not stable: %q != %q", got, want)
	}
}

func TestSemanticBreaker_CheckDoesNotRefreshLRU(t *testing.T) {
	l := newSemanticFailureLedger()
	l.record("oldest", "class", "boundary")
	for i := range maxFailureLedgerEntries - 1 {
		l.record(fmt.Sprintf("other-%d", i), "class", "boundary")
	}
	if count, _, _ := l.check("oldest"); count != 1 {
		t.Fatalf("oldest count=%d, want 1", count)
	}
	l.record("new", "class", "boundary")
	if count, _, _ := l.check("oldest"); count != 0 {
		t.Fatalf("check refreshed LRU; oldest count=%d", count)
	}
}

func TestSemanticBreaker_AskUserDefaultsDoNotMutateArguments(t *testing.T) {
	r := NewRegistry()
	seenAbsent := false
	registerSemanticReviewTool(t, r, "ask_user", DefAskUser().Parameters, func(args map[string]any) (any, error) {
		questions := args["questions"].([]any)
		_, seenAbsent = questions[0].(map[string]any)["multi_select"]
		return nil, errors.New("invalid_request: unavailable")
	})
	r.MarkRegisteredToolsCoreSemanticMetadata()
	args := map[string]any{"questions": []any{map[string]any{
		"question": "Choose",
		"options":  []any{map[string]any{"label": "A", "detail": "a"}, map[string]any{"label": "B", "detail": "b"}},
	}}}
	before, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.semanticSignature("ask_user", args)
	after, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("signature mutated args: before=%s after=%s", before, after)
	}
	r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall("ask-no-mutate", "ask_user", string(before)))
	if seenAbsent {
		t.Fatal("executor received synthesized multi_select")
	}
}

func TestSemanticBreaker_ConcurrentExactFailurePublishesSemanticMetadata(t *testing.T) {
	r := NewRegistry()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	registerSemanticReviewTool(t, r, "concurrent_failure", map[string]any{"type": "object"}, func(map[string]any) (any, error) {
		entered <- struct{}{}
		<-release
		return nil, errors.New("invalid_request: concurrent failure")
	})
	call := breakerCall("same", "concurrent_failure", `{"target":"same"}`)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			r.ExecuteCall(context.Background(), breakerEnv(t), call)
		})
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	parked := r.ExecuteCall(context.Background(), breakerEnv(t), call)
	if parked.BreakerSemanticSignature == "" || !strings.Contains(parked.Output, "normalized boundary") {
		t.Fatalf("exact threshold published without semantic metadata: %#v", parked)
	}
}

func TestSemanticBreaker_DispatchUsesOneRegistrationSnapshot(t *testing.T) {
	r := NewRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	oldCalls, newCalls := 0, 0
	old := RegisteredTool{
		Definition:                          llm.ToolDefinition{Name: "snapshot", Parameters: map[string]any{"type": "object"}},
		OmitDescriptionFromSemanticIdentity: true,
		ApplyBuiltInSemanticDefaults:        true,
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			close(entered)
			<-release
			return args, nil
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			oldCalls++
			return nil, errors.New("invalid_request: old")
		},
	}
	if err := r.Register(old); err != nil {
		t.Fatal(err)
	}
	call := breakerCall("snapshot-old", "snapshot", `{"description":"old"}`)
	result := make(chan ExecResult, 1)
	go func() { result <- r.ExecuteCall(context.Background(), breakerEnv(t), call) }()
	<-entered
	newTool := old
	newTool.OmitDescriptionFromSemanticIdentity = false
	newTool.ApplyBuiltInSemanticDefaults = false
	newTool.NormalizeArgs = nil
	newTool.Exec = func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		newCalls++
		return nil, errors.New("invalid_request: new")
	}
	if err := r.Register(newTool); err != nil {
		t.Fatal(err)
	}
	close(release)
	res := <-result
	if res.BreakerExactSignature != "" || res.BreakerSemanticSignature != "" || res.BreakerBypassed || oldCalls != 1 || newCalls != 0 {
		t.Fatalf("stale dispatch published replacement ledger state: result=%#v old=%d new=%d", res, oldCalls, newCalls)
	}
	r.ExecuteCall(context.Background(), breakerEnv(t), breakerCall("snapshot-new", "snapshot", `{"description":"new"}`))
	if newCalls != 1 {
		t.Fatalf("replacement executor calls=%d, want 1", newCalls)
	}
}
