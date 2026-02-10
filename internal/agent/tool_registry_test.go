package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/llm"
)

func TestToolRegistry_UnknownTool_ReturnsErrorResult(t *testing.T) {
	r := NewToolRegistry()
	// No tools registered.
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "does_not_exist",
		Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Fatalf("expected error")
	}
	if !strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("output: %q", res.Output)
	}
}

func TestToolRegistry_SchemaValidationError_IsReturnedToModel(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name: "t",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"required_field": map[string]any{"type": "string"},
				},
				"required": []string{"required_field"},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return "ok", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Fatalf("expected error")
	}
	if !strings.Contains(res.Output, "schema validation failed") {
		t.Fatalf("output: %q", res.Output)
	}
}

func TestToolRegistry_InvalidArgumentsJSON_IsReturnedToModel(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "t"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return "ok", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{"unterminated":`),
	})
	if !res.IsError {
		t.Fatalf("expected error")
	}
	if !strings.Contains(res.Output, "invalid tool arguments JSON") {
		t.Fatalf("output: %q", res.Output)
	}
}

func TestToolRegistry_ExecError_IsReturnedToModel(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "t"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return "", context.DeadlineExceeded
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Fatalf("expected error")
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Fatalf("expected non-empty error output")
	}
}

func TestToolRegistry_TruncationMarkers(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "t"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return strings.Repeat("x", 2000), nil
		},
		Limit: ToolOutputLimit{MaxChars: 200, Strategy: TruncTail},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error")
	}
	if len(res.FullOutput) != 2000 {
		t.Fatalf("full output length: got %d want 2000", len(res.FullOutput))
	}
	if !strings.Contains(res.Output, "Tool output was truncated") || !strings.Contains(res.Output, "event stream") {
		t.Fatalf("expected truncation marker, got: %q", res.Output)
	}
	if len(res.Output) > 400 {
		t.Fatalf("expected truncated output to be small, got %d chars", len(res.Output))
	}
}

func TestToolRegistry_TruncationOrder_CharsFirstThenLines(t *testing.T) {
	r := NewToolRegistry()
	full := strings.Repeat("0123456789\n", 100) // ~1100 chars, many lines
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "t"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return full, nil
		},
		Limit: ToolOutputLimit{MaxChars: 200, MaxLines: 2, Strategy: TruncTail},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error")
	}
	if !strings.Contains(res.Output, "characters were removed") {
		t.Fatalf("expected character truncation marker (chars-first), got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "lines omitted") {
		t.Fatalf("expected line truncation marker (lines-second), got:\n%s", res.Output)
	}
}

func TestToolRegistry_TruncationLines_UsesHeadTailAndOmittedMarker(t *testing.T) {
	r := NewToolRegistry()
	full := strings.Join([]string{
		"l0",
		"l1",
		"l2",
		"l3",
		"l4",
		"l5",
		"l6",
		"l7",
		"l8",
		"l9",
	}, "\n")
	if err := r.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "t"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return full, nil
		},
		Limit: ToolOutputLimit{MaxChars: 10_000, MaxLines: 4, Strategy: TruncHeadTail},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "t",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error")
	}
	// head_count=2, tail_count=2, omitted=6 per spec.
	for _, want := range []string{"l0", "l1", "[... 6 lines omitted ...]", "l8", "l9"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("missing %q in output:\n%s", want, res.Output)
		}
	}
	// Ensure we actually kept the tail and didn't just keep the first lines.
	if strings.Contains(res.Output, "l2") || strings.Contains(res.Output, "l7") {
		t.Fatalf("expected middle lines to be omitted, got:\n%s", res.Output)
	}
}

func TestDefaultToolLimit_MatchesSpecTable(t *testing.T) {
	type want struct {
		tool   string
		chars  int
		lines  int
		strat  TruncationStrategy
	}
	cases := []want{
		{tool: "read_file", chars: 50_000, lines: 0, strat: TruncHeadTail},
		{tool: "shell", chars: 30_000, lines: 256, strat: TruncHeadTail},
		{tool: "grep", chars: 20_000, lines: 200, strat: TruncTail},
		{tool: "glob", chars: 20_000, lines: 500, strat: TruncTail},
		{tool: "edit_file", chars: 10_000, lines: 0, strat: TruncTail},
		{tool: "apply_patch", chars: 10_000, lines: 0, strat: TruncTail},
		{tool: "write_file", chars: 1_000, lines: 0, strat: TruncTail},
		{tool: "spawn_agent", chars: 20_000, lines: 0, strat: TruncHeadTail},
	}
	for _, tc := range cases {
		lim := defaultToolLimit(tc.tool)
		if lim.MaxChars != tc.chars || lim.MaxLines != tc.lines || lim.Strategy != tc.strat {
			t.Fatalf("%s: got=%+v want MaxChars=%d MaxLines=%d Strategy=%s", tc.tool, lim, tc.chars, tc.lines, tc.strat)
		}
	}
}
