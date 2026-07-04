package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// TestMCPManager_InMemory creates an in-process MCP server with a test tool,
// connects via InMemoryTransport, and verifies tool discovery and invocation.
func TestMCPManager_InMemory(t *testing.T) {
	ctx := context.Background()

	// Create a minimal MCP server with an "echo" tool.
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "test-server",
		Version: "v0.0.1",
	}, nil)
	server.AddTool(&mcpsdk.Tool{
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
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		msg := args["message"].(string)
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + msg}},
		}, nil
	})

	// Connect via InMemoryTransport.
	st, ct := mcpsdk.NewInMemoryTransports()
	_, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "testserver", Type: "stdio"},
	}, []mcpsdk.Transport{ct})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
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
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
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
	server1 := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s1", Version: "v1"}, nil)
	server1.AddTool(&mcpsdk.Tool{
		Name:        "greet",
		Description: "Greets someone",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hello from s1"}},
		}, nil
	})

	// Server 2 also has "greet" tool.
	server2 := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s2", Version: "v1"}, nil)
	server2.AddTool(&mcpsdk.Tool{
		Name:        "greet",
		Description: "Greets someone (s2)",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hello from s2"}},
		}, nil
	})

	st1, ct1 := mcpsdk.NewInMemoryTransports()
	st2, ct2 := mcpsdk.NewInMemoryTransports()
	if _, err := server1.Connect(ctx, st1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server2.Connect(ctx, st2, nil); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "alpha", Type: "stdio"},
		{Name: "beta", Type: "stdio"},
	}, []mcpsdk.Transport{ct1, ct2})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
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
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatal(err)
	}
	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}

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

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "An echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}},
		}, nil
	})

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "s", Type: "stdio"},
	}, []mcpsdk.Transport{ct})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	// Pre-register s__echo in the registry to simulate collision.
	reg := tool.NewRegistry()
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "s__echo", Description: "pre-existing"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "built-in", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	err := mgr.RegisterTools(reg)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

// TestMCPManager_ToolNameTooLong verifies that an MCP tool whose namespaced
// name exceeds 64 chars is reported as an error.
func TestMCPManager_ToolNameTooLong(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	longName := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghij" // 60 chars
	server.AddTool(&mcpsdk.Tool{
		Name:        longName,
		Description: "Too long",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}

	// "longservername__" (16) + 60 = 76 chars > 64 limit
	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "longservername", Type: "stdio"},
	}, []mcpsdk.Transport{ct})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	err := mgr.RegisterTools(reg)
	if err == nil {
		t.Fatal("expected tool name too long error, got nil")
	}
}

// TestMCPManager_Empty verifies that an empty config list returns nil manager.
func TestMCPManager_Empty(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		t.Error("expected nil manager for empty config")
	}
}

func TestTransportForConfig_Types(t *testing.T) {
	tests := []struct {
		name     string
		cfg      mcpconfig.ServerConfig
		wantErr  bool
		wantType string // concrete type label for non-error cases
	}{
		{"stdio valid", mcpconfig.ServerConfig{Type: "stdio", Command: "cmd"}, false, "CommandTransport"},
		{"stdio empty command", mcpconfig.ServerConfig{Type: "stdio"}, true, ""},
		{"sse valid", mcpconfig.ServerConfig{Type: "sse", URL: "http://localhost:8080"}, false, "SSEClientTransport"},
		{"sse empty url", mcpconfig.ServerConfig{Type: "sse"}, true, ""},
		{"http valid", mcpconfig.ServerConfig{Type: "http", URL: "http://localhost:8080"}, false, "StreamableClientTransport"},
		{"http empty url", mcpconfig.ServerConfig{Type: "http"}, true, ""},
		{"unknown type", mcpconfig.ServerConfig{Type: "websocket"}, true, ""},
		{"default (empty type) with command", mcpconfig.ServerConfig{Command: "cmd"}, false, "CommandTransport"},
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
				return
			}
			if transport == nil {
				t.Error("expected non-nil transport")
				return
			}
			switch tt.wantType {
			case "CommandTransport":
				if _, ok := transport.(*mcpsdk.CommandTransport); !ok {
					t.Errorf("got %T, want *mcpsdk.CommandTransport", transport)
				}
			case "SSEClientTransport":
				if _, ok := transport.(*mcpsdk.SSEClientTransport); !ok {
					t.Errorf("got %T, want *mcpsdk.SSEClientTransport", transport)
				}
			case "StreamableClientTransport":
				if _, ok := transport.(*mcpsdk.StreamableClientTransport); !ok {
					t.Errorf("got %T, want *mcpsdk.StreamableClientTransport", transport)
				}
			}
		})
	}
}

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"echo", "echo"},
		{"get-sum", "get_sum"},
		{"server__get-tiny-image", "server__get_tiny_image"},
		{"no-hyphens-at-all", "no_hyphens_at_all"},
		{"already_underscores", "already_underscores"},
	}
	for _, tt := range tests {
		got := sanitizeToolName(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestMCPManager_Servers verifies that Servers() returns per-server info
// with names and namespaced tool names.
func TestMCPManager_Servers(t *testing.T) {
	ctx := context.Background()

	server1 := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s1", Version: "v1"}, nil)
	server1.AddTool(&mcpsdk.Tool{
		Name:        "greet",
		Description: "Greets",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})
	server1.AddTool(&mcpsdk.Tool{
		Name:        "farewell",
		Description: "Says bye",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})

	server2 := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s2", Version: "v1"}, nil)
	server2.AddTool(&mcpsdk.Tool{
		Name:        "search",
		Description: "Search",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})

	st1, ct1 := mcpsdk.NewInMemoryTransports()
	st2, ct2 := mcpsdk.NewInMemoryTransports()
	if _, err := server1.Connect(ctx, st1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server2.Connect(ctx, st2, nil); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "alpha", Type: "stdio"},
		{Name: "beta", Type: "stdio"},
	}, []mcpsdk.Transport{ct1, ct2})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	servers := mgr.Servers()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}

	// Find alpha and beta.
	byName := map[string]mcpconfig.ServerInfo{}
	for _, s := range servers {
		byName[s.Name] = s
	}

	alpha, ok := byName["alpha"]
	if !ok {
		t.Fatal("missing server alpha")
	}
	if len(alpha.Tools) != 2 {
		t.Errorf("alpha tools: got %d, want 2", len(alpha.Tools))
	}
	alphaTools := make(map[string]bool, len(alpha.Tools))
	for _, name := range alpha.Tools {
		alphaTools[name] = true
	}
	if !alphaTools["alpha__greet"] {
		t.Error("alpha tools missing alpha__greet")
	}
	if !alphaTools["alpha__farewell"] {
		t.Error("alpha tools missing alpha__farewell")
	}

	beta, ok := byName["beta"]
	if !ok {
		t.Fatal("missing server beta")
	}
	if len(beta.Tools) != 1 {
		t.Errorf("beta tools: got %d, want 1", len(beta.Tools))
	}
	if beta.Tools[0] != "beta__search" {
		t.Errorf("beta tool name = %q, want beta__search", beta.Tools[0])
	}
}

// TestMCPManager_Servers_Nil verifies Servers() on nil manager returns nil.
func TestMCPManager_Servers_Nil(t *testing.T) {
	var mgr *Manager
	servers := mgr.Servers()
	if servers != nil {
		t.Errorf("expected nil, got %v", servers)
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

	// Setting PATH should replace, not duplicate, and must carry the override value.
	result2 := mergeEnv(map[string]string{"PATH": "/custom/path"})
	pathCount := 0
	for _, e := range result2 {
		key, val, _ := strings.Cut(e, "=")
		if key == "PATH" {
			pathCount++
			if val != "/custom/path" {
				t.Errorf("PATH value = %q, want /custom/path", val)
			}
		}
	}
	if pathCount != 1 {
		t.Errorf("expected exactly 1 PATH entry, got %d", pathCount)
	}
}
