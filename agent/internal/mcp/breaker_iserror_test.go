package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// registerStubServer connects a stub MCP server exposing a single "probe"
// tool that answers every request with result, counting the requests that
// actually reach it, and registers its tools into a fresh tool.Registry
// exactly as production does.
func registerStubServer(ctx context.Context, t *testing.T, result *mcpsdk.CallToolResult) (*tool.Registry, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "probe",
		Description: "Always answers identically",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		requests.Add(1)
		return result, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	return reg, &requests
}

// TestMCPBreaker_IsErrorFailuresParkAtThird proves the breaker counts MCP
// failures that arrive through the ordinary dispatch error path: the manager
// turns a server-set IsError into a Go error, so the ledger sees a failure
// without anything reading a recorded is_error flag.
func TestMCPBreaker_IsErrorFailuresParkAtThird(t *testing.T) {
	ctx := context.Background()
	reg, requests := registerStubServer(ctx, t, &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom: upstream 400"}},
	})
	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	call := llm.ToolCallData{ID: "c", Name: "s__probe", Arguments: json.RawMessage(`{}`)}

	first := reg.ExecuteCall(ctx, env, call)
	if !first.IsError {
		t.Fatalf("call 1: IsError=false, output=%q", first.Output)
	}
	if strings.Contains(first.Output, "same failure") {
		t.Errorf("call 1 carried a nudge: %q", first.Output)
	}

	second := reg.ExecuteCall(ctx, env, call)
	if !second.IsError {
		t.Fatalf("call 2: IsError=false, output=%q", second.Output)
	}
	if !strings.HasSuffix(second.Output, "You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach") {
		t.Errorf("call 2 missing the failure nudge: %q", second.Output)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("server saw %d requests after 2 calls, want 2", got)
	}

	third := reg.ExecuteCall(ctx, env, call)
	if !third.IsError {
		t.Fatalf("call 3: IsError=false, output=%q", third.Output)
	}
	want := "serf did not execute this call: s__probe with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach."
	if !strings.HasPrefix(third.Output, want) {
		t.Errorf("call 3 park text = %q, want prefix %q", third.Output, want)
	}
	if !strings.Contains(third.Output, "boom: upstream 400") {
		t.Errorf("call 3 park text omits the failure snippets: %q", third.Output)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("server saw %d requests after 3 calls, want 2 — the parked call reached the server", got)
	}
}

// TestMCPBreaker_IdenticalBodiesNudgeWithoutIsError replays the
// 034163AU8MmLapfXKT7nMu shape: the chrome plugin returned its failure with
// IsError false and the failure as plain body text, byte-identical every
// time. The repetition trigger catches it without serf knowing anything
// about the plugin's error convention — and never parks, so every call still
// reaches the server.
func TestMCPBreaker_IdenticalBodiesNudgeWithoutIsError(t *testing.T) {
	ctx := context.Background()
	const body = "Error: set_viewport requires payload with width and height: {width,height,deviceScaleFactor?,mobile?}"
	reg, requests := registerStubServer(ctx, t, &mcpsdk.CallToolResult{
		IsError: false,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: body}},
	})
	env := &agenttest.FakeEnv{WorkDir: t.TempDir()}
	call := llm.ToolCallData{ID: "c", Name: "s__probe", Arguments: json.RawMessage(`{}`)}
	// The nudge counts the actual streak (Jesse ruling 2026-08-07), so each
	// call expects its own number.
	nudgeAt := func(count int) string {
		return fmt.Sprintf("You have now made this same call and received the identical result %d times in a row. Repeating it will not change the answer — use the result you already have, or change your approach.", count)
	}

	first := reg.ExecuteCall(ctx, env, call)
	if first.IsError {
		t.Fatalf("call 1: IsError=true, output=%q", first.Output)
	}
	if first.Output != body {
		t.Errorf("call 1 output = %q, want the plain body %q", first.Output, body)
	}

	for n := 2; n <= 4; n++ {
		res := reg.ExecuteCall(ctx, env, call)
		if res.IsError {
			t.Fatalf("call %d: IsError=true, output=%q", n, res.Output)
		}
		if !strings.HasPrefix(res.Output, body) {
			t.Errorf("call %d dropped the result body: %q", n, res.Output)
		}
		if !strings.HasSuffix(res.Output, nudgeAt(n)) {
			t.Errorf("call %d missing the repetition nudge for count %d: %q", n, n, res.Output)
		}
		if got := requests.Load(); got != int64(n) {
			t.Fatalf("server saw %d requests after %d calls: repetition must never park", got, n)
		}
	}
}
