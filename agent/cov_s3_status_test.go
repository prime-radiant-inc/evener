package agent

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/mcpconfig"
)

// TestS3Cov_DetailedStatus_MCPBranch drives DetailedStatus with a real in-memory
// MCP manager attached, so the tool-categorization arm that labels a tool
// "mcp:<server>" (and populates ds.MCP) is exercised.
func TestS3Cov_DetailedStatus_MCPBranch(t *testing.T) {
	t.Parallel()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "ext", Version: "1.0.0"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "greet",
		Description: "greets",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hi"}}}, nil
	})

	st, ct := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mgr, err := mcp.NewManager(ctx, []mcpconfig.ServerConfig{{Name: "ext", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){
			func(context.Context) (mcpsdk.Transport, error) { return ct, nil },
		})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	sess := newSession(t)
	if err := mgr.RegisterTools(sess.reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	sess.mcpMgr = mgr

	ds := sess.DetailedStatus()
	if len(ds.MCP) == 0 {
		t.Fatal("expected MCP servers in DetailedStatus")
	}
	var found bool
	for _, ti := range ds.Tools {
		if ti.Name == "ext__greet" {
			found = true
			if ti.Source != "mcp:ext" {
				t.Fatalf("ext__greet source = %q, want mcp:ext", ti.Source)
			}
		}
	}
	if !found {
		t.Fatalf("ext__greet tool not categorized in DetailedStatus: %+v", ds.Tools)
	}
}
