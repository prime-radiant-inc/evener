package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/internal/llm"
)

// TestMCPManager_InMemory creates an in-process MCP server with a test tool,
// connects via InMemoryTransport, and verifies tool discovery and invocation.
func TestMCPManager_InMemory(t *testing.T) {
	ctx := context.Background()

	// Create a minimal MCP server with an "echo" tool.
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "v0.0.1",
	}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "The message to echo",
				},
			},
			"required": []string{"message"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		msg := args["message"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + msg}},
		}, nil
	})

	// Connect via InMemoryTransport.
	st, ct := mcp.NewInMemoryTransports()
	_, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	mgr, err := NewMCPManager(ctx, []MCPServerConfig{
		{Name: "testserver", Type: "stdio"},
	}, []mcp.Transport{ct})
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer mgr.Close()

	// Check tool discovery.
	defs := mgr.ToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(defs))
	}
	if defs[0].Name != "testserver__echo" {
		t.Errorf("tool name = %q, want testserver__echo", defs[0].Name)
	}
	if defs[0].Description == "" {
		t.Error("tool description should not be empty")
	}

	// Register and execute.
	reg := NewToolRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &fakeEnvForMCP{workDir: t.TempDir()}
	result := reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "call_test",
		Name:      "testserver__echo",
		Arguments: json.RawMessage(`{"message":"hello"}`),
	})
	if result.IsError {
		t.Fatalf("tool call returned error: %s", result.Output)
	}
	if result.Output != "echo: hello" {
		t.Errorf("tool output = %q, want %q", result.Output, "echo: hello")
	}
}

// TestMCPManager_MultipleServers verifies that tools from multiple servers
// are namespaced correctly and don't collide.
func TestMCPManager_MultipleServers(t *testing.T) {
	ctx := context.Background()

	// Server 1 has "greet" tool.
	server1 := mcp.NewServer(&mcp.Implementation{Name: "s1", Version: "v1"}, nil)
	server1.AddTool(&mcp.Tool{
		Name:        "greet",
		Description: "Greets someone",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "hello from s1"}},
		}, nil
	})

	// Server 2 also has "greet" tool.
	server2 := mcp.NewServer(&mcp.Implementation{Name: "s2", Version: "v1"}, nil)
	server2.AddTool(&mcp.Tool{
		Name:        "greet",
		Description: "Greets someone (s2)",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "hello from s2"}},
		}, nil
	})

	st1, ct1 := mcp.NewInMemoryTransports()
	st2, ct2 := mcp.NewInMemoryTransports()
	server1.Connect(ctx, st1, nil)
	server2.Connect(ctx, st2, nil)

	mgr, err := NewMCPManager(ctx, []MCPServerConfig{
		{Name: "alpha", Type: "stdio"},
		{Name: "beta", Type: "stdio"},
	}, []mcp.Transport{ct1, ct2})
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer mgr.Close()

	defs := mgr.ToolDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["alpha__greet"] {
		t.Error("missing alpha__greet")
	}
	if !names["beta__greet"] {
		t.Error("missing beta__greet")
	}

	// Verify invocation routes to correct server.
	reg := NewToolRegistry()
	mgr.RegisterTools(reg)
	env := &fakeEnvForMCP{workDir: t.TempDir()}

	r1 := reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID: "c1", Name: "alpha__greet", Arguments: json.RawMessage(`{}`),
	})
	if r1.Output != "hello from s1" {
		t.Errorf("alpha__greet output = %q, want %q", r1.Output, "hello from s1")
	}
	r2 := reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID: "c2", Name: "beta__greet", Arguments: json.RawMessage(`{}`),
	})
	if r2.Output != "hello from s2" {
		t.Errorf("beta__greet output = %q, want %q", r2.Output, "hello from s2")
	}
}

// TestMCPManager_BuiltinCollision verifies that registering an MCP tool
// that collides with a pre-existing tool returns an error.
func TestMCPManager_BuiltinCollision(t *testing.T) {
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "An echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil
	})

	st, ct := mcp.NewInMemoryTransports()
	server.Connect(ctx, st, nil)

	mgr, err := NewMCPManager(ctx, []MCPServerConfig{
		{Name: "s", Type: "stdio"},
	}, []mcp.Transport{ct})
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer mgr.Close()

	// Pre-register s__echo in the registry to simulate collision.
	reg := NewToolRegistry()
	reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "s__echo", Description: "pre-existing"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "built-in", nil
		},
	})

	err = mgr.RegisterTools(reg)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

// TestMCPManager_ToolNameTooLong verifies that an MCP tool whose namespaced
// name exceeds 64 chars is reported as an error.
func TestMCPManager_ToolNameTooLong(t *testing.T) {
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "v1"}, nil)
	longName := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghij" // 60 chars
	server.AddTool(&mcp.Tool{
		Name:        longName,
		Description: "Too long",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})

	st, ct := mcp.NewInMemoryTransports()
	server.Connect(ctx, st, nil)

	// "longservername__" (16) + 60 = 76 chars > 64 limit
	mgr, err := NewMCPManager(ctx, []MCPServerConfig{
		{Name: "longservername", Type: "stdio"},
	}, []mcp.Transport{ct})
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer mgr.Close()

	reg := NewToolRegistry()
	err = mgr.RegisterTools(reg)
	if err == nil {
		t.Fatal("expected tool name too long error, got nil")
	}
}

// TestMCPManager_Empty verifies that an empty config list returns nil manager.
func TestMCPManager_Empty(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMCPManager(ctx, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		t.Error("expected nil manager for empty config")
	}
}

func TestTransportForConfig_Types(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MCPServerConfig
		wantErr bool
	}{
		{"stdio valid", MCPServerConfig{Type: "stdio", Command: "cmd"}, false},
		{"stdio empty command", MCPServerConfig{Type: "stdio"}, true},
		{"sse valid", MCPServerConfig{Type: "sse", URL: "http://localhost:8080"}, false},
		{"sse empty url", MCPServerConfig{Type: "sse"}, true},
		{"http valid", MCPServerConfig{Type: "http", URL: "http://localhost:8080"}, false},
		{"http empty url", MCPServerConfig{Type: "http"}, true},
		{"unknown type", MCPServerConfig{Type: "websocket"}, true},
		{"default (empty type) with command", MCPServerConfig{Command: "cmd"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := transportForConfig(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if transport == nil {
				t.Error("expected non-nil transport")
			}
		})
	}
}

func TestMergeEnv(t *testing.T) {
	// mergeEnv uses os.Environ so we can only test that extra vars appear
	// and that overriding works correctly.
	result := mergeEnv(map[string]string{"MCP_TEST_UNIQUE_KEY_42": "value42"})

	found := false
	for _, e := range result {
		if e == "MCP_TEST_UNIQUE_KEY_42=value42" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected MCP_TEST_UNIQUE_KEY_42=value42 in merged env")
	}

	// Setting PATH should replace, not duplicate.
	result2 := mergeEnv(map[string]string{"PATH": "/custom/path"})
	pathCount := 0
	for _, e := range result2 {
		key, _, _ := strings.Cut(e, "=")
		if key == "PATH" {
			pathCount++
		}
	}
	if pathCount != 1 {
		t.Errorf("expected exactly 1 PATH entry, got %d", pathCount)
	}
}
