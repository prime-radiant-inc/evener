package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
)

// These tests cover RegisterTools' per-server register-stage outcomes: a
// server whose tools fail validation or collide with an existing name is
// demoted to "failed" and reported in the returned []ServerOutcome, but the
// batch keeps going. Two properties are load-bearing and easy to get subtly
// wrong, so each gets its own test:
//
//   - Rollback must remove ONLY the names the failing server itself
//     registered before hitting the failure — never a name that belongs to
//     an earlier server that already won that name.
//   - ToolDefinitions() must reflect the post-register state: a server
//     demoted to "failed" during RegisterTools contributes no definitions.

// newRollbackTestServer builds an in-memory MCP server named name, exposing
// one no-op tool per entry in toolNames, and returns the client-side
// transport ready for NewManager. The handler is never invoked by these
// tests; only tool discovery and namespaced-name registration are exercised.
func newRollbackTestServer(t *testing.T, name string, toolNames ...string) mcpsdk.Transport {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	for _, tn := range toolNames {
		server.AddTool(&mcpsdk.Tool{
			Name:        tn,
			Description: "test tool " + tn,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
		})
	}
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("server %s connect: %v", name, err)
	}
	return ct
}

// TestMCPManager_RegisterTools_RollbackSparesWinner is the I3 contract test.
// Server configs "foo_bar" and "foo-bar" sanitize to the same namespaced
// prefix ("foo_bar__..."), so their "y" tools collide once both are
// discovered. "foo_bar" registers first (the winner) and its two tools
// ("foo_bar__x", "foo_bar__y") succeed outright. "foo-bar" (the loser)
// registers its own non-colliding tool ("foo_bar__w") first, then hits the
// collision on "foo_bar__y" — which already belongs to the winner. Only
// "foo-bar"'s own "foo_bar__w" must be rolled back; "foo_bar__y" must
// survive untouched because it was never in "foo-bar"'s own added list.
func TestMCPManager_RegisterTools_RollbackSparesWinner(t *testing.T) {
	ctx := context.Background()

	ctWinner := newRollbackTestServer(t, "winner", "x", "y")
	ctLoser := newRollbackTestServer(t, "loser", "w", "y")

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "foo_bar", Type: "stdio"},
		{Name: "foo-bar", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ctWinner), staticDial(ctLoser)})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	regOutcomes := mgr.RegisterTools(reg)

	if len(regOutcomes) != 1 || regOutcomes[0].Name != "foo-bar" || regOutcomes[0].Stage != "register" {
		t.Fatalf("want one register outcome for server %q, got %+v", "foo-bar", regOutcomes)
	}
	if regOutcomes[0].Err == nil || !strings.Contains(regOutcomes[0].Err.Error(), "foo_bar__y") {
		t.Errorf("outcome error %v does not mention the colliding name foo_bar__y", regOutcomes[0].Err)
	}

	// The winner's tools survive untouched, including the colliding name.
	if reg.Get("foo_bar__x") == nil {
		t.Error("winner's foo_bar__x should survive")
	}
	if reg.Get("foo_bar__y") == nil {
		t.Error("winner's foo_bar__y (the collision winner) should survive")
	}
	// The loser's own, previously-successful registration must be rolled back.
	if reg.Get("foo_bar__w") != nil {
		t.Error("loser's own foo_bar__w should have been rolled back")
	}

	servers := mgr.Servers()
	byName := make(map[string]mcpconfig.ServerInfo, len(servers))
	for _, s := range servers {
		byName[s.Name] = s
	}
	if got := byName["foo-bar"]; got.Status != "failed" {
		t.Errorf("loser server status = %q, want failed (%+v)", got.Status, got)
	}
	if got := byName["foo_bar"]; got.Status != "connected" {
		t.Errorf("winner server status = %q, want connected (%+v)", got.Status, got)
	}
}

// TestMCPManager_ToolDefinitions_DefinitionsRebuiltAfterRegisterFailure is the
// I1 contract test: ToolDefinitions() must be rebuilt from the registered
// set, not the connected set. A server whose one tool fails name validation
// contributes zero entries once RegisterTools has run, while a healthy
// sibling's tool remains present.
func TestMCPManager_ToolDefinitions_DefinitionsRebuiltAfterRegisterFailure(t *testing.T) {
	ctx := context.Background()

	// "longservername__" (16 chars) + a 60-char tool name = 76 chars, over
	// the 64-char provider limit — fails validation at register time.
	longToolName := strings.Repeat("a", 60)
	ctLong := newRollbackTestServer(t, "longservername_src", longToolName)
	ctHealthy := newRollbackTestServer(t, "healthy_src", "ping")

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "longservername", Type: "stdio"},
		{Name: "healthy", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ctLong), staticDial(ctHealthy)})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	regOutcomes := mgr.RegisterTools(reg)
	if len(regOutcomes) != 1 || regOutcomes[0].Name != "longservername" || regOutcomes[0].Stage != "register" {
		t.Fatalf("want one register outcome for server %q, got %+v", "longservername", regOutcomes)
	}

	defs := mgr.ToolDefinitions()
	if len(defs) != 1 || defs[0].Name != "healthy__ping" {
		names := make([]string, len(defs))
		for i, d := range defs {
			names[i] = d.Name
		}
		t.Fatalf("ToolDefinitions() = %v, want exactly [healthy__ping]", names)
	}
	if reg.Get("healthy__ping") == nil {
		t.Error("healthy sibling's tool should be registered")
	}
}
