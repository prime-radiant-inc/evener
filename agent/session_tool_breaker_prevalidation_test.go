package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

type registeredHookFailureError struct {
	cause   error
	message string
}

func (e registeredHookFailureError) Error() string        { return e.message }
func (e registeredHookFailureError) FailureClass() string { return "policy_denied" }
func (e registeredHookFailureError) Unwrap() error        { return e.cause }

func TestSessionPrevalidationFailuresReachSemanticBreaker(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	defer sess.Close()

	tests := []struct {
		name string
		tool string
		args func(int) string
	}{
		{"unknown", "definitely_unknown_829", func(i int) string { return fmt.Sprintf(`{"intent":"variant %d"}`, i) }},
		{"schema", "job_stop", func(i int) string { return fmt.Sprintf(`{"intent":"variant %d"}`, i) }},
		{"json", "job_stop", func(int) string { return `{"target":` }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var first string
			var second string
			var third string
			var semantic string
			for i := range 3 {
				res := sess.execTool(context.Background(), llm.ToolCallData{
					ID:        fmt.Sprintf("%s-%d", tc.name, i),
					Name:      tc.tool,
					Arguments: []byte(tc.args(i)),
				}, "")
				if !res.PrevalOnly {
					t.Fatalf("prevalidation result %d lost its no-dispatch marker: %#v", i+1, res)
				}
				switch i {
				case 0:
					first = res.Output
				case 1:
					second = res.Output
				case 2:
					third, semantic = res.Output, res.BreakerSemanticSignature
				}
			}
			if strings.Contains(first, "You just ran") || strings.Contains(first, "did not execute") {
				t.Fatalf("first prevalidation failure unexpectedly advanced breaker: %q", first)
			}
			if !strings.Contains(second, "You just ran") || strings.Contains(second, "did not execute") {
				t.Fatalf("second prevalidation failure did not produce its breaker nudge: %q", second)
			}
			if semantic == "" || !strings.Contains(third, "semantic failure loop") {
				t.Fatalf("prevalidation bypassed semantic breaker: signature=%q output=%q", semantic, third)
			}
		})
	}
}

func TestRegisteredHookFailuresKeepClassificationAcrossDirectAndSessionPaths(t *testing.T) {
	for _, hook := range []string{"normalize", "prevalidate"} {
		t.Run(hook, func(t *testing.T) {
			sess := newSession(t, withConfig(SessionConfig{
				StateDir:         t.TempDir(),
				MaxSubagentDepth: 1,
				NoProjectPrompts: true,
				testOnly: testConfig{
					skipGitSnapshot:     true,
					minimalSystemPrompt: true,
					noSyncJobStore:      true,
				},
			}))
			defer sess.Close()

			const name = "registered_hook_failure"
			cause := errors.New("registered hook sentinel")
			wantBoundary := "tool_execution"
			message := "registered hook rejected request 42"
			if hook == "normalize" {
				wantBoundary = "invalid_request"
				message = "invalid_request: registered hook rejected request 42"
			}
			wantErr := registeredHookFailureError{cause: cause, message: message}
			hookCalls := 0
			dispatches := 0
			registered := tool.RegisteredTool{
				Definition: llm.ToolDefinition{Name: name, Description: "registered hook failure probe", Parameters: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"value": map[string]any{"type": "string"},
					},
					"required": []any{"value"},
				}},
				OmitIntent: true,
				Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
					dispatches++
					return "unexpected", nil
				},
			}
			switch hook {
			case "normalize":
				registered.NormalizeArgs = func(map[string]any) (map[string]any, error) {
					hookCalls++
					return nil, wantErr
				}
			case "prevalidate":
				registered.PreValidate = func(map[string]any) error {
					hookCalls++
					return wantErr
				}
			}
			if err := sess.reg.Register(registered); err != nil {
				t.Fatalf("Register: %v", err)
			}

			raw := [][]byte{
				[]byte(`{"value":"same"}`),
				[]byte(`{ "value" : "same" }`),
				[]byte(`{"value": "same"}`),
			}
			results := make([]tool.ExecResult, 0, len(raw))
			for i, arguments := range raw {
				call := llm.ToolCallData{ID: fmt.Sprintf("%s-%d", hook, i), Name: name, Arguments: arguments}
				if i == 1 {
					results = append(results, sess.execTool(context.Background(), call, ""))
				} else {
					results = append(results, sess.reg.ExecuteCall(context.Background(), sess.currentEnv(), call))
				}
			}

			for i := range 2 {
				if !results[i].IsError || !errors.Is(results[i].Err, cause) {
					t.Fatalf("failure %d lost typed hook error: %#v", i+1, results[i])
				}
				if strings.Contains(results[i].Output, "semantic failure loop") {
					t.Fatalf("failure %d parked before threshold: %#v", i+1, results[i])
				}
			}
			for i := range results {
				if results[i].BreakerSemanticSignature != results[0].BreakerSemanticSignature {
					t.Fatalf("direct and Session failures used different classification: %#v", results)
				}
				for j := i + 1; j < len(results); j++ {
					if results[i].BreakerExactSignature == results[j].BreakerExactSignature {
						t.Fatalf("raw-distinct failures %d and %d shared exact identity: %#v", i+1, j+1, results)
					}
				}
			}
			if !strings.Contains(results[2].Output, "semantic failure loop") || !strings.Contains(results[2].Output, "normalized boundary "+wantBoundary) {
				t.Fatalf("third equivalent hook failure did not park at classified boundary: %#v", results[2])
			}
			if hookCalls != 3 || dispatches != 0 {
				t.Fatalf("calls = hook %d, dispatch %d; want 3, 0", hookCalls, dispatches)
			}
		})
	}
}

func TestSessionRegisterToolReplacementDoesNotInheritShellDefaults(t *testing.T) {
	sess := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}))
	defer sess.Close()
	calls := 0
	sess.RegisterTool("shell", "custom shell", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		calls++
		return nil, errors.New("custom shell failure")
	})
	for i, args := range []string{`{"command":"false"}`, `{"command":"false","mode":"foreground"}`, `{"command":"false","intent":"retry"}`} {
		res := sess.execTool(context.Background(), llm.ToolCallData{ID: fmt.Sprintf("custom-shell-%d", i), Name: "shell", Arguments: []byte(args)}, "")
		if strings.Contains(res.Output, "semantic failure loop") {
			t.Fatalf("custom shell replacement inherited core defaults: %#v", res)
		}
	}
	if calls != 3 {
		t.Fatalf("custom shell calls=%d, want 3", calls)
	}
}
