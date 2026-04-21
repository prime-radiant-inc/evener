package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/llm"
)

func TestToolRegistry_ToolStateResult_CarriesStateAsSideChannel(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "snap"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return ToolStateResult{
				Output: "terse summary for LLM",
				State: []map[string]any{
					{"id": 1, "description": "first", "status": "done"},
					{"id": 2, "description": "second", "status": "open"},
				},
			}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res := r.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "snap",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}
	if res.Output != "terse summary for LLM" {
		t.Errorf("Output: got %q, want %q", res.Output, "terse summary for LLM")
	}
	if len(res.ToolState) == 0 {
		t.Fatalf("expected ToolState to be populated")
	}
	var parsed []map[string]any
	if err := json.Unmarshal(res.ToolState, &parsed); err != nil {
		t.Fatalf("ToolState not valid JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 state entries, got %d", len(parsed))
	}
	if parsed[0]["description"] != "first" {
		t.Errorf("first entry description: got %v, want first", parsed[0]["description"])
	}
}

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
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: "t",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"required_field": map[string]any{"type": "string"},
				},
				"required": []string{"required_field"},
			},
		}},
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "t"}},
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "t"}},
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "t"}},
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "t"}},
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "t"}},
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

func TestToolRegistry_Unregister(t *testing.T) {
	r := NewToolRegistry()
	_ = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "foo", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	r.Unregister("foo")
	if r.Get("foo") != nil {
		t.Fatal("expected nil after Unregister")
	}
}

func TestToolRegistry_Get(t *testing.T) {
	r := NewToolRegistry()
	_ = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "bar", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	got := r.Get("bar")
	if got == nil {
		t.Fatal("expected non-nil from Get")
	}
	if got.Definition.Name != "bar" {
		t.Fatalf("got name %q, want bar", got.Definition.Name)
	}
	if r.Get("nonexistent") != nil {
		t.Fatal("expected nil for unknown tool")
	}
}

func TestToolRegistry_Names(t *testing.T) {
	r := NewToolRegistry()
	_ = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "alpha", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) { return "", nil },
	})
	_ = r.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "beta", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) { return "", nil },
	})
	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("names count = %d, want 2", len(names))
	}
	// Names should be sorted for determinism
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("unexpected names: %v", names)
	}
}

func TestToolRegistry_Register_RejectsNonObjectRootSchema(t *testing.T) {
	reg := NewToolRegistry()
	err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: "bad_tool",
			Parameters: map[string]any{
				"type": "string",
			},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	if err == nil {
		t.Fatal("expected error for non-object root schema type")
	}
	if !strings.Contains(err.Error(), "object") {
		t.Fatalf("error should mention 'object': %v", err)
	}
}

func TestToolRegistry_Register_LatestWinsOnNameCollision(t *testing.T) {
	reg := NewToolRegistry()
	first := RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "my_tool", Description: "first"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "first", nil
		},
	}
	second := RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "my_tool", Description: "second"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "second", nil
		},
	}
	if err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(second); err != nil {
		t.Fatal(err)
	}
	got := reg.Get("my_tool")
	if got == nil || got.Definition.Description != "second" {
		t.Fatalf("expected latest-wins: got description=%q", got.Definition.Description)
	}
}

func TestTruncateChars_UTF8Aware(t *testing.T) {
	// 5 emoji characters, each 4 bytes in UTF-8: 20 bytes but 5 runes.
	input := "😀😁😂🤣😃"
	if utf8.RuneCountInString(input) != 5 {
		t.Fatal("test setup: expected 5 runes")
	}
	if len(input) != 20 {
		t.Fatal("test setup: expected 20 bytes")
	}

	// Truncate to 3 characters (runes), not 3 bytes.
	result := truncateChars(input, 3, TruncHeadTail)
	// Should contain valid UTF-8 (no broken characters).
	if !utf8.ValidString(result) {
		t.Error("truncated result must be valid UTF-8")
	}
	// The marker separates head and tail. With max=3, head=1, tail=2.
	// So we should see the first emoji and last two emojis, with a marker in between.
	if !strings.Contains(result, "😀") {
		t.Error("expected first emoji in head portion")
	}
	if !strings.Contains(result, "😃") {
		t.Error("expected last emoji in tail portion")
	}

	// TruncTail: keep last 3 characters
	result2 := truncateChars(input, 3, TruncTail)
	if !utf8.ValidString(result2) {
		t.Error("TruncTail result must be valid UTF-8")
	}
	if !strings.Contains(result2, "😂🤣😃") {
		t.Error("TruncTail should keep last 3 emojis")
	}
}

func TestToolRegistry_Middleware_CalledBeforeExecution(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "test_tool",
			Description: "test",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "executed", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	var middlewareCalled bool
	reg.Use(func(ctx context.Context, name string, args map[string]any) error {
		middlewareCalled = true
		return nil
	})

	result := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "test_tool", ID: "c1"})
	if !middlewareCalled {
		t.Error("middleware must be called before execution")
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Output)
	}
}

func TestToolRegistry_Middleware_CanBlockExecution(t *testing.T) {
	reg := NewToolRegistry()
	var execCalled bool
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "test_tool",
			Description: "test",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			execCalled = true
			return "executed", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	reg.Use(func(ctx context.Context, name string, args map[string]any) error {
		return fmt.Errorf("permission denied: tool blocked by policy")
	})

	result := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "test_tool", ID: "c1"})
	if !result.IsError {
		t.Error("middleware rejection should produce error result")
	}
	if !strings.Contains(result.Output, "permission denied") {
		t.Errorf("expected 'permission denied' in output, got: %s", result.Output)
	}
	if execCalled {
		t.Error("tool should not have been executed when middleware blocked")
	}
}

func TestToolRegistry_Register_WarnsOnEmptyDescription(t *testing.T) {
	reg := NewToolRegistry()
	// Capture log output.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "no_desc_tool",
			Description: "",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("expected registration to succeed, got: %v", err)
	}

	// Tool should still be registered.
	tool := reg.Get("no_desc_tool")
	if tool == nil {
		t.Error("tool should still be registered despite empty description")
	}

	// Should have logged a warning.
	if !strings.Contains(buf.String(), "WARNING") || !strings.Contains(buf.String(), "no_desc_tool") {
		t.Errorf("expected warning about empty description, got log: %q", buf.String())
	}
}

func TestToolRegistry_Register_RecoversPanicInSchemaCompilation(t *testing.T) {
	// compileSchema wraps the jsonschema library which has multiple panic()
	// sites. A recover() in compileSchema must convert panics to errors so
	// that a malformed MCP or plugin tool schema doesn't crash the process.

	// We can't easily trigger the exact library panic in a unit test, so
	// we verify the contract: Register never panics, even with pathological
	// schema inputs that push the library to its limits.
	reg := NewToolRegistry()
	pathological := []struct {
		name   string
		params map[string]any
	}{
		{"nil-property-value", map[string]any{
			"type":       "object",
			"properties": map[string]any{"x": nil},
		}},
		{"empty-map", map[string]any{}},
		{"nested-self-ref", map[string]any{
			"type":       "object",
			"properties": map[string]any{"x": map[string]any{"$ref": "#"}},
		}},
		{"deeply-nested", func() map[string]any {
			inner := map[string]any{"type": "string"}
			for i := 0; i < 50; i++ {
				inner = map[string]any{
					"type":       "object",
					"properties": map[string]any{"x": inner},
				}
			}
			return inner
		}()},
	}

	for _, tc := range pathological {
		t.Run(tc.name, func(t *testing.T) {
			// This must not panic — error return is acceptable.
			_ = reg.Register(RegisteredTool{
				Tool: llm.Tool{Definition: llm.ToolDefinition{
					Name:       "test_" + tc.name,
					Parameters: tc.params,
				}},
				Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
					return nil, nil
				},
			})
		})
	}
}

func TestRegister_ReusesCompiledSchemaOnReregistration(t *testing.T) {
	// Regression test: registerCoreTools re-registers tools that were already
	// registered by NewToolRegistry. Previously, each Register call recompiled
	// the schema from scratch. If the jsonschema library panicked transiently
	// (e.g. os.Getwd() failure in a deleted worktree), the re-registration
	// failed even though the first registration succeeded. Register should
	// reuse the already-compiled Schema when re-registering a tool with
	// identical parameters.
	reg := NewToolRegistry()
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
		"required":   []string{"file_path"},
	}
	noop := func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
		return nil, nil
	}

	// First registration compiles the schema.
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "read_file", Parameters: params}},
		Exec: noop,
	}); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Verify schema was compiled.
	reg.mu.RLock()
	first := reg.tools["read_file"]
	reg.mu.RUnlock()
	if first.Schema == nil {
		t.Fatal("expected compiled schema after first registration")
	}

	// Re-register the same tool (as registerCoreTools does) without Schema.
	// Register should reuse the compiled Schema from the first registration.
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "read_file", Parameters: params}},
		Exec: noop,
	}); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	reg.mu.RLock()
	second := reg.tools["read_file"]
	reg.mu.RUnlock()
	if second.Schema == nil {
		t.Fatal("expected compiled schema after second registration")
	}
	if first.Schema != second.Schema {
		t.Fatal("Register should reuse the compiled schema from the first registration, got a different pointer")
	}
}

func TestExecuteCall_ImageResult_PopulatesImageFields(t *testing.T) {
	reg := NewToolRegistry()
	imgData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:       "read_file",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []string{"file_path"}},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return ImageResult{
				Text:      "Image file: photo.png (PNG, 4 bytes)",
				Data:      imgData,
				MediaType: "image/png",
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	res := reg.ExecuteCall(context.Background(), NewLocalExecutionEnvironment(t.TempDir()), llm.ToolCallData{
		ID:        "c1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path": "photo.png"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Image file: photo.png") {
		t.Fatalf("expected text in output, got: %q", res.Output)
	}
	if !bytes.Equal(res.ImageData, imgData) {
		t.Fatalf("ImageData mismatch: got %v, want %v", res.ImageData, imgData)
	}
	if res.ImageMediaType != "image/png" {
		t.Fatalf("ImageMediaType = %q, want image/png", res.ImageMediaType)
	}
}

func TestDefaultToolLimit_MatchesSpecTable(t *testing.T) {
	type want struct {
		tool  string
		chars int
		lines int
		strat TruncationStrategy
	}
	cases := []want{
		{tool: "read_file", chars: 50_000, lines: 0, strat: TruncHeadTail},
		{tool: "shell", chars: 30_000, lines: 512, strat: TruncHeadTail},
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
