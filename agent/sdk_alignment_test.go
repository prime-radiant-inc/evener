package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestRegisteredTool_EmbedsLLMTool(t *testing.T) {
	var execCalled bool
	rt := registeredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        "test_embed",
				Description: "embedded tool",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
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
	reg := newToolRegistry()
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
	var executeCalled bool
	rt := registeredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name: "preset_exec",
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				executeCalled = true
				return "direct-execute", nil
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "agent-exec", nil
		},
	}

	reg := newToolRegistry()
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
	rt := registeredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name: "bridge_reject",
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}

	reg := newToolRegistry()
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

func TestSessionEvent_ToStreamEvent_TextDelta(t *testing.T) {
	e := SessionEvent{
		Kind: EventAssistantTextDelta,
		Data: AssistantTextDeltaData{Delta: "hello"},
	}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventTextDelta {
		t.Fatalf("expected TEXT_DELTA, got %s", se.Type)
	}
	if se.Delta != "hello" {
		t.Fatalf("expected delta 'hello', got %q", se.Delta)
	}
}

func TestSessionEvent_ToStreamEvent_TextStart(t *testing.T) {
	e := SessionEvent{Kind: EventAssistantTextStart}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventTextStart {
		t.Fatalf("expected TEXT_START, got %s", se.Type)
	}
}

func TestSessionEvent_ToStreamEvent_TextEnd(t *testing.T) {
	e := SessionEvent{Kind: EventAssistantTextEnd}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventTextEnd {
		t.Fatalf("expected TEXT_END, got %s", se.Type)
	}
}

func TestSessionEvent_ToStreamEvent_ToolCallStart(t *testing.T) {
	e := SessionEvent{
		Kind: EventToolCallStart,
		Data: ToolCallStartData{CallID: "c1", ToolName: "shell"},
	}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventToolCallStart {
		t.Fatalf("expected TOOL_CALL_START, got %s", se.Type)
	}
	if se.ToolCall == nil {
		t.Fatal("expected ToolCall to be set")
	}
	if se.ToolCall.ID != "c1" {
		t.Fatalf("expected call ID 'c1', got %q", se.ToolCall.ID)
	}
	if se.ToolCall.Name != "shell" {
		t.Fatalf("expected tool name 'shell', got %q", se.ToolCall.Name)
	}
}

func TestSessionEvent_ToStreamEvent_ToolCallEnd(t *testing.T) {
	e := SessionEvent{
		Kind: EventToolCallEnd,
		Data: ToolCallEndData{CallID: "c2", ToolName: "read_file"},
	}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventToolCallEnd {
		t.Fatalf("expected TOOL_CALL_END, got %s", se.Type)
	}
	if se.ToolCall == nil {
		t.Fatal("expected ToolCall to be set")
	}
	if se.ToolCall.ID != "c2" {
		t.Fatalf("expected call ID 'c2', got %q", se.ToolCall.ID)
	}
	if se.ToolCall.Name != "read_file" {
		t.Fatalf("expected tool name 'read_file', got %q", se.ToolCall.Name)
	}
}

func TestSessionEvent_ToStreamEvent_SessionStart(t *testing.T) {
	e := SessionEvent{Kind: EventSessionStart}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventStreamStart {
		t.Fatalf("expected STREAM_START, got %s", se.Type)
	}
}

func TestSessionEvent_ToStreamEvent_SessionEnd(t *testing.T) {
	e := SessionEvent{Kind: EventSessionEnd}
	se := e.ToStreamEvent()
	if se == nil {
		t.Fatal("expected non-nil StreamEvent")
	}
	if se.Type != llm.StreamEventFinish {
		t.Fatalf("expected FINISH, got %s", se.Type)
	}
}

func TestSessionEvent_ToStreamEvent_AgentOnlyEvent_ReturnsNil(t *testing.T) {
	agentOnlyKinds := []EventKind{
		EventSteeringInjected,
		EventTurnLimit,
		EventLoopDetection,
		EventSkillActivated,
		EventContextCompaction,
		EventWarning,
		EventError,
		EventSubagentStart,
		EventSubagentEnd,
		EventUserInput,
		EventToolCallOutputDelta,
	}
	for _, kind := range agentOnlyKinds {
		e := SessionEvent{Kind: kind}
		se := e.ToStreamEvent()
		if se != nil {
			t.Errorf("expected nil for agent-only event %s, got %+v", kind, se)
		}
	}
}
