package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestRegisteredTool_EmbedsLLMTool(t *testing.T) {
	t.Parallel()
	var execCalled bool
	rt := tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "test_embed",
			Description: "embedded tool",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			execCalled = true
			return "agent-exec-ok", nil
		},
	}

	// Verify Definition is accessible through embedded Tool.
	if rt.Definition.Name != "test_embed" {
		t.Fatalf("expected Name 'test_embed', got %q", rt.Definition.Name)
	}
	if rt.Definition.Description != "embedded tool" {
		t.Fatalf("expected Description 'embedded tool', got %q", rt.Definition.Description)
	}

	// Register should bridge Execute from Exec.
	reg := tool.NewRegistry()
	if err := reg.Register(rt); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := reg.Get("test_embed")
	if got == nil {
		t.Fatal("expected tool to be registered")
	}

	// Execute (the bridged llm.Tool.Execute) should work.
	if got.Execute == nil {
		t.Fatal("expected Execute to be bridged from Exec")
	}
	result, err := got.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result != "agent-exec-ok" {
		t.Fatalf("expected 'agent-exec-ok', got %v", result)
	}
	if !execCalled {
		t.Fatal("expected Exec to have been called via bridged Execute")
	}
}

func TestRegisteredTool_ExecuteNotBridgedWhenAlreadySet(t *testing.T) {
	t.Parallel()
	var executeCalled bool
	rt := tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "preset_exec",
			Description: "tool with execute already set",
		},
		Execute: func(ctx context.Context, args any) (any, error) {
			executeCalled = true
			return "direct-execute", nil
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "agent-exec", nil
		},
	}

	reg := tool.NewRegistry()
	if err := reg.Register(rt); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := reg.Get("preset_exec")
	if got == nil {
		t.Fatal("expected tool to be registered")
	}

	// Execute should be the original one, not bridged.
	result, err := got.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result != "direct-execute" {
		t.Fatalf("expected 'direct-execute', got %v", result)
	}
	if !executeCalled {
		t.Fatal("expected original Execute to have been called")
	}
}

func TestRegisteredTool_BridgedExecute_RejectsNonMapArgs(t *testing.T) {
	t.Parallel()
	rt := tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "bridge_reject",
			Description: "tool for testing non-map arg rejection",
		},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}

	reg := tool.NewRegistry()
	if err := reg.Register(rt); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := reg.Get("bridge_reject")

	// Passing a non-map arg should error.
	_, err := got.Execute(context.Background(), "not-a-map")
	if err == nil {
		t.Fatal("expected error for non-map args")
	}
}
