package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

func requireNpx(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not found, skipping real MCP server test")
	}
}

func newEverythingManager(t *testing.T) *Manager {
	t.Helper()
	requireNpx(t)
	// 60s instead of 30s: npx startup is fork+exec heavy and load-sensitive.
	// These are correctness tests, not perf budgets — give them headroom so
	// they don't flake under parallel `go test ./... -count=1`.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{{
		Name:    "everything",
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-everything"},
	}}, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func TestRealMCP_ToolDiscovery(t *testing.T) {
	mgr := newEverythingManager(t)

	defs := mgr.ToolDefinitions()
	if len(defs) < 5 {
		t.Fatalf("expected at least 5 tools, got %d", len(defs))
	}

	// All names must be prefixed with "everything__".
	for _, d := range defs {
		if !strings.HasPrefix(d.Name, "everything__") {
			t.Errorf("tool %q missing everything__ prefix", d.Name)
		}
	}

	// Known tools must be present.
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"everything__echo", "everything__get_sum", "everything__get_tiny_image"} {
		if !names[want] {
			t.Errorf("expected tool %q not found", want)
		}
	}

	// Each tool has a non-empty description and object-typed parameters.
	for _, d := range defs {
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		if d.Parameters == nil {
			t.Errorf("tool %q has nil Parameters", d.Name)
			continue
		}
		if d.Parameters["type"] != "object" {
			t.Errorf("tool %q Parameters type = %v, want \"object\"", d.Name, d.Parameters["type"])
		}
	}
}

func TestRealMCP_Echo(t *testing.T) {
	mgr := newEverythingManager(t)

	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	result := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "call_echo",
		Name:      "everything__echo",
		Arguments: json.RawMessage(`{"message":"integration test"}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "integration test") {
		t.Errorf("output %q does not contain %q", result.Output, "integration test")
	}
}

func TestRealMCP_GetSum(t *testing.T) {
	mgr := newEverythingManager(t)

	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	result := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "call_sum",
		Name:      "everything__get_sum",
		Arguments: json.RawMessage(`{"a":17,"b":25}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "42") {
		t.Errorf("output %q does not contain %q", result.Output, "42")
	}
}

func TestRealMCP_ImageContent(t *testing.T) {
	mgr := newEverythingManager(t)

	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	result := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "call_image",
		Name:      "everything__get_tiny_image",
		Arguments: json.RawMessage(`{}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output for get-tiny-image")
	}
	// Server returns text ("image") and a base64-encoded PNG.
	// mcpResultToString marshals non-text content to JSON containing image data.
	if !strings.Contains(strings.ToLower(result.Output), "image") {
		t.Errorf("output missing text about image: %s", result.Output)
	}
	// The JSON-marshaled image block contains a "data" field with base64 content.
	if !strings.Contains(result.Output, `"data"`) {
		t.Errorf("output missing image data field: %s", result.Output)
	}
}

func TestRealMCP_EnvPassing(t *testing.T) {
	requireNpx(t)
	// 60s instead of 30s: npx startup is fork+exec heavy and load-sensitive.
	// These are correctness tests, not perf budgets — give them headroom so
	// they don't flake under parallel `go test ./... -count=1`.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{{
		Name:    "everything",
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-everything"},
		Env:     map[string]string{"MCP_TEST_MARKER": "serf_integration_12345"},
	}}, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)

	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	result := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "call_env",
		Name:      "everything__get_env",
		Arguments: json.RawMessage(`{}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "MCP_TEST_MARKER") {
		t.Errorf("output missing MCP_TEST_MARKER key: %s", result.Output)
	}
	if !strings.Contains(result.Output, "serf_integration_12345") {
		t.Errorf("output missing serf_integration_12345 value: %s", result.Output)
	}
}

func TestRealMCP_AnnotatedMessage(t *testing.T) {
	mgr := newEverythingManager(t)

	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	result := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "call_annotated",
		Name:      "everything__get_annotated_message",
		Arguments: json.RawMessage(`{"messageType":"error"}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Error") {
		t.Errorf("output missing 'Error': %s", result.Output)
	}
}
