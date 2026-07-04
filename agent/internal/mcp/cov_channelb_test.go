package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// TestMCPManager_ChannelBError_IsErrorTypedResult verifies that a Channel-B
// error (CallToolResult.IsError==true, e.g. a Linear 400) reaches
// tool.Registry.ExecuteCall as an error-typed ExecResult, not a green
// success carrying the error text.
func TestMCPManager_ChannelBError_IsErrorTypedResult(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "fail",
		Description: "Always reports a server-side error",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom: upstream 400"}},
		}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	res := reg.ExecuteCall(ctx, &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
		ID: "c", Name: "s__fail", Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Fatalf("Channel-B error rendered as success: IsError=false, output=%q", res.Output)
	}
	if !strings.Contains(res.Output, "boom: upstream 400") {
		t.Errorf("error body missing from result: %q", res.Output)
	}
}
