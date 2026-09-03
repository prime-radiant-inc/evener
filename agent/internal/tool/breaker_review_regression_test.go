package tool

import (
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
	if omitted, explicit := semanticCallSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp"}), semanticCallSignature("exec_command", map[string]any{"command": "false", "cwd": "/tmp", "mode": "foreground"}); omitted != explicit {
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
	if omitted, explicit := semanticCallSignatureWithDefaults("custom", map[string]any{}, custom), semanticCallSignatureWithDefaults("custom", map[string]any{"mode": "safe"}, custom); omitted == explicit {
		t.Fatalf("custom schema annotation collapsed omission with explicit behavior: %q", omitted)
	}
	image := map[string]any{"file_path": "scan.pdf", "intent": "extract invoice totals"}
	pdf := map[string]any{"file_path": "scan.pdf", "intent": "locate signature blocks"}
	if first, second := semanticCallSignature("read_file", image), semanticCallSignature("read_file", pdf); first == second {
		t.Fatalf("read_file analysis intent was removed from semantic identity: %q", first)
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

type reviewCodedError struct {
	code string
	text string
}

func (e reviewCodedError) Error() string        { return e.text }
func (e reviewCodedError) FailureClass() string { return e.code }

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
}

func TestSemanticBreaker_TelemetryComponentsAreSessionKeyed(t *testing.T) {
	first, second := NewRegistry(), NewRegistry()
	oversize := make([]byte, maxToolArgumentBytes+1)
	for _, tc := range []struct {
		name string
		one  string
		two  string
	}{
		{"oversize marker", first.semanticSignatureFromRaw("write_file", oversize, nil), second.semanticSignatureFromRaw("write_file", oversize, nil)},
		{"invalid JSON marker", first.semanticSignatureFromRaw("write_file", []byte(`{"target":`), nil), second.semanticSignatureFromRaw("write_file", []byte(`{"target":`), nil)},
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
