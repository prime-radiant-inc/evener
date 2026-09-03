package agent

import (
	"context"
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
