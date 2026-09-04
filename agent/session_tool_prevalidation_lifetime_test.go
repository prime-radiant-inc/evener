package agent

import (
	"context"
	"errors"
	"maps"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

func TestExecTool_PrevalidationSnapshotOldUnknownFailuresDoNotContaminateSuccessor(t *testing.T) {
	reached := make(chan struct{}, 2)
	release := make(chan struct{})
	var paused atomic.Int32

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: t.TempDir(),
		testOnly: testConfig{execToolCheckpoint: func(name string) {
			if name != "after_pre_hook" || paused.Add(1) > 2 {
				return
			}
			reached <- struct{}{}
			<-release
		}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	oldCall := llm.ToolCallData{ID: "old-unknown", Name: "prevalidation_successor", Arguments: []byte(`{"value":"ok"}`)}
	results := make(chan tool.ExecResult, 2)
	for range 2 {
		go func() { results <- sess.execTool(context.Background(), oldCall, "") }()
	}
	for range 2 {
		<-reached
	}

	var successorCalls atomic.Int32
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: "prevalidation_successor", Description: "test successor", Parameters: map[string]any{"type": "object"}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			successorCalls.Add(1)
			return "successor", nil
		},
	}); err != nil {
		t.Fatalf("Register successor: %v", err)
	}
	close(release)
	for range 2 {
		res := <-results
		if !res.IsError || res.BreakerExactSignature != "" || res.BreakerSemanticSignature != "" || res.BreakerBypassed {
			t.Fatalf("stale prevalidation must return its own plain validation error, got %#v", res)
		}
	}

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "successor-valid", Name: "prevalidation_successor", Arguments: []byte(`{"value":"ok"}`)}, "")
	if res.IsError || successorCalls.Load() != 1 {
		t.Fatalf("first successor call must execute instead of parking: calls=%d result=%#v", successorCalls.Load(), res)
	}
}

func TestExecTool_PreparedCallReplacementRunsSuccessorPreValidate(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	const name = "prepared_replacement"
	params := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"value":      map[string]any{"type": "string"},
			"normalized": map[string]any{"type": "string"},
		},
		"required": []any{"value"},
	}
	var originalNormalizations, originalPrevalidations, originalExecutions int
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "original registration", Parameters: params},
		OmitIntent: true,
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			originalNormalizations++
			args["normalized"] = "original"
			return args, nil
		},
		PreValidate: func(map[string]any) error {
			originalPrevalidations++
			return nil
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			originalExecutions++
			return "original executor ran", nil
		},
	}); err != nil {
		t.Fatalf("Register original: %v", err)
	}

	wantPrevalidationErr := errors.New("successor rejected normalized arguments")
	var successorNormalizations, successorPrevalidations, successorExecutions int
	replaced := false
	sess.cfg.testOnly.execToolCheckpoint = func(checkpoint string) {
		if checkpoint != "after_pre_hook" || replaced {
			return
		}
		replaced = true
		err := sess.reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: "successor registration", Parameters: params},
			OmitIntent: true,
			NormalizeArgs: func(args map[string]any) (map[string]any, error) {
				successorNormalizations++
				if _, exists := args["normalized"]; exists {
					t.Fatalf("successor NormalizeArgs received old registration's normalized arguments: %#v", args)
				}
				args["normalized"] = "successor"
				return args, nil
			},
			PreValidate: func(args map[string]any) error {
				successorPrevalidations++
				if args["normalized"] != "successor" {
					t.Fatalf("successor PreValidate args = %#v, want normalized successor args", args)
				}
				return wantPrevalidationErr
			},
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				successorExecutions++
				return "successor executor ran", nil
			},
		})
		if err != nil {
			t.Fatalf("Register successor: %v", err)
		}
	}

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "prepared-replacement",
		Name:      name,
		Arguments: []byte(`{"value":"input"}`),
	}, "")
	if !replaced {
		t.Fatal("successor was not installed between preparation and execution")
	}
	if originalNormalizations != 1 {
		t.Errorf("original NormalizeArgs calls = %d, want 1 during preparation", originalNormalizations)
	}
	if originalPrevalidations != 1 {
		t.Errorf("original PreValidate calls = %d, want 1 during preparation", originalPrevalidations)
	}
	if successorNormalizations != 1 {
		t.Errorf("successor NormalizeArgs calls = %d, want 1 during execution", successorNormalizations)
	}
	if successorPrevalidations != 1 {
		t.Errorf("successor PreValidate calls = %d, want 1 for the replacement registration", successorPrevalidations)
	}
	if originalExecutions != 0 || successorExecutions != 0 {
		t.Errorf("executor calls = original %d, successor %d; want neither after successor rejection", originalExecutions, successorExecutions)
	}
	if !res.IsError || !errors.Is(res.Err, wantPrevalidationErr) {
		t.Errorf("result = %#v, want successor PreValidate error %q", res, wantPrevalidationErr)
	}
}

func TestExecTool_PreparedCallReplacementDiscardsOldSchemaRepair(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	const name = "prepared_schema_replacement"
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "original registration", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "integer"},
			},
			"required": []any{"value"},
		}},
		OmitIntent: true,
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return "original executor ran", nil
		},
	}); err != nil {
		t.Fatalf("Register original: %v", err)
	}

	var successorExecutions int
	replaced := false
	sess.cfg.testOnly.execToolCheckpoint = func(checkpoint string) {
		if checkpoint != "after_pre_hook" || replaced {
			return
		}
		replaced = true
		err := sess.reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: "successor registration", Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "integer"},
				},
				"required": []any{"value"},
			}},
			OmitIntent: true,
			NormalizeArgs: func(args map[string]any) (map[string]any, error) {
				if args["value"] != "7" {
					t.Fatalf("successor NormalizeArgs args = %#v, want original provider string", args)
				}
				args["value"] = float64(7)
				return args, nil
			},
			PreValidate: func(args map[string]any) error {
				if args["value"] != float64(7) {
					t.Fatalf("successor PreValidate args = %#v, want successor-normalized integer", args)
				}
				return nil
			},
			Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				successorExecutions++
				if args["value"] != float64(7) {
					t.Fatalf("successor executor args = %#v, want successor-normalized integer", args)
				}
				return "successor", nil
			},
		})
		if err != nil {
			t.Fatalf("Register successor: %v", err)
		}
	}

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "prepared-schema-replacement",
		Name:      name,
		Arguments: []byte(`{"value":"7"}`),
	}, "")
	if !replaced {
		t.Fatal("successor was not installed between preparation and execution")
	}
	if res.IsError || successorExecutions != 1 {
		t.Fatalf("successor did not execute original provider arguments: executions=%d result=%#v", successorExecutions, res)
	}
}

func TestExecTool_HookUpdatedInputSupersedesPreparationFailure(t *testing.T) {
	sess := newSession(t)
	t.Cleanup(sess.Close)

	const name = "hook_corrected_prevalidation"
	var executions int
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "hook correction probe", Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []any{"value"},
		}},
		OmitIntent: true,
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			executions++
			if args["value"] != "corrected" {
				t.Fatalf("executor args = %#v, want hook-corrected value", args)
			}
			return "corrected call executed", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	hookClient := llm.NewClient()
	hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"value":"corrected"}}}`)}
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: name, Type: "prompt", Prompt: "correct input"})
	sess.hookRunner = runner

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "hook-corrected-prevalidation",
		Name:      name,
		Arguments: []byte(`{"value":{"invalid":true}}`),
	}, "")
	if res.IsError || executions != 1 {
		t.Fatalf("hook-corrected call did not execute: executions=%d result=%#v", executions, res)
	}
}

func TestExecTool_PreparedCallNormalizesBeforePreValidate(t *testing.T) {
	sess := newSession(t)
	t.Cleanup(sess.Close)

	const name = "prepared_normalization_order"
	var normalizations, prevalidations, executions int
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "normalization order probe", Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"value": map[string]any{"const": "normalized"},
			},
			"required": []any{"value"},
		}},
		OmitIntent: true,
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			normalizations++
			normalized := make(map[string]any, len(args))
			maps.Copy(normalized, args)
			delete(normalized, "legacy")
			normalized["value"] = "normalized"
			return normalized, nil
		},
		PreValidate: func(args map[string]any) error {
			prevalidations++
			if args["value"] != "normalized" {
				return errors.New("prevalidation observed unnormalized arguments")
			}
			return nil
		},
		Exec: func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			executions++
			if args["value"] != "normalized" {
				t.Fatalf("executor args = %#v, want normalized value", args)
			}
			if _, exists := args["legacy"]; exists {
				t.Fatalf("executor args = %#v, want legacy field removed", args)
			}
			return "ok", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "normalize-before-prevalidate",
		Name:      name,
		Arguments: []byte(`{"legacy":"provider default"}`),
	}, "")
	if res.IsError {
		t.Fatalf("normalized prepared call failed: %#v", res)
	}
	if normalizations != 1 || prevalidations != 1 || executions != 1 {
		t.Fatalf("calls = normalize %d, prevalidate %d, execute %d; want one normalization, one prevalidation, and one execution", normalizations, prevalidations, executions)
	}
}

func TestExecTool_PreparedCallRejectsNormalizedArguments(t *testing.T) {
	sess := newSession(t)
	t.Cleanup(sess.Close)

	const name = "prepared_normalized_rejection"
	wantErr := errors.New("normalized zero is forbidden")
	var normalizations, prevalidations, executions int
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "normalized rejection probe", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": []any{"string", "number"}},
			},
			"required": []any{"value"},
		}},
		OmitIntent: true,
		NormalizeArgs: func(args map[string]any) (map[string]any, error) {
			normalizations++
			if args["value"] == "0" {
				args["value"] = float64(0)
			}
			return args, nil
		},
		PreValidate: func(args map[string]any) error {
			prevalidations++
			if args["value"] == float64(0) {
				return wantErr
			}
			return nil
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			executions++
			return "unexpected", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "reject-normalized-zero",
		Name:      name,
		Arguments: []byte(`{"value":"0"}`),
	}, "")
	if !res.IsError || !res.PrevalOnly {
		t.Fatalf("result = %#v, want pre-dispatch normalized rejection", res)
	}
	if normalizations != 1 || prevalidations != 1 || executions != 0 {
		t.Fatalf("calls = normalize %d, prevalidate %d, execute %d; want 1, 1, 0", normalizations, prevalidations, executions)
	}
}
