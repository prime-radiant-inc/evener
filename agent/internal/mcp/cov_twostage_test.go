package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// TestIntgMCP_NewManager_SiblingSurvivesFailure is the core two-stage assembly
// contract test: server "a" fails at the connect stage (its tools/list call is
// rejected, same failure arm as TestIntgMCP_NewManager_ListToolsError) while
// sibling server "b" is healthy. NewManager must still connect and discover
// "b"'s tools, and RegisterTools/ExecuteCall must still be able to invoke them
// — "a"'s failure must not poison the batch.
func TestIntgMCP_NewManager_SiblingSurvivesFailure(t *testing.T) {
	ctx := context.Background()

	// Server "a": initialize succeeds but tools/list is rejected by middleware.
	failingServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "a", Version: "v1"}, nil)
	failingServer.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" {
				return nil, errors.New("intg: tools/list disabled")
			}
			return next(ctx, method, req)
		}
	})
	stA, ctA := mcpsdk.NewInMemoryTransports()
	if _, err := failingServer.Connect(ctx, stA, nil); err != nil {
		t.Fatalf("server a connect: %v", err)
	}

	// Server "b": a normal healthy server with one "echo" tool.
	healthyServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "b", Version: "v1"}, nil)
	healthyServer.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
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
	stB, ctB := mcpsdk.NewInMemoryTransports()
	if _, err := healthyServer.Connect(ctx, stB, nil); err != nil {
		t.Fatalf("server b connect: %v", err)
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "a", Type: "stdio"},
		{Name: "b", Type: "stdio"},
	}, []mcpsdk.Transport{ctA, ctB})
	if mgr == nil {
		t.Fatal("expected a non-nil manager when only one of two servers fails")
	}
	defer mgr.Close()

	if len(outcomes) != 1 || outcomes[0].Name != "a" || outcomes[0].Stage != "connect" {
		t.Fatalf("want exactly one connect outcome for %q, got %+v", "a", outcomes)
	}

	servers := mgr.Servers()
	byName := make(map[string]mcpconfig.ServerInfo, len(servers))
	for _, s := range servers {
		byName[s.Name] = s
	}
	if got := byName["a"]; got.Status != "failed" {
		t.Errorf("server a status = %q, want failed (%+v)", got.Status, got)
	}
	if got := byName["b"]; got.Status != "connected" {
		t.Errorf("server b status = %q, want connected (%+v)", got.Status, got)
	}

	// b's tool must be discoverable...
	const wantTool = "b__echo"
	found := false
	for _, td := range mgr.ToolDefinitions() {
		if td.Name == wantTool {
			found = true
		}
	}
	if !found {
		t.Fatalf("healthy sibling's tool %q missing from ToolDefinitions: %+v", wantTool, mgr.ToolDefinitions())
	}

	// ...and registrable/callable, proving that "a"'s failure didn't poison "b".
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	res := reg.ExecuteCall(ctx, &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
		ID:        "call_b_echo",
		Name:      wantTool,
		Arguments: json.RawMessage(`{"message":"hi"}`),
	})
	if res.IsError {
		t.Fatalf("%s call errored: %s", wantTool, res.Output)
	}
	if res.Output != "echo: hi" {
		t.Errorf("%s output = %q, want %q", wantTool, res.Output, "echo: hi")
	}
}
