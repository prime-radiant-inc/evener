package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
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
			"normalized": map[string]any{"const": "successor"},
		},
		"required": []any{"value"},
	}
	var originalPrevalidations, originalExecutions int
	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: name, Description: "original registration", Parameters: params},
		OmitIntent: true,
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
